package adapter

import (
	"strings"
	"testing"
)

// backgroundSignal reports the optional per-kind "background command running"
// pane signal (BUG-172): false for kinds without the BackgroundWaker
// capability or with a nil regex, else the regex over the ANSI-stripped
// capture. Kept out of Busy()/Idle(): it says nothing about whether the
// worker is working.
func backgroundSignal(ad Adapter, capture string) bool {
	bw, ok := ad.(BackgroundWaker)
	if !ok {
		return false
	}
	bz := bw.BackgroundRunning()
	if bz == nil {
		return false
	}
	return bz.MatchString(StripANSI(capture))
}

// BUG-172: claude and muse re-invoke an idle agent when its background
// command exits, so the daemon must see the running command in the pane.
// Fixtures are verbatim pane-tail excerpts from the 2026-11-10 live probes
// (research BUG-172/background-wake.md, captures in
// ~/.swarm/screenshots/BUG-172/).
func TestBackgroundRunningSignal(t *testing.T) {
	d := testDeps(t)
	tests := []struct {
		name string
		new  func() Adapter
		pane string
		want bool
	}{
		// Claude: the idle mode line carries the shell count while a
		// background shell runs, and drops it once the shell exits.
		{"claude running shell", func() Adapter { return newClaude(d) },
			pane(t, "claude", "pane-background-running.txt"), true},
		{"claude plural shells", func() Adapter { return newClaude(d) },
			strings.Replace(pane(t, "claude", "pane-background-running.txt"),
				"· 1 shell\n", "· 3 shells\n", 1), true},
		{"claude after exit", func() Adapter { return newClaude(d) },
			pane(t, "claude", "pane-background-exited.txt"), false},
		{"claude idle no job", func() Adapter { return newClaude(d) },
			pane(t, "claude", "pane-idle.txt"), false},
		{"claude busy turn", func() Adapter { return newClaude(d) },
			pane(t, "claude", "pane-busy.txt"), false},
		// Muse: the task row under the composer reads "running" plus the
		// elapsed time while the command runs; a finished row does not.
		// The running fixture also carries a transcript line containing
		// "running" (PROBE_DONE running), which must not match: only the
		// └ row under the composer counts.
		{"muse running task", func() Adapter { return newMuse(d) },
			pane(t, "muse", "pane-background-running.txt"), true},
		{"muse running task alt glyph", func() Adapter { return newMuse(d) },
			strings.Replace(pane(t, "muse", "pane-background-running.txt"),
				"└ ◈", "└ ◆", 1), true},
		{"muse after wake", func() Adapter { return newMuse(d) },
			pane(t, "muse", "pane-background-finished.txt"), false},
		{"muse idle no job", func() Adapter { return newMuse(d) },
			pane(t, "muse", "pane-idle.txt"), false},
		{"muse busy turn", func() Adapter { return newMuse(d) },
			pane(t, "muse", "pane-busy-ansi.txt"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := backgroundSignal(tc.new(), tc.pane); got != tc.want {
				t.Fatalf("background signal = %v, want %v", got, tc.want)
			}
		})
	}
}

// Only the kinds with a proven-live idle wake on background-command exit
// (claude, muse) carry the BackgroundWaker capability with the wake fact on.
// Grok's docs claim a wake but no live idle wake was recorded; codex, cursor
// and agy have none: they stay off (no method) and keep the stall alert.
func TestBackgroundWakeFactOnlyForProvenKinds(t *testing.T) {
	d := testDeps(t)
	for _, tc := range []struct {
		name string
		new  func() Adapter
		wake bool
	}{
		{"claude", func() Adapter { return newClaude(d) }, true},
		{"muse", func() Adapter { return newMuse(d) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bw, ok := tc.new().(BackgroundWaker)
			if !ok {
				t.Fatalf("%s does not implement BackgroundWaker", tc.name)
			}
			if bw.BackgroundRunning() == nil {
				t.Fatalf("%s reports no background signal", tc.name)
			}
			if bw.WakesOnBackgroundExit() != tc.wake {
				t.Fatalf("%s WakesOnBackgroundExit = %v, want %v",
					tc.name, bw.WakesOnBackgroundExit(), tc.wake)
			}
		})
	}
	for _, tc := range []struct {
		name string
		new  func() Adapter
	}{
		{"grok", func() Adapter { return newGrok(d) }},
		{"codex", func() Adapter { return newCodex(d) }},
		{"cursor", func() Adapter { return newCursor(d) }},
		{"agy", func() Adapter { return newAgy(d) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if bw, ok := tc.new().(BackgroundWaker); ok && bw.BackgroundRunning() != nil {
				t.Fatalf("%s reports a background signal; keep the stall alert", tc.name)
			}
		})
	}
}
