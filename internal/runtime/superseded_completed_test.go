package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/db"
)

// BUG-170: SPIKE-82's orchestrator wrote `completed` (no-change spike close),
// the user declined the close, and the agent kept working in the SAME session
// and attempt. A completed checkpoint that a later agent-written checkpoint
// superseded must not count as the agent being done.

// insertAgentCkp writes one agent-written (daemon_written = 0) checkpoint row
// straight into the table, so a test can replay a sequence WriteCheckpoint's
// gates would refuse (an orchestrator's completed on an open root).
func insertAgentCkp(t *testing.T, s *Store, id string, a Agent, ses Session, kind CheckpointKind, summary string) {
	t.Helper()
	mustExec(t, s.DB, `INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind, attempt, summary,
		daemon_written, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		id, ses.ID, a.ID, a.ItemID, string(kind), ses.Attempt, summary, db.Millis(s.Now()))
}

// replaySpike82 replays accepted, progress, completed, then (close declined)
// a later progress, advancing the fake clock between each so created_at
// orders them. It returns the orchestrator and its live session.
func replaySpike82(t *testing.T, s *Store, at *testClock) (Agent, Session) {
	t.Helper()
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	insertAgentCkp(t, s, "ckp_s82_accepted", orch, ses, Accepted, "starting")
	at.Advance(time.Minute)
	insertAgentCkp(t, s, "ckp_s82_progress1", orch, ses, Progress, "researching")
	at.Advance(time.Minute)
	insertAgentCkp(t, s, "ckp_s82_completed", orch, ses, CompletedCkp, "no change: closing the spike")
	at.Advance(8 * time.Minute) // the user declines the close; the agent keeps going
	insertAgentCkp(t, s, "ckp_s82_progress2", orch, ses, Progress, "close declined, continuing")
	return orch, ses
}

func TestSupersededCompletedIsNotKilledByTheIdleKill(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, ses := replaySpike82(t, s, at)
	panes(tm, Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	showPane(tm, orch.Name, paneIdlePrompt)
	at.Advance(61 * time.Second)
	before := len(tm.killed)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.killed) != before {
		t.Fatalf("idle pane killed after a completed that a later checkpoint superseded: killed = %v", tm.killed[before:])
	}
}

func TestSupersededCompletedIsNotKilledAtTheBusyCap(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, ses := replaySpike82(t, s, at)
	panes(tm, Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	showPane(tm, orch.Name, paneBusyFooter)
	at.Advance(killCompletedBusyCap + time.Second)
	before := len(tm.killed)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.killed) != before {
		t.Fatalf("busy pane killed at the 2 h cap after a completed that a later checkpoint superseded: killed = %v", tm.killed[before:])
	}
}

// deadPaneReconcile reconciles a dead pane for name through the spawn grace
// and the alive-confirming tick, the same way the existing dead-pane tests do.
func deadPaneReconcile(t *testing.T, s *Store, tm *fakeTmux, at *testClock, ses Session, name string, others ...Pane) {
	t.Helper()
	ctx := context.Background()
	panes(tm, append([]Pane{{Session: name, Dead: true, DeadStatus: 137, Command: "swarm-fake-agent"}}, others...)...)
	tm.env[name] = map[string]string{"SWARM_SESSION": ses.ID}
	at.Advance(11 * time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	at.Advance(spawnGracePeriod + time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
}

// A dead pane after a superseded completed is a crash: crashed session,
// agent.crashed notification, relay to the live parent, agent left active.
func TestDeadPaneAfterASupersededCompletedIsCrashed(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	insertAgentCkp(t, s, "ckp_w_accepted", w, wSes, Accepted, "starting")
	at.Advance(time.Minute)
	insertAgentCkp(t, s, "ckp_w_progress1", w, wSes, Progress, "building")
	at.Advance(time.Minute)
	insertAgentCkp(t, s, "ckp_w_completed", w, wSes, CompletedCkp, "done")
	at.Advance(time.Minute)
	insertAgentCkp(t, s, "ckp_w_progress2", w, wSes, Progress, "more work after all")
	orchSes := mustSessionID(t, s, orch.ID)
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": orchSes}
	deadPaneReconcile(t, s, tm, at, wSes, w.Name, Pane{Session: orch.Name, Command: "swarm-fake-agent"})

	ses, _ := s.LatestSession(ctx, w.ID)
	if ses.State != Crashed {
		t.Fatalf("session state = %s, want crashed: a later checkpoint superseded the completed", ses.State)
	}
	if n := notified(t, s, "agent.crashed"); n.ItemKey != "TASK-1" {
		t.Fatalf("crash notification = %+v", n)
	}
	var relays int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'
		AND payload_json LIKE '%"event":"crashed"%'`, orch.ID).Scan(&relays)
	if relays != 1 {
		t.Fatalf("crash relays to the live ancestor = %d, want 1", relays)
	}
	if got, _ := s.Agent(ctx, w.Name); got.State != AgentActive {
		t.Fatalf("agent state = %s, want active until acknowledged", got.State)
	}
}

