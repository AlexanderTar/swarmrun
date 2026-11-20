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

// replacementFixture models BUG-180: a build step's first agent recorded
// red/green for units 1 and 2, then was cancelled; a fresh agent runs the
// step in round 2 of the same workflow in a git worktree. It returns the
// fresh coder's session, the worktree dir and the unix second the earlier
// agent's last evidence was recorded at.
func replacementFixture(t *testing.T) (s *Store, coderSes Session, dir string, evidenceSec int64) {
	t.Helper()
	ctx := context.Background()
	s, _, _ = newStore(t)
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
	var vs []Verify
	for _, u := range []int{1, 2} {
		vs = append(vs,
			Verify{Cmd: "go test ./x", Phase: "red", OK: false, Unit: u},
			Verify{Cmd: "go test ./x", Phase: "green", OK: true, Unit: u})
	}
	if _, err := s.WriteCheckpoint(ctx, prevSes.ID, CheckpointInput{Kind: Progress, Summary: "units 1-2", Verification: vs}); err != nil {
		t.Fatal(err)
	}
	var ms int64
	if err := s.DB.QueryRowContext(ctx, `SELECT MAX(created_at) FROM checkpoints WHERE agent_id = ?`, prev.ID).Scan(&ms); err != nil {
		t.Fatal(err)
	}

	dir = gitRepoNoSigning(t)
	repoID := ids.New("repo")
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO repos (id, path, name, default_branch, source, created_at, updated_at)
		VALUES (?, ?, 'proj', 'main', 'manual', 1, 1)`, repoID, dir); err != nil {
		t.Fatal(err)
	}
	seedRWWorktreeAt(t, s, repoID, coder.ID, orch.RootItemID, dir)
	return s, coderSes, dir, ms / 1000
}

// commitAt makes one commit whose committer date is unix second sec.
func commitAt(t *testing.T, dir, name string, sec int64) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
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

// Units 1-2 were proven by the cancelled first agent and committed while it
// ran; the fresh agent only owes the unit it did itself.
func TestTDDGateCarriesEarlierAgentUnitsInHistory(t *testing.T) {
	s, ses, dir, sec := replacementFixture(t)
	commitAt(t, dir, "u1", sec)
	commitAt(t, dir, "u2", sec)
	head := commitAt(t, dir, "u3", sec+3600)
	if err := completeUnit3(t, s, ses, head); err != nil {
		t.Fatalf("units 1-2 carry from the earlier agent: %v", err)
	}
}

// A unit the fresh agent touched again needs its own red then green.
func TestTDDGateDoesNotCarryUnitChangedThisRound(t *testing.T) {
	s, ses, dir, sec := replacementFixture(t)
	commitAt(t, dir, "u1", sec)
	commitAt(t, dir, "u2", sec)
	head := commitAt(t, dir, "u2b", sec+3600)
	err := completeUnit3(t, s, ses, head, Verify{Cmd: "go test ./x", Phase: "red", OK: false, Unit: 2})
	want := `TDD evidence missing for unit(s) 2: record red then green with "unit": <n>.` + hintCopy
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// Evidence with no commit from the earlier agent's window in the new sha's
// history is not proof the work is there.
func TestTDDGateDoesNotCarryWithoutCommitsInHistory(t *testing.T) {
	s, ses, dir, sec := replacementFixture(t)
	commitAt(t, dir, "old", sec-3600)
	head := commitAt(t, dir, "u3", sec+3600)
	err := completeUnit3(t, s, ses, head)
	want := `TDD evidence missing for unit(s) 1,2: record red then green with "unit": <n>.` + hintCopy
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}
