package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/db"
	"github.com/AlexanderTar/swarmrun/internal/ids"
	"github.com/AlexanderTar/swarmrun/internal/workflow"
)

// replacementFixture models BUG-180: a build step's first agent is cancelled
// and a fresh agent runs the step in round 2 of the same workflow in a shared
// git worktree. record writes one unit's red then green as the earlier agent
// (a checkpoint of its own, so the daemon anchors the unit to the worktree
// HEAD of that moment); endSec is the unix second its activity ended at.
type replacementEnv struct {
	s      *Store
	ses    Session // the fresh agent's session
	dir    string
	record func(unit int)
	endSec func() int64
}

func replacementFixture(t *testing.T) replacementEnv {
	t.Helper()
	ctx := context.Background()
	s, _, _ := newStore(t)
	orch, coder, coderSes := worker(t, s)
	setItemWorkflow(t, s, "TASK-1", workflow.Spec{Steps: []workflow.Step{
		{ID: "build", Run: "coder", Gates: []workflow.Gate{workflow.GateTDD}},
		{ID: "review", Review: []string{"reviewer"}, Of: "build"},
	}})
	setItemUnits(t, s, "TASK-1", "one", "two", "three")
	it, err := s.Items.Get(ctx, "TASK-1")
	if err != nil {
		t.Fatal(err)
	}
	wfID, _ := seedWorkflowRun(t, s, it.ID, orch.RootItemID, orch.ID, coder.ID, "build", "coder", 2)

	prev, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "first build"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO workflow_runs
		(id, workflow_id, step_id, round, role, agent_id, state, created_at)
		VALUES (?, ?, 'build', 1, 'coder', ?, 'cancelled', ?)`, ids.New("wfr"), wfID, prev.ID, db.Millis(s.Now())); err != nil {
		t.Fatal(err)
	}
	prevSes, err := s.LatestSession(ctx, prev.ID)
	if err != nil {
		t.Fatal(err)
	}

	dir := gitRepoNoSigning(t)
	commitAt(t, dir, "base", 1700000000)
	repoID := ids.New("repo")
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO repos (id, path, name, default_branch, source, created_at, updated_at)
		VALUES (?, ?, 'proj', 'main', 'manual', 1, 1)`, repoID, dir); err != nil {
		t.Fatal(err)
	}
	// Both agents work in the one worktree: the earlier one's reservation is
	// held while it records; the fresh one's is what the gate reads.
	wtID := seedRWWorktreeAt(t, s, repoID, prev.ID, orch.RootItemID, dir)
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO worktree_reservations
		(worktree_id, agent_id, mode, created_at) VALUES (?, ?, 'rw', ?)`, wtID, coder.ID, db.Millis(s.Now())); err != nil {
		t.Fatal(err)
	}
	return replacementEnv{s: s, ses: coderSes, dir: dir,
		record: func(unit int) {
			t.Helper()
			if _, err := s.WriteCheckpoint(ctx, prevSes.ID, CheckpointInput{Kind: Progress, Summary: fmt.Sprintf("unit %d", unit),
				Verification: []Verify{
					{Cmd: "go test ./x", Phase: "red", OK: false, Unit: unit},
					{Cmd: "go test ./x", Phase: "green", OK: true, Unit: unit}}}); err != nil {
				t.Fatal(err)
			}
		},
		endSec: func() int64 {
			var ms int64
			if err := s.DB.QueryRowContext(ctx, `SELECT MAX(created_at) FROM checkpoints WHERE agent_id = ?`, prev.ID).Scan(&ms); err != nil {
				t.Fatal(err)
			}
			return ms / 1000
		}}
}

// commitAt makes one commit whose committer date is unix second sec. A file
// already written by an earlier commit is rewritten.
func commitAt(t *testing.T, dir, name string, sec int64) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(fmt.Sprint(name, sec)), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", name}, {"commit", "-m", name}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), fmt.Sprintf("GIT_COMMITTER_DATE=@%d", sec), fmt.Sprintf("GIT_AUTHOR_DATE=@%d", sec))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return strings.TrimSpace(gitOutput(t, dir, "rev-parse", "HEAD"))
}

func completeUnit3(t *testing.T, s *Store, ses Session, head string, extra ...Verify) error {
	t.Helper()
	vs := append(extra,
		Verify{Cmd: "go test ./x", Phase: "red", OK: false, Unit: 3},
		Verify{Cmd: "go test ./x", Phase: "green", OK: true, Unit: 3})
	_, err := s.WriteCheckpoint(context.Background(), ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
		Git: []GitRef{{Repo: "proj", Branch: "main", SHA: head}}, Verification: vs})
	return err
}

// Units 1-2 were proven by the cancelled first agent, each committed right
// after its green; the fresh agent only owes the unit it did itself.
func TestTDDGateCarriesEarlierAgentUnitsInHistory(t *testing.T) {
	e := replacementFixture(t)
	e.record(1)
	commitAt(t, e.dir, "u1", e.endSec())
	e.record(2)
	commitAt(t, e.dir, "u2", e.endSec())
	head := commitAt(t, e.dir, "u3", e.endSec()+3600)
	if err := completeUnit3(t, e.s, e.ses, head); err != nil {
		t.Fatalf("units 1-2 carry from the earlier agent: %v", err)
	}
}

// A unit the fresh agent started again (a red with no green) needs its own.
func TestTDDGateDoesNotCarryUnitChangedThisRound(t *testing.T) {
	e := replacementFixture(t)
	e.record(1)
	commitAt(t, e.dir, "u1", e.endSec())
	e.record(2)
	commitAt(t, e.dir, "u2", e.endSec())
	head := commitAt(t, e.dir, "u2b", e.endSec()+3600)
	err := completeUnit3(t, e.s, e.ses, head, Verify{Cmd: "go test ./x", Phase: "red", OK: false, Unit: 2})
	want := `TDD evidence missing for unit(s) 2: record red then green with "unit": <n>.` + hintCopy
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// The fresh agent rewrote a carried unit's file and recorded nothing for it:
// the unit changed in this round, so its old evidence no longer counts.
func TestTDDGateDoesNotCarryUnitRewrittenSilently(t *testing.T) {
	e := replacementFixture(t)
	e.record(1)
	commitAt(t, e.dir, "u1", e.endSec())
	e.record(2)
	commitAt(t, e.dir, "u2", e.endSec())
	commitAt(t, e.dir, "u2", e.endSec()+3600) // same file, new content
	head := commitAt(t, e.dir, "u3", e.endSec()+3601)
	err := completeUnit3(t, e.s, e.ses, head)
	want := `TDD evidence missing for unit(s) 2: record red then green with "unit": <n>.` + hintCopy
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// The earlier agent proved unit 2 but never committed it; the fresh agent's
// own commit later in history must not vouch for it.
func TestTDDGateDoesNotCarryUncommittedEarlierUnit(t *testing.T) {
	e := replacementFixture(t)
	e.record(1)
	commitAt(t, e.dir, "u1", e.endSec())
	e.record(2) // no commit for unit 2
	commitAt(t, e.dir, "u2", e.endSec()+3600)
	head := commitAt(t, e.dir, "u3", e.endSec()+3601)
	err := completeUnit3(t, e.s, e.ses, head)
	want := `TDD evidence missing for unit(s) 2: record red then green with "unit": <n>.` + hintCopy
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// A commit merged in from another branch is not the earlier agent's unit.
func TestTDDGateDoesNotCarryFromMergedInCommit(t *testing.T) {
	e := replacementFixture(t)
	e.record(1)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = e.dir
		cmd.Env = append(os.Environ(), fmt.Sprintf("GIT_COMMITTER_DATE=@%d", e.endSec()), fmt.Sprintf("GIT_AUTHOR_DATE=@%d", e.endSec()))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("checkout", "-q", "-b", "other")
	commitAt(t, e.dir, "foreign", e.endSec())
	run("checkout", "-q", "main")
	run("merge", "--no-ff", "-q", "-m", "merge other", "other")
	head := commitAt(t, e.dir, "u3", e.endSec()+3600)
	err := completeUnit3(t, e.s, e.ses, head)
	want := `TDD evidence missing for unit(s) 1,2: record red then green with "unit": <n>.` + hintCopy
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// Evidence with no commit after it in the new sha's history is not proof the
// work is there.
func TestTDDGateDoesNotCarryWithoutCommitsInHistory(t *testing.T) {
	e := replacementFixture(t)
	e.record(1)
	e.record(2)
	head := commitAt(t, e.dir, "u3", e.endSec()+3600)
	err := completeUnit3(t, e.s, e.ses, head)
	want := `TDD evidence missing for unit(s) 1,2: record red then green with "unit": <n>.` + hintCopy
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// Two greens recorded before a single commit anchor to the same HEAD, so the
// first commit after it cannot say which unit it holds: neither carries.
func TestTDDGateDoesNotCarryUnitsSharingAnAnchor(t *testing.T) {
	e := replacementFixture(t)
	e.record(1)
	e.record(2)
	commitAt(t, e.dir, "u1", e.endSec())
	commitAt(t, e.dir, "u2", e.endSec()+3600) // the fresh agent's, no evidence
	head := commitAt(t, e.dir, "u3", e.endSec()+3601)
	err := completeUnit3(t, e.s, e.ses, head)
	want := `TDD evidence missing for unit(s) 1,2: record red then green with "unit": <n>.` + hintCopy
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// A carried unit's file with a non-ASCII name, rewritten this round, still
// counts as changed.
func TestTDDGateDoesNotCarryUnitRewrittenWithNonASCIIName(t *testing.T) {
	e := replacementFixture(t)
	e.record(1)
	commitAt(t, e.dir, "é1", e.endSec())
	e.record(2)
	commitAt(t, e.dir, "u2", e.endSec())
	commitAt(t, e.dir, "é1", e.endSec()+3600)
	head := commitAt(t, e.dir, "u3", e.endSec()+3601)
	err := completeUnit3(t, e.s, e.ses, head)
	want := `TDD evidence missing for unit(s) 1: record red then green with "unit": <n>.` + hintCopy
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}
