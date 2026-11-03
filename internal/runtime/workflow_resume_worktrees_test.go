package runtime

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/items"
	"github.com/AlexanderTar/swarmrun/internal/workflow"
)

// escalatedStoryReview starts STORY-1's after_tasks review on a ro tree and
// has the reviewer request changes, leaving the workflow escalated with the
// stored tree (returned with its directory) still recorded.
func escalatedStoryReview(t *testing.T) (s *Store, orch Agent, wfID, oldWT, oldDir string) {
	t.Helper()
	s, _, _ = newStore(t)
	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx, `UPDATE settings SET value_json = '["fake"]' WHERE key = 'enabled_agents'`); err != nil {
		t.Fatal(err)
	}
	s.Events.Notify()
	ep := seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: ep.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	setItemWorkflow(t, s, "STORY-1", workflow.Spec{AfterTasks: &workflow.Step{ID: "story-review", Review: []string{"reviewer"}}})
	w, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake, Model: "fake-1",
		ParentAgentID: orch.ID, Brief: BriefInput{Objective: "build it"}})
	if err != nil {
		t.Fatal(err)
	}
	wSes, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "started"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
		Verification: []Verify{{Cmd: "go test ./...", Phase: "green", OK: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Items.Transition(ctx, "TASK-1", items.Done, items.Daemon()); err != nil {
		t.Fatal(err)
	}
	oldWT, _, oldDir, _ = seedOwnedRepoWorktree(t, s, orch)
	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: "STORY-1",
		Worktrees: []WorkflowWorktree{{WorktreeID: oldWT, Mode: "ro"}}})
	if err != nil {
		t.Fatal(err)
	}
	revSes := agentSessionForStep(t, s, st.ID, "story-review")
	if _, err := s.WriteCheckpoint(ctx, revSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "needs rework",
		Verdict:  "changes_requested",
		Findings: []workflow.Finding{{Severity: "major", File: "main.go", Summary: "missing check"}}}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.WorkflowFor(ctx, "STORY-1"); got.State != "escalated" {
		t.Fatalf("setup: workflow state = %s, want escalated", got.State)
	}
	return s, orch, st.ID, oldWT, oldDir
}

// removeWorktree is what the daemon does to a tree after it is merged: the row
// goes 'removed' and the directory is deleted.
func removeWorktree(t *testing.T, s *Store, id, dir string) {
	t.Helper()
	if _, err := s.DB.ExecContext(context.Background(), `UPDATE worktrees SET state = 'removed' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
}

func TestResumeRetryReplacesStoryWorktreeAndBriefsReviewerOnIt(t *testing.T) {
	s, orch, wfID, oldWT, oldDir := escalatedStoryReview(t)
	ctx := context.Background()
	removeWorktree(t, s, oldWT, oldDir)
	newWT, _, newDir, _ := seedOwnedRepoWorktree(t, s, orch)

	st, err := s.ResumeWorkflow(ctx, orch, ResumeWorkflowInput{ItemKey: "STORY-1", Decision: "retry",
		Worktrees: []WorkflowWorktree{{WorktreeID: newWT, Mode: "ro"}}})
	if err != nil {
		t.Fatalf("resume retry with a fresh tree: %v", err)
	}
	if st.State != "running" {
		t.Fatalf("workflow state = %s (escalation %q), want running", st.State, st.Escalation)
	}
	a, err := s.agentByID(ctx, agentIDForStep(t, s, wfID, "story-review"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.Brief, newDir) {
		t.Errorf("reviewer brief does not name the new tree %s:\n%s", newDir, a.Brief)
	}
	if strings.Contains(a.Brief, oldDir) {
		t.Errorf("reviewer brief still names the removed tree %s:\n%s", oldDir, a.Brief)
	}
}

func TestResumeRetryWorktreeRefusalsMirrorStart(t *testing.T) {
	s, orch, _, oldWT, oldDir := escalatedStoryReview(t)
	ctx := context.Background()
	removeWorktree(t, s, oldWT, oldDir)

	// A tree that is not the caller's is refused, and the stored list is untouched.
	ep2 := seedEpicWithTask(t, s)
	other, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: ep2.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	foreignWT, _, _, _ := seedOwnedRepoWorktree(t, s, other)
	_, err = s.ResumeWorkflow(ctx, orch, ResumeWorkflowInput{ItemKey: "STORY-1", Decision: "retry",
		Worktrees: []WorkflowWorktree{{WorktreeID: foreignWT, Mode: "ro"}}})
	if err == nil || !strings.Contains(err.Error(), "Resume needs a worktree you own.") {
		t.Fatalf("foreign tree: err = %v, want the ownership refusal", err)
	}
	// An unknown tree and a bad mode are refused by the same validation as start.
	_, err = s.ResumeWorkflow(ctx, orch, ResumeWorkflowInput{ItemKey: "STORY-1", Decision: "retry",
		Worktrees: []WorkflowWorktree{{WorktreeID: "wt_nope", Mode: "ro"}}})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown tree: err = %v, want not found", err)
	}
	if wf, _, _ := s.WorkflowFor(ctx, "STORY-1"); wf.State != "escalated" {
		t.Fatalf("a refused resume must leave the workflow escalated, got %s", wf.State)
	}
}

func TestResumeAcceptFailRefuseWorktrees(t *testing.T) {
	s, orch, _, _, _ := escalatedStoryReview(t)
	ctx := context.Background()
	newWT, _, _, _ := seedOwnedRepoWorktree(t, s, orch)
	for _, decision := range []string{"accept", "fail"} {
		_, err := s.ResumeWorkflow(ctx, orch, ResumeWorkflowInput{ItemKey: "STORY-1", Decision: decision,
			Worktrees: []WorkflowWorktree{{WorktreeID: newWT, Mode: "ro"}}})
		if err == nil || !strings.Contains(err.Error(), `only valid with decision "retry"`) {
			t.Fatalf("%s with worktrees: err = %v, want refusal", decision, err)
		}
	}
	if wf, _, _ := s.WorkflowFor(ctx, "STORY-1"); wf.State != "escalated" {
		t.Fatalf("workflow state = %s, want still escalated", wf.State)
	}
}

func TestResumeRetryRefusesGoneStoredWorktreeAtResumeTime(t *testing.T) {
	s, orch, _, oldWT, oldDir := escalatedStoryReview(t)
	ctx := context.Background()
	removeWorktree(t, s, oldWT, oldDir)

	_, err := s.ResumeWorkflow(ctx, orch, ResumeWorkflowInput{ItemKey: "STORY-1", Decision: "retry"})
	if err == nil {
		t.Fatal("resume retry with a gone stored tree and no worktrees must be refused")
	}
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeBadRequest {
		t.Fatalf("err = %v, want bad_request", err)
	}
	if !strings.Contains(err.Error(), oldWT) || !strings.Contains(err.Error(), "worktrees") {
		t.Fatalf("err = %q, want it to name %s and tell the caller to pass worktrees", err, oldWT)
	}
	got, _, _ := s.WorkflowFor(ctx, "STORY-1")
	if got.State != "escalated" || got.ExtraRounds != 0 {
		t.Fatalf("refused resume changed the workflow: state %s extra_rounds %d", got.State, got.ExtraRounds)
	}

	// accept/fail do not brief anything, so a gone tree does not stop them.
	if _, err := s.ResumeWorkflow(ctx, orch, ResumeWorkflowInput{ItemKey: "STORY-1", Decision: "accept"}); err != nil {
		t.Fatalf("accept with a gone stored tree: %v", err)
	}
}
