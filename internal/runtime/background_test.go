package runtime

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/adapter"
	"github.com/AlexanderTar/swarmrun/internal/workflow"
)

// bgSignalAdapter is the Fake with a background-running pane signal: the test
// stand-in for the claude/muse/grok BackgroundWaker over a canned capture. Kinds
// mapped to the plain Fake have no BackgroundWaker and keep the stall alert.
type bgSignalAdapter struct {
	*adapter.Fake
	signal *regexp.Regexp
	wake   bool
}

func (b *bgSignalAdapter) BackgroundRunning() *regexp.Regexp { return b.signal }
func (b *bgSignalAdapter) WakesOnBackgroundExit() bool       { return b.wake }

const (
	// bgRunningCapture keeps the Fake's idle prompt (so the worker still
	// reads waiting) with a background-task line the stub signal matches.
	bgRunningCapture = "─────\n❯ \n─────\nBACKGROUND_TASK_RUNNING\n"
	bgIdleCapture    = "─────\n❯ \n─────\n"
)

func selfWaking(kind AgentKind) bool { return kind == Claude || kind == Muse || kind == Grok }

func selfWakingKinds() []AgentKind {
	var out []AgentKind
	for _, k := range AgentKinds {
		if selfWaking(k) {
			out = append(out, k)
		}
	}
	return out
}

