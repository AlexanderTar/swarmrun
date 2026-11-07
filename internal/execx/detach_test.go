package execx

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func waitDone(t *testing.T, d *Detached) {
	t.Helper()
	select {
	case <-d.Done:
	case <-time.After(10 * time.Second):
		t.Fatal("the detached process never exited")
	}
}

func TestStartDetachedReportsAFailureWithItsStderr(t *testing.T) {
	d, err := StartDetached([]string{"SWARM_DETACH_PROBE=from-env"}, "sh", "-c", `echo "no display: $SWARM_DETACH_PROBE" >&2; exit 3`)
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, d)
	err = d.Err()
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "no display: from-env") {
		t.Fatalf("Err = %v", err)
	}
}

func TestStartDetachedRunsInItsOwnSessionWithoutADeadline(t *testing.T) {
	out := filepath.Join(t.TempDir(), "pid")
	// The child records its pid, then stays alive long enough to be asked for
	// its session id. Getsid works on Linux and macOS alike; `ps -o sid` does
	// not exist on macOS.
	d, err := StartDetached(nil, "sh", "-c", `echo $$ > "$0.tmp" && mv "$0.tmp" "$0"; sleep 1`, out)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for pid == 0 {
		if b, err := os.ReadFile(out); err == nil {
			if pid, err = strconv.Atoi(strings.TrimSpace(string(b))); err != nil {
				t.Fatalf("pid file = %q: %v", b, err)
			}
			break
		}
		select {
		case <-d.Done:
			t.Fatalf("exited before writing its pid: %v", d.Err())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the detached process never wrote its pid")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A session leader's session id is its own pid.
	sid, err := unix.Getsid(pid)
	if err != nil {
		t.Fatalf("getsid(%d): %v", pid, err)
	}
	if sid != pid {
		t.Fatalf("sid = %d, pid = %d, want the process to lead its own session", sid, pid)
	}
	waitDone(t, d)
	if err := d.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestStartDetachedKeepsOnlyTheStartOfStderr(t *testing.T) {
	d, err := StartDetached(nil, "sh", "-c", `i=0; while [ $i -lt 2000 ]; do printf 'xxxxxxxxxx' >&2; i=$((i+1)); done; exit 1`)
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, d)
	if err := d.Err(); err == nil || len(err.Error()) > detachedStderr+100 {
		t.Fatalf("Err length = %d", len(err.Error()))
	}
}

func TestStartDetachedReportsAMissingProgram(t *testing.T) {
	if _, err := StartDetached(nil, filepath.Join(t.TempDir(), "no-such-terminal")); err == nil {
		t.Fatal("started a program that does not exist")
	}
}

// The child sees exactly the environment it is given: nothing of the
// daemon's leaks through, so a stale variable can be left out.
func TestStartDetachedGivesTheChildOnlyItsEnv(t *testing.T) {
	t.Setenv("SWARM_DETACH_STALE", "from-the-daemon")
	out := filepath.Join(t.TempDir(), "env")
	d, err := StartDetached([]string{"SWARM_DETACH_GIVEN=yes"}, "sh", "-c", `echo "${SWARM_DETACH_STALE-unset} ${SWARM_DETACH_GIVEN-unset}" > "$0"`, out)
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, d)
	if b, _ := os.ReadFile(out); strings.TrimSpace(string(b)) != "unset yes" {
		t.Fatalf("child saw %q, want only the given variable", b)
	}
}

func TestStartDetachedKillStopsIt(t *testing.T) {
	d, err := StartDetached(nil, "sleep", "30")
	if err != nil {
		t.Fatal(err)
	}
	d.Kill()
	waitDone(t, d)
	if d.Err() == nil {
		t.Fatal("a killed process reported success")
	}
}
