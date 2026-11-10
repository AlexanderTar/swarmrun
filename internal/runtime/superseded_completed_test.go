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
