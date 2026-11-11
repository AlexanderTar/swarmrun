package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/items"
	"github.com/AlexanderTar/swarmrun/internal/workflow"
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

// BUG-173: after a cancelled round-2 builder escalates the workflow,
// swarm_workflow resume retry must restart the build step and leave the
// earlier rounds' run rows untouched.
func TestResumeRetryRestartsCancelledStepKeepingRounds(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, gatedBuildReviewSpec(t, 3))
	wtID, _, _, head := seedOwnedRepoWorktree(t, s, orch)
	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	// Round 1: build completes, review asks for changes -> round 2 build.
	coderSes := agentSessionForStep(t, s, st.ID, "build")
	if _, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
		Git: []GitRef{{Repo: "proj", Branch: "main", SHA: head, Dirty: false}}}); err != nil {
		t.Fatal(err)
	}
	reviewerSes := agentSessionForStep(t, s, st.ID, "review")
	if _, err := s.WriteCheckpoint(ctx, reviewerSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "needs work",
		Verdict:  "changes_requested",
		Findings: []workflow.Finding{{Severity: "major", File: "a.go", Line: 3, Summary: "off by one"}}}); err != nil {
		t.Fatal(err)
	}
	coder, err := s.agentByID(ctx, agentIDForStep(t, s, st.ID, "build"))
	if err != nil {
		t.Fatal(err)
	}
	tm.env[coder.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, coder.ID)}

	if _, err := s.Cancel(ctx, coder.Name, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := workflowStateOf(t, s, st.ID); got != "escalated" {
		t.Fatalf("setup: workflow state = %s, want escalated", got)
	}
	runRows := func() []string {
		rows, err := s.DB.QueryContext(ctx, `SELECT step_id || '/' || round || '/' || role || '/' || state || '/' || COALESCE(agent_id, '')
			FROM workflow_runs WHERE workflow_id = ? ORDER BY round, step_id`, st.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var r string
			if err := rows.Scan(&r); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		return out
	}
	before := runRows()

	final, err := s.ResumeWorkflow(ctx, orch, ResumeWorkflowInput{ItemKey: taskKey, Decision: "retry", Note: "restart the build"})
	if err != nil {
		t.Fatal(err)
	}
	if final.State != "running" {
		t.Fatalf("workflow state after resume = %s, want running", final.State)
	}
	after := runRows()
	if len(after) != len(before)+1 {
		t.Fatalf("run rows before %v, after %v; want exactly one new row", before, after)
	}
	for i, r := range before {
		if after[i] != r {
			t.Fatalf("earlier row changed: %q -> %q", r, after[i])
		}
	}
	var newState, newAgent string
	if err := s.DB.QueryRowContext(ctx, `SELECT state, COALESCE(agent_id, '') FROM workflow_runs
		WHERE workflow_id = ? AND step_id = 'build' AND round = ?`, st.ID, final.Round).Scan(&newState, &newAgent); err != nil {
		t.Fatal(err)
	}
	if newState != "active" || newAgent == "" {
		t.Fatalf("build run at round %d = state %s agent %q, want active with an agent", final.Round, newState, newAgent)
	}
	if ses, err := s.LatestSession(ctx, newAgent); err != nil || !ses.State.Live() {
		t.Fatalf("restarted build session = %+v err=%v, want live", ses, err)
	}
}

// A cancelled reviewer escalates at once too, and resume retry restarts the
// review at the same sha in a fresh round without touching round 1's rows.
func TestResumeRetryRestartsCancelledReviewer(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, gatedBuildReviewSpec(t, 3))
	wtID, _, _, head := seedOwnedRepoWorktree(t, s, orch)
	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	coderSes := agentSessionForStep(t, s, st.ID, "build")
	if _, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
		Git: []GitRef{{Repo: "proj", Branch: "main", SHA: head, Dirty: false}}}); err != nil {
		t.Fatal(err)
	}
	reviewer, err := s.agentByID(ctx, agentIDForStep(t, s, st.ID, "review"))
	if err != nil {
		t.Fatal(err)
	}
	tm.env[reviewer.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, reviewer.ID)}
	if _, err := s.Cancel(ctx, reviewer.Name, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := workflowStateOf(t, s, st.ID); got != "escalated" {
		t.Fatalf("setup: workflow state = %s, want escalated", got)
	}

	final, err := s.ResumeWorkflow(ctx, orch, ResumeWorkflowInput{ItemKey: taskKey, Decision: "retry", Note: "restart the review"})
	if err != nil {
		t.Fatal(err)
	}
	if final.State != "running" {
		t.Fatalf("workflow state after resume = %s, want running", final.State)
	}
	var oldState, newState, newAgent string
	if err := s.DB.QueryRowContext(ctx, `SELECT state FROM workflow_runs WHERE workflow_id = ? AND step_id = 'review' AND round = 1`, st.ID).Scan(&oldState); err != nil {
		t.Fatal(err)
	}
	if oldState != "cancelled" {
		t.Fatalf("round 1 review row = %s, want cancelled (kept)", oldState)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT state, COALESCE(agent_id, '') FROM workflow_runs
		WHERE workflow_id = ? AND step_id = 'review' AND round = ?`, st.ID, final.Round).Scan(&newState, &newAgent); err != nil {
		t.Fatal(err)
	}
	if newState != "active" || newAgent == "" {
		t.Fatalf("review run at round %d = state %s agent %q, want active with an agent", final.Round, newState, newAgent)
	}
}

// BUG-174: swarm_control retry on a cancelled workflow step agent used to
// refuse ("isn't in a state that can be retried"). A bare Retry would also
// leave a live agent with no active run row, so it goes through the engine:
// same effect as swarm_workflow resume retry with the note.
func TestRetryCancelledStepAgentRestartsThroughWorkflow(t *testing.T) {
	s, _, _, wfID, coder := startCancelFixture(t)
	ctx := context.Background()
	if _, err := s.Cancel(ctx, coder.Name, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := workflowStateOf(t, s, wfID); got != "escalated" {
		t.Fatalf("setup: workflow state = %s, want escalated", got)
	}

	got, err := s.Retry(ctx, coder.Name, "carry on from the last commit", "", "")
	if err != nil {
		t.Fatalf("Retry on a cancelled step agent: %v", err)
	}
	if st := workflowStateOf(t, s, wfID); st != "running" {
		t.Fatalf("workflow state after Retry = %s, want running", st)
	}
	var round1, round2State, round2Agent string
	if err := s.DB.QueryRowContext(ctx, `SELECT state FROM workflow_runs WHERE workflow_id = ? AND step_id = 'build' AND round = 1`, wfID).Scan(&round1); err != nil {
		t.Fatal(err)
	}
	if round1 != "cancelled" {
		t.Fatalf("round 1 build run = %s, want cancelled (kept)", round1)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT state, COALESCE(agent_id, '') FROM workflow_runs
		WHERE workflow_id = ? AND step_id = 'build' AND round = 2`, wfID).Scan(&round2State, &round2Agent); err != nil {
		t.Fatal(err)
	}
	if round2State != "active" || round2Agent == "" {
		t.Fatalf("round 2 build run = %s agent %q, want active with an agent", round2State, round2Agent)
	}
	if got.ID != round2Agent {
		t.Fatalf("Retry returned agent %s, want the restarted step's agent %s", got.ID, round2Agent)
	}
	// The note reaches the restarted step's brief.
	var brief string
	if err := s.DB.QueryRowContext(ctx, `SELECT brief FROM agents WHERE id = ?`, round2Agent).Scan(&brief); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(brief, "carry on from the last commit") {
		t.Fatalf("restarted step's brief lacks the retry note:\n%s", brief)
	}
}

// A live agent can't be retried: the refusal names the next step.
func TestRetryLiveStepAgentRefusalNamesNextStep(t *testing.T) {
	s, _, _, _, coder := startCancelFixture(t)
	_, err := s.Retry(context.Background(), coder.Name, "", "", "")
	if err == nil {
		t.Fatal("Retry on a live step agent succeeded, want a refusal")
	}
	for _, want := range []string{"swarm_control cancel", "swarm_workflow resume"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not name %q", err, want)
		}
	}
}

// A cancelled agent that is not a workflow step has no retry path either; the
// refusal says to spawn a replacement.
func TestRetryCancelledPlainAgentRefusalNamesNextStep(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, _ := worker(t, s)
	if _, err := s.Cancel(ctx, w.Name, "", ""); err != nil {
		t.Fatal(err)
	}
	_, err := s.Retry(ctx, w.Name, "", "", "")
	if err == nil || !strings.Contains(err.Error(), "swarm_spawn") {
		t.Fatalf("refusal = %v, want one naming swarm_spawn", err)
	}
}
