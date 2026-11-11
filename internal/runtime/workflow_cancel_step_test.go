package runtime

import (
	"context"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/items"
)

// startCancelFixture starts a build->review workflow and returns the store,
// the orchestrator, the workflow id and the coder (build step) agent.
func startCancelFixture(t *testing.T) (s *Store, orch Agent, taskKey, wfID string, coder Agent) {
	t.Helper()
	ctx := context.Background()
	s, tm, _ := newStore(t)
	orch, taskKey = seedWorkflowTask(t, s, buildReviewSpec(t))
	wtID, _, _, _ := seedOwnedRepoWorktree(t, s, orch)
	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	coder, err = s.agentByID(ctx, agentIDForStep(t, s, st.ID, "build"))
	if err != nil {
		t.Fatal(err)
	}
	tm.env[coder.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, coder.ID)}
	return s, orch, taskKey, st.ID, coder
}

func workflowStateOf(t *testing.T, s *Store, wfID string) string {
	t.Helper()
	var st string
	if err := s.DB.QueryRowContext(context.Background(), `SELECT state FROM workflows WHERE id = ?`, wfID).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

// BUG-173/BUG-174: swarm_control cancel of a workflow step's agent used to
// leave the run 'active' and the workflow 'running' until the 30 s stall
// scan, so an orchestrator acting within seconds found no way to restart the
// step. Cancel now advances the workflow in the same call.
func TestCancelStepAgentEscalatesWorkflowAtOnce(t *testing.T) {
	s, orch, _, wfID, coder := startCancelFixture(t)
	ctx := context.Background()

	if _, err := s.Cancel(ctx, coder.Name, "", ""); err != nil {
		t.Fatal(err)
	}
	// No advance() call here: Cancel itself must have escalated.
	if got := workflowStateOf(t, s, wfID); got != "escalated" {
		t.Fatalf("workflow state right after Cancel = %s, want escalated", got)
	}
	var runState string
	if err := s.DB.QueryRowContext(ctx, `SELECT state FROM workflow_runs WHERE workflow_id = ? AND step_id = 'build'`, wfID).Scan(&runState); err != nil {
		t.Fatal(err)
	}
	if runState != "cancelled" {
		t.Fatalf("build run state = %s, want cancelled", runState)
	}
	if n := countEvent(relayEvents(t, s, orch.ID), "workflow_escalated"); n != 1 {
		t.Fatalf("workflow_escalated relays = %d, want 1", n)
	}
}

// Cancelling the whole workflow cancels its agents through Cancel; that must
// not turn into an escalation (CancelWorkflow flips the workflow first and
// holds its lock across the agent cancels).
func TestCancelWorkflowDoesNotEscalate(t *testing.T) {
	s, orch, taskKey, wfID, _ := startCancelFixture(t)
	ctx := context.Background()

	if _, err := s.CancelWorkflow(ctx, orch, taskKey, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := workflowStateOf(t, s, wfID); got != "cancelled" {
		t.Fatalf("workflow state = %s, want cancelled", got)
	}
	if n := countEvent(relayEvents(t, s, orch.ID), "workflow_escalated"); n != 0 {
		t.Fatalf("workflow_escalated relays = %d, want 0", n)
	}
}

// An orchestrator forcing the task Done cancels the workflow in the same
// transaction; Reconcile then cancels the step agent through Cancel, which
// must not escalate the already-cancelled workflow.
func TestForcedDoneDoesNotEscalate(t *testing.T) {
	s, orch, taskKey, wfID, coder := startCancelFixture(t)
	ctx := context.Background()

	task, err := s.Items.Get(ctx, taskKey)
	if err != nil {
		t.Fatal(err)
	}
	done := items.Done
	if _, err := s.Items.Update(ctx, taskKey, items.Patch{Status: &done, OverrideReason: "done by hand",
		Revision: task.Revision}, items.Orchestrator(orch.ID, orch.RootItemID)); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Agent(ctx, coder.Name); got.State != AgentFinished {
		t.Fatalf("coder = %s, want finished", got.State)
	}
	if got := workflowStateOf(t, s, wfID); got != "cancelled" {
		t.Fatalf("workflow state = %s, want cancelled", got)
	}
	if n := countEvent(relayEvents(t, s, orch.ID), "workflow_escalated"); n != 0 {
		t.Fatalf("workflow_escalated relays = %d, want 0", n)
	}
}

// Cancelling the task's item cancels the workflow first, then the agents.
func TestItemCancelDoesNotEscalate(t *testing.T) {
	s, orch, taskKey, wfID, coder := startCancelFixture(t)
	ctx := context.Background()

	if _, err := s.Items.Transition(ctx, taskKey, items.Cancelled, items.User("board")); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Agent(ctx, coder.Name); got.State != AgentFinished {
		t.Fatalf("coder = %s, want finished", got.State)
	}
	if got := workflowStateOf(t, s, wfID); got != "cancelled" {
		t.Fatalf("workflow state = %s, want cancelled", got)
	}
	if n := countEvent(relayEvents(t, s, orch.ID), "workflow_escalated"); n != 0 {
		t.Fatalf("workflow_escalated relays = %d, want 0", n)
	}
}