// bgDeadlockSetup spawns the standard progress/deadlock worker (orchestrator
// plus coder with a progress checkpoint), reassigns the coder to kind, and
// registers that kind's adapter: a signaling self-wake stub for
// claude/muse/grok,
// the plain Fake (no BackgroundWaker) for every other kind.
func bgDeadlockSetup(t *testing.T, kind AgentKind) (*Store, *fakeTmux, *testClock, Agent, Agent, Session) {
	t.Helper()
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	fa := s.Adapters[Fake].(*adapter.Fake)
	if selfWaking(kind) {
		s.Adapters[kind] = &bgSignalAdapter{Fake: fa,
			signal: regexp.MustCompile("BACKGROUND_TASK_RUNNING"), wake: true}
	} else {
		s.Adapters[kind] = fa
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind = ? WHERE id = ?`,
		string(kind), w.ID); err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: w.Name, Command: "swarm-fake-agent"},
		Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}
	tm.captures[orch.Name] = []string{"working…\n"}
	s.Sync(ctx, wSes.ID, nil, 20)
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, w.ID)
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress, Summary: "midway"}); err != nil {
		t.Fatal(err)
	}
	return s, tm, at, orch, w, wSes
}

// tickThrough advances the clock by d in reconcile ticks under the quiet
// window, so a pane signal that stays on screen is seen continuously (one big
// jump would read as the command going quiet and a new one starting).
func tickThrough(t *testing.T, s *Store, at *testClock, d time.Duration) {
	t.Helper()
	const step = 4 * time.Minute
	for ; d > step; d -= step {
		at.Advance(step)
		reconcileOnce(t, s)
	}
	at.Advance(d)
	reconcileOnce(t, s)
}

func bgDeadlockRelays(t *testing.T, s *Store, orch Agent) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'
		AND payload_json LIKE '%"event":"progress_deadlock"%'`, orch.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// BUG-172: a worker that ended its turn after a progress checkpoint while its
// own background command still runs is waiting on a harness wake, not stalled.
// The deadlock relay holds for the self-waking kinds while the pane signal is
// seen, and still fires for every kind with no signal.
func TestBackgroundSignalSuppressesDeadlockForSelfWakingKinds(t *testing.T) {
	for _, kind := range AgentKinds {
		t.Run(string(kind), func(t *testing.T) {
			s, tm, at, orch, w, _ := bgDeadlockSetup(t, kind)
			showPane(tm, w.Name, bgRunningCapture)
			at.Advance(6 * time.Minute)
			reconcileOnce(t, s)
			want := 1
			if selfWaking(kind) {
				want = 0
			}
			if got := bgDeadlockRelays(t, s, orch); got != want {
				t.Fatalf("%s with background signal: %d progress_deadlock relays, want %d", kind, got, want)
			}
		})
	}
}

func TestDeadlockFiresWithoutBackgroundSignal(t *testing.T) {
	for _, kind := range AgentKinds {
		t.Run(string(kind), func(t *testing.T) {
			s, _, at, orch, _, _ := bgDeadlockSetup(t, kind)
			at.Advance(6 * time.Minute)
			reconcileOnce(t, s)
			if got := bgDeadlockRelays(t, s, orch); got != 1 {
				t.Fatalf("%s without background signal: %d progress_deadlock relays, want 1", kind, got)
			}
		})
	}
}

// Once the pane row disappears the alert needs five quiet minutes (the muse
// hookless wake gap, and a command that exited without ever waking its
// agent), then it fires.
func TestDeadlockFiresAfterBackgroundSignalGoesQuiet(t *testing.T) {
	for _, kind := range selfWakingKinds() {
		t.Run(string(kind), func(t *testing.T) {
			s, tm, at, orch, w, _ := bgDeadlockSetup(t, kind)
			showPane(tm, w.Name, bgRunningCapture)
			reconcileOnce(t, s)
			at.Advance(6 * time.Minute)
			reconcileOnce(t, s)
			if got := bgDeadlockRelays(t, s, orch); got != 0 {
				t.Fatalf("%s while signal seen: %d relays, want 0", kind, got)
			}
			showPane(tm, w.Name, bgIdleCapture)
			at.Advance(4*time.Minute + 59*time.Second)
			reconcileOnce(t, s)
			if got := bgDeadlockRelays(t, s, orch); got != 0 {
				t.Fatalf("%s 4:59 after the signal disappeared: %d relays, want 0", kind, got)
			}
			at.Advance(2 * time.Second)
			reconcileOnce(t, s)
			if got := bgDeadlockRelays(t, s, orch); got != 1 {
				t.Fatalf("%s 5:01 after the signal disappeared: %d relays, want 1", kind, got)
			}
		})
	}
}

// A background job that never exits still alerts past the 60-minute ceiling,
// and the relay says a background command was running so the orchestrator
// checks it instead of nudging blindly.
func TestDeadlockFiresPastBackgroundCapWithPayload(t *testing.T) {
	for _, kind := range selfWakingKinds() {
		t.Run(string(kind), func(t *testing.T) {
			s, tm, at, orch, w, _ := bgDeadlockSetup(t, kind)
			showPane(tm, w.Name, bgRunningCapture)
			reconcileOnce(t, s)
			tickThrough(t, s, at, busyCeiling+time.Minute)
			var payload string
			if err := s.DB.QueryRowContext(context.Background(), `SELECT payload_json FROM messages
				WHERE to_agent_id = ? AND kind = 'relay' AND payload_json LIKE '%"event":"progress_deadlock"%'`,
				orch.ID).Scan(&payload); err != nil {
				t.Fatalf("%s past the cap: expected exactly one progress_deadlock relay: %v", kind, err)
			}
			if !strings.Contains(payload, `"background_running":true`) {
				t.Fatalf("%s past the cap: relay payload missing background_running: %s", kind, payload)
			}
		})
	}
}

// The 60-minute cap counts from the start of the current background episode,
// not from the first command the session ever showed: a second long command,
// started after the first one went quiet for longer than the quiet window,
// gets its own hold even when it starts more than an hour after the first.
func TestBackgroundCapRestartsForANewCommand(t *testing.T) {
	for _, kind := range selfWakingKinds() {
		t.Run(string(kind), func(t *testing.T) {
			s, tm, at, orch, w, wSes := bgDeadlockSetup(t, kind)
			showPane(tm, w.Name, bgRunningCapture) // command A
			reconcileOnce(t, s)
			showPane(tm, w.Name, bgIdleCapture) // A exits
			at.Advance(10 * time.Minute)
			reconcileOnce(t, s)
			at.Advance(110 * time.Minute)
			showPane(tm, w.Name, bgRunningCapture) // command B, t=120m
			reconcileOnce(t, s)
			if _, err := s.WriteCheckpoint(context.Background(), wSes.ID,
				CheckpointInput{Kind: Progress, Summary: "running suite B in the background"}); err != nil {
				t.Fatal(err)
			}
			before := bgDeadlockRelays(t, s, orch)
			at.Advance(6 * time.Minute)
			reconcileOnce(t, s)
			if got := bgDeadlockRelays(t, s, orch); got != before {
				t.Fatalf("%s while command B runs: %d new progress_deadlock relays, want 0", kind, got-before)
			}
		})
	}
}

// A kind that shows the signal but has no wake fact keeps the alert, and the
// relay carries background_running so the orchestrator checks the command.
func TestDeadlockCarriesBackgroundHintForNonWakingKind(t *testing.T) {
	s, tm, at, orch, w, _ := bgDeadlockSetup(t, Codex)
	s.Adapters[Codex] = &bgSignalAdapter{Fake: s.Adapters[Fake].(*adapter.Fake),
		signal: regexp.MustCompile("BACKGROUND_TASK_RUNNING"), wake: false}
	showPane(tm, w.Name, bgRunningCapture)
	reconcileOnce(t, s)
	at.Advance(6 * time.Minute)
	reconcileOnce(t, s)
	var payloads []string
	rows, err := s.DB.QueryContext(context.Background(), `SELECT payload_json FROM messages
		WHERE to_agent_id = ? AND kind = 'relay' AND payload_json LIKE '%"event":"progress_deadlock"%'`, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, p)
	}
	if len(payloads) != 1 {
		t.Fatalf("non-waking kind with background signal: %d relays, want 1", len(payloads))
	}
	if !strings.Contains(payloads[0], `"background_running":true`) {
		t.Fatalf("relay payload missing background_running: %s", payloads[0])
	}
}

// bgStaleSetup starts a lone spike worker reassigned to kind with that kind's
// adapter (signaling stub for claude/muse/grok, plain Fake otherwise).
func bgStaleSetup(t *testing.T, kind AgentKind) (*Store, *fakeTmux, *testClock, string) {
	t.Helper()
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: string(kind) + "-bg-stale",
		Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	fa := s.Adapters[Fake].(*adapter.Fake)
	if selfWaking(kind) {
		s.Adapters[kind] = &bgSignalAdapter{Fake: fa,
			signal: regexp.MustCompile("BACKGROUND_TASK_RUNNING"), wake: true}
	} else {
		s.Adapters[kind] = fa
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind = ? WHERE id = ?`,
		string(kind), a.ID); err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: a.Name, Command: "zsh"})
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	return s, tm, tm.clk, a.Name
}

func TestBackgroundSignalSuppressesStaleForSelfWakingKinds(t *testing.T) {
	for _, kind := range AgentKinds {
		t.Run(string(kind), func(t *testing.T) {
			s, tm, at, name := bgStaleSetup(t, kind)
			showPane(tm, name, bgRunningCapture)
			at.Advance(31 * time.Minute)
			reconcileOnce(t, s)
			want := 1
			if selfWaking(kind) {
				want = 0
			}
			if got := notifiedCount(s, "agent.stale"); got != want {
				t.Fatalf("%s with background signal: %d stale alerts, want %d", kind, got, want)
			}
		})
	}
}

func TestStaleFiresAfterBackgroundSignalGoesQuiet(t *testing.T) {
	for _, kind := range selfWakingKinds() {
		t.Run(string(kind), func(t *testing.T) {
			s, tm, at, name := bgStaleSetup(t, kind)
			showPane(tm, name, bgRunningCapture)
			at.Advance(31 * time.Minute)
			reconcileOnce(t, s)
			if got := notifiedCount(s, "agent.stale"); got != 0 {
				t.Fatalf("%s while signal seen: %d stale alerts, want 0", kind, got)
			}
			showPane(tm, name, bgIdleCapture)
			at.Advance(5 * time.Minute)
			reconcileOnce(t, s)
			if got := notifiedCount(s, "agent.stale"); got != 1 {
				t.Fatalf("%s 5 min after the signal disappeared: %d stale alerts, want 1", kind, got)
			}
		})
	}
}

func TestStaleFiresPastBackgroundCap(t *testing.T) {
	for _, kind := range selfWakingKinds() {
		t.Run(string(kind), func(t *testing.T) {
			s, tm, at, name := bgStaleSetup(t, kind)
			showPane(tm, name, bgRunningCapture)
			reconcileOnce(t, s)
			tickThrough(t, s, at, busyCeiling+time.Minute)
			if got := notifiedCount(s, "agent.stale"); got != 1 {
				t.Fatalf("%s past the cap: %d stale alerts, want 1", kind, got)
			}
		})
	}
}

// The background signal stays out of the busy readout: no busy label, no
// 15-minute progress reminder clock, no urgent-interrupt change.
func TestBackgroundSignalLeavesBusySinceAlone(t *testing.T) {
	for _, kind := range selfWakingKinds() {
		t.Run(string(kind), func(t *testing.T) {
			s, tm, _, name := bgStaleSetup(t, kind)
			showPane(tm, name, bgRunningCapture)
			reconcileOnce(t, s)
			ses, err := s.LatestSession(context.Background(),
				agentIDFor(t, s, name))
			if err != nil {
				t.Fatal(err)
			}
			if since, ok := s.BusySince(ses.ID); ok {
				t.Fatalf("%s with background signal: BusySince = %v, want idle (no busy label)", kind, since)
			}
		})
	}
}

// BUG-172: the progress response's next tells a worker on a self-waking kind
// (claude, muse, grok) it may end the turn after naming its background command and
// must not poll; every other kind keeps waiting in the foreground.
func TestProgressNextDiffersForSelfWakingKinds(t *testing.T) {
	for _, tc := range []struct {
		kind     AgentKind
		selfWake bool
	}{
		{Claude, true}, {Muse, true}, {Grok, true},
		{Fake, false}, {Codex, false}, {Cursor, false}, {Agy, false},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			s, _, _ := newStore(t)
			ctx := context.Background()
			coder, coderSes, _ := buildOnly(t, s, workflow.GateTDD)
			if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind = ? WHERE id = ?`,
				string(tc.kind), coder.ID); err != nil {
				t.Fatal(err)
			}
			res, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: Progress,
				Summary:      "unit 1 red",
				Verification: []Verify{{Cmd: "go test ./x", Phase: "red", OK: false}}})
			if err != nil {
				t.Fatal(err)
			}
			if !tc.selfWake {
				if res.Next != progressKeepWorkingNext {
					t.Fatalf("%s Next = %q, want the foreground-wait text %q",
						tc.kind, res.Next, progressKeepWorkingNext)
				}
				return
			}
			if res.Next == progressKeepWorkingNext {
				t.Fatalf("%s Next is the foreground-wait text; want the self-wake text", tc.kind)
			}
			for _, want := range []string{"background command", "without polling"} {
				if !strings.Contains(res.Next, want) {
					t.Fatalf("%s Next = %q, want it to mention %q", tc.kind, res.Next, want)
				}
			}
		})
	}
}

func agentIDFor(t *testing.T, s *Store, name string) string {
	t.Helper()
	var id string
	if err := s.DB.QueryRowContext(context.Background(),
		`SELECT id FROM agents WHERE name = ?`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