// The SPIKE-82 case itself: a top-level orchestrator whose pane dies.
func TestDeadOrchestratorAfterASupersededCompletedIsCrashed(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, ses := replaySpike82(t, s, at)
	deadPaneReconcile(t, s, tm, at, ses, orch.Name)
	got, _ := s.LatestSession(ctx, orch.ID)
	if got.State != Crashed {
		t.Fatalf("session state = %s, want crashed", got.State)
	}
	if a, _ := s.Agent(ctx, orch.Name); a.State != AgentActive {
		t.Fatalf("agent state = %s, want active (not finished)", a.State)
	}
	f := s.Notify.(*fakeNotifier)
	f.mu.Lock()
	defer f.mu.Unlock()
	var crashes int
	for _, n := range f.raised {
		if n.Kind == "agent.crashed" && n.AgentName == orch.Name {
			crashes++
		}
	}
	if crashes != 1 {
		t.Fatalf("agent.crashed notifications for %s = %d, want 1", orch.Name, crashes)
	}
}

// A stray checkpoint after a genuine completed turns the exit into a crash,
// but the crash path only fails workflow runs still active/waiting: a run
// that already completed stays completed and no retry agent is spawned.
func TestStrayCheckpointAfterACompletedWorkflowRunDoesNotRetryIt(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	insertAgentCkp(t, s, "ckp_w_completed", w, wSes, CompletedCkp, "done")
	at.Advance(time.Minute)
	insertAgentCkp(t, s, "ckp_w_stray", w, wSes, Progress, "stray note after completed")
	mustExec(t, s.DB, `INSERT INTO workflows (id, item_id, root_item_id, owner_agent_id, state, round, extra_rounds,
		context_json, worktrees_json, created_at, updated_at) VALUES ('wf_stray', ?, ?, ?, 'running', 1, 0, '[]', '[]', 1, 1)`,
		w.ItemID, w.RootItemID, orch.ID)
	mustExec(t, s.DB, `INSERT INTO workflow_runs (id, workflow_id, step_id, round, role, agent_id, state, created_at)
		VALUES ('wfr_stray', 'wf_stray', 'build', 1, 'coder', ?, 'completed', 1)`, w.ID)
	var agentsBefore int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&agentsBefore)
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}
	deadPaneReconcile(t, s, tm, at, wSes, w.Name, Pane{Session: orch.Name, Command: "swarm-fake-agent"})

	if ses, _ := s.LatestSession(ctx, w.ID); ses.State != Crashed {
		t.Fatalf("session state = %s, want crashed", ses.State)
	}
	var runState string
	s.DB.QueryRowContext(ctx, `SELECT state FROM workflow_runs WHERE id = 'wfr_stray'`).Scan(&runState)
	if runState != "completed" {
		t.Fatalf("workflow run state = %s, want completed (the crash path only fails active/waiting runs)", runState)
	}
	var agentsAfter int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&agentsAfter)
	if agentsAfter != agentsBefore {
		t.Fatalf("agents %d -> %d: a stray checkpoint must not spawn a retry", agentsBefore, agentsAfter)
	}
}

// A genuine final completed (nothing later) still completes the session.
// TestDeadPaneWithACompletedCheckpointCompletesTheSession covers it end to
// end; this pins the orchestrator variant against the new predicate: its own
// agent-written completed with a later checkpoint on a CHILD item only.
func TestDeadOrchestratorWithAGenuineCompletedAndOnlyChildNotesAfterIsCompleted(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	ses, _ := s.LatestSession(ctx, orch.ID)
	insertAgentCkp(t, s, "ckp_o_completed", orch, ses, CompletedCkp, "all done")
	at.Advance(time.Minute)
	child := orch
	child.ItemID = mustItemID(t, s, "TASK-1")
	insertAgentCkp(t, s, "ckp_o_child_note", child, ses, Progress, "board bookkeeping on a child")
	deadPaneReconcile(t, s, tm, at, ses, orch.Name)
	got, _ := s.LatestSession(ctx, orch.ID)
	if got.State != Completed {
		t.Fatalf("session state = %s, want completed: a note on a child item does not supersede", got.State)
	}
}
