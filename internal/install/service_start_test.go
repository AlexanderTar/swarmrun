package install

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/execx"
)

// scripted answers each argv from its own queue, repeating the last answer, and
// records the calls in order. execx.Fake can only give one answer per command.
type scripted struct {
	mu    sync.Mutex
	steps map[string][]error
	calls []string
}

func (s *scripted) runner() execx.Runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls = append(s.calls, key)
		q, ok := s.steps[key]
		if !ok {
			return nil, errors.New("scripted: unexpected command: " + key)
		}
		err := q[0]
		if len(q) > 1 {
			s.steps[key] = q[1:]
		}
		return nil, err
	}
}

// noSleep replaces launchdSleep for the test and returns the total time it was
// asked to wait, so a test can assert the bound without waiting for it.
func noSleep(t *testing.T) *time.Duration {
	var slept time.Duration
	old := launchdSleep
	launchdSleep = func(_ context.Context, d time.Duration) error { slept += d; return nil }
	t.Cleanup(func() { launchdSleep = old })
	return &slept
}

var (
	errNotFound = errors.New("launchctl: exit status 113: Could not find service \"dev.swarm.daemon\" in domain for port")
	errBusy     = errors.New("launchctl: exit status 5: Bootstrap failed: 5: Input/output error")
)

const (
	cmdBootout   = "launchctl bootout gui/501/dev.swarm.daemon"
	cmdPrint     = "launchctl print gui/501/dev.swarm.daemon"
	cmdBootstrap = "launchctl bootstrap gui/501 "
)

func count(calls []string, key string) int {
	n := 0
	for _, c := range calls {
		if c == key {
			n++
		}
	}
	return n
}

// bootout returns as soon as launchd has signalled the daemon, which then takes
// seconds to exit; bootstrap before the job is gone fails with exit 5.
func TestStartWaitsUntilTheJobIsUnloadedBeforeBootstrap(t *testing.T) {
	noSleep(t)
	c := testConfig(t)
	boot := cmdBootstrap + PlistPath(c)
	s := &scripted{steps: map[string][]error{
		cmdBootout: {nil},
		cmdPrint:   {nil, nil, errNotFound},
		boot:       {nil},
	}}
	if err := (launchdService{c}).Start(bg, s.runner(), nil); err != nil {
		t.Fatal(err)
	}
	want := []string{cmdBootout, cmdPrint, cmdPrint, cmdPrint, boot}
	if !slices.Equal(s.calls, want) {
		t.Fatalf("calls = %v, want %v", s.calls, want)
	}
}

func TestStartRetriesBootstrapOnExit5(t *testing.T) {
	noSleep(t)
	c := testConfig(t)
	boot := cmdBootstrap + PlistPath(c)
	s := &scripted{steps: map[string][]error{
		cmdBootout: {nil},
		cmdPrint:   {errNotFound},
		boot:       {errBusy, errBusy, nil},
	}}
	if err := (launchdService{c}).Start(bg, s.runner(), nil); err != nil {
		t.Fatal(err)
	}
	if n := count(s.calls, boot); n != 3 {
		t.Fatalf("bootstrap ran %d times, want 3: %v", n, s.calls)
	}
}

func TestStartGivesUpAfterTheBound(t *testing.T) {
	c := testConfig(t)
	boot := cmdBootstrap + PlistPath(c)

	t.Run("bootstrap keeps failing with 5", func(t *testing.T) {
		slept := noSleep(t)
		s := &scripted{steps: map[string][]error{cmdBootout: {nil}, cmdPrint: {errNotFound}, boot: {errBusy}}}
		err := (launchdService{c}).Start(bg, s.runner(), nil)
		if err == nil || !strings.Contains(err.Error(), "launchctl bootstrap") || !strings.Contains(err.Error(), "Input/output error") {
			t.Fatalf("err = %v", err)
		}
		if n := count(s.calls, boot); n < 2 || *slept > time.Minute {
			t.Fatalf("bootstrap ran %d times, slept %v: not retried, or unbounded", n, *slept)
		}
	})

	t.Run("the job never unloads", func(t *testing.T) {
		slept := noSleep(t)
		s := &scripted{steps: map[string][]error{cmdBootout: {nil}, cmdPrint: {nil}, boot: {nil}}}
		err := (launchdService{c}).Start(bg, s.runner(), nil)
		if err == nil || !strings.Contains(err.Error(), "still loaded") {
			t.Fatalf("err = %v", err)
		}
		if count(s.calls, boot) != 0 || *slept > time.Minute {
			t.Fatalf("bootstrapped a loaded job, or slept %v", *slept)
		}
	})
}

func TestStartDoesNotWaitForAJobThatWasNotLoaded(t *testing.T) {
	slept := noSleep(t)
	c := testConfig(t)
	boot := cmdBootstrap + PlistPath(c)
	s := &scripted{steps: map[string][]error{cmdBootout: {errors.New("exit status 3: No such process")}, boot: {nil}}}
	if err := (launchdService{c}).Start(bg, s.runner(), nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{cmdBootout, boot}; !slices.Equal(s.calls, want) || *slept != 0 {
		t.Fatalf("calls = %v, slept %v", s.calls, *slept)
	}
}

func TestStartDoesNotRetryOtherBootstrapErrors(t *testing.T) {
	noSleep(t)
	c := testConfig(t)
	boot := cmdBootstrap + PlistPath(c)
	s := &scripted{steps: map[string][]error{
		cmdBootout: {nil}, cmdPrint: {errNotFound},
		boot: {errors.New("launchctl: exit status 122: Bootstrap failed: 122: Path had bad ownership/permissions")},
	}}
	err := (launchdService{c}).Start(bg, s.runner(), nil)
	if err == nil || !strings.Contains(err.Error(), "launchctl bootstrap") {
		t.Fatalf("err = %v", err)
	}
	if n := count(s.calls, boot); n != 1 {
		t.Fatalf("bootstrap ran %d times, want 1", n)
	}
}
