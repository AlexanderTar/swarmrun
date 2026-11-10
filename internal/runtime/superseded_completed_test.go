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

// declineClose records what RequestChanges leaves on a close_spike request
// the user declined: state changes_requested, responded_at now. That durable
// row is what tells a declined completed from a stray note after a genuine one.
func declineClose(t *testing.T, s *Store, id string, a Agent, ses Session) {
	t.Helper()
	mustExec(t, s.DB, `INSERT INTO requests (id, kind, agent_id, session_id, item_id, prompt, state,
		binding_json, created_at, responded_at, responded_via)
		VALUES (?, 'close_spike', ?, ?, ?, 'found nothing to build', 'changes_requested',
		'{"resolution":"no_change"}', ?, ?, 'board')`,
		id, a.ID, ses.ID, a.ItemID, db.Millis(s.Now()), db.Millis(s.Now()))
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
	declineClose(t, s, "req_s82_close", orch, ses)
	at.Advance(time.Second)
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
	declineClose(t, s, "req_w_close", w, wSes)
	at.Advance(time.Second)
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

// A stray note after a GENUINE completed (no declined close) changes nothing:
// the agent is still finished, so its dead pane reads completed, with no
// agent.crashed notification, no crash relay, the workflow run stays completed
// and no retry agent is spawned.
func TestStrayCheckpointAfterAGenuineCompletedStaysCompleted(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	w, _ := s.agentByID(ctx, wSes.AgentID)
	insertAgentCkp(t, s, "ckp_w_completed", w, wSes, CompletedCkp, "done")
	at.Advance(time.Minute)
	insertAgentCkp(t, s, "ckp_w_stray", w, wSes, Progress, "Review is done, nothing left to do")
	mustExec(t, s.DB, `INSERT INTO workflows (id, item_id, root_item_id, owner_agent_id, state, round, extra_rounds,
		context_json, worktrees_json, created_at, updated_at) VALUES ('wf_stray', ?, ?, ?, 'running', 1, 0, '[]', '[]', 1, 1)`,
		w.ItemID, w.RootItemID, orch.ID)
	mustExec(t, s.DB, `INSERT INTO workflow_runs (id, workflow_id, step_id, round, role, agent_id, state, created_at)
		VALUES ('wfr_stray', 'wf_stray', 'build', 1, 'coder', ?, 'completed', 1)`, w.ID)
	var agentsBefore int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&agentsBefore)
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}
	deadPaneReconcile(t, s, tm, at, wSes, w.Name, Pane{Session: orch.Name, Command: "swarm-fake-agent"})

	if ses, _ := s.LatestSession(ctx, w.ID); ses.State != Completed {
		t.Fatalf("session state = %s, want completed: a stray note does not undo a genuine completed", ses.State)
	}
	f := s.Notify.(*fakeNotifier)
	f.mu.Lock()
	for _, n := range f.raised {
		if n.Kind == "agent.crashed" {
			t.Errorf("unexpected agent.crashed notification: %+v", n)
		}
	}
	f.mu.Unlock()
	var relays int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'
		AND payload_json LIKE '%"event":"crashed"%'`, orch.ID).Scan(&relays)
	if relays != 0 {
		t.Fatalf("crash relays = %d, want 0 for a finished agent", relays)
	}
	var runState string
	s.DB.QueryRowContext(ctx, `SELECT state FROM workflow_runs WHERE id = 'wfr_stray'`).Scan(&runState)
	if runState != "completed" {
		t.Fatalf("workflow run state = %s, want completed", runState)
	}
	var agentsAfter int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&agentsAfter)
	if agentsAfter != agentsBefore {
		t.Fatalf("agents %d -> %d: no retry may be spawned", agentsBefore, agentsAfter)
	}
}

func killedPane(tm *fakeTmux, before int, name string) bool {
	for _, k := range tm.killed[before:] {
		if k == name {
			return true
		}
	}
	return false
}

// replayStrayNote replays accepted, progress, completed, then a stray note
// with no declined close, for an orchestrator.
func replayStrayNote(t *testing.T, s *Store, at *testClock) (Agent, Session) {
	t.Helper()
	orch, _, _ := worker(t, s)
	ses, err := s.LatestSession(context.Background(), orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	insertAgentCkp(t, s, "ckp_st_accepted", orch, ses, Accepted, "starting")
	at.Advance(time.Minute)
	insertAgentCkp(t, s, "ckp_st_completed", orch, ses, CompletedCkp, "all done")
	at.Advance(15 * time.Minute)
	insertAgentCkp(t, s, "ckp_st_stray", orch, ses, Progress, "Review is done, nothing left to do")
	return orch, ses
}

// A stray note after a genuine completed leaves the finished-agent policy on:
// the 60 s idle kill and the 2 h busy cap still fire, and the session still
// reads as completed (so the progress nudge, final note and slot stay off).
func TestStrayCheckpointAfterAGenuineCompletedIsStillKilledWhenIdle(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, ses := replayStrayNote(t, s, at)
	panes(tm, Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	showPane(tm, orch.Name, paneIdlePrompt)
	at.Advance(61 * time.Second)
	before := len(tm.killed)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if !killedPane(tm, before, orch.Name) {
		t.Fatalf("killed = %v, want the idle finished pane %s", tm.killed[before:], orch.Name)
	}
}

func TestStrayCheckpointAfterAGenuineCompletedIsStillKilledAtTheBusyCap(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, ses := replayStrayNote(t, s, at)
	panes(tm, Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	showPane(tm, orch.Name, paneBusyFooter)
	at.Advance(killCompletedBusyCap + time.Second)
	before := len(tm.killed)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if !killedPane(tm, before, orch.Name) {
		t.Fatalf("killed = %v, want the busy finished pane %s", tm.killed[before:], orch.Name)
	}
}

// The progress nudge, final note and slot count all read the completed
// marker: a stray note must leave it set, a declined close must clear it.
func TestCompletedMarkerSurvivesAStrayNoteButNotADeclinedClose(t *testing.T) {
	for _, tc := range []struct {
		name   string
		replay func(*testing.T, *Store, *testClock) (Agent, Session)
		want   bool
	}{{"stray note", replayStrayNote, true}, {"declined close", replaySpike82, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, at := clockStore(t)
			a, _ := tc.replay(t, s, at)
			rows, err := s.liveSessionRows(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, r := range rows {
				if r.AgentID == a.ID {
					found = true
					if got := r.CompletedCheckpointAt != nil; got != tc.want {
						t.Fatalf("completed marker set = %v, want %v", got, tc.want)
					}
				}
			}
			if !found {
				t.Fatal("session not among the live rows")
			}
		})
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

// The real flow, no hand-written rows: a spike's no-change completed opens a
// close_spike request, the user requests changes, the agent writes a progress.
// The completed marker must clear (a declined close), and come back when the
// agent completes again afterwards.
func TestDeclinedCloseSpikeClearsTheCompletedMarkerUntilItCompletesAgain(t *testing.T) {
	s, _, at := clockStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Retry", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	marker := func() bool {
		rows, err := s.liveSessionRows(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.AgentID == a.ID {
				return r.CompletedCheckpointAt != nil
			}
		}
		t.Fatal("spike session not among the live rows")
		return false
	}
	s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})
	at.Advance(time.Minute)
	s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "nothing", Resolution: "no_change"})
	if !marker() {
		t.Fatal("a completed with an open close request must set the marker")
	}
	var reqID string
	s.DB.QueryRowContext(ctx, `SELECT id FROM requests WHERE kind = 'close_spike'`).Scan(&reqID)
	at.Advance(time.Minute)
	if _, err := s.RequestChanges(ctx, reqID, "Look at the session cookie path first.", "board"); err != nil {
		t.Fatal(err)
	}
	at.Advance(time.Second)
	s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Progress, Summary: "looking at cookies"})
	if marker() {
		t.Fatal("a declined close must clear the completed marker")
	}
	at.Advance(time.Minute)
	s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "still nothing", Resolution: "no_change"})
	if !marker() {
		t.Fatal("a new completed after the decline must set the marker again")
	}
}
