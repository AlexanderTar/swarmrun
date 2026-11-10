package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/db"
)

// BUG-170: the SPIKE-82 orchestrator's pane died after a completed that a
// later checkpoint had superseded, and an older daemon recorded the session
// completed and the agent finished. The user's restart then minted a suffixed
// agent, so the still-open request stayed bound to the old agent id.
func TestStartRestartsASupersededCompletedOrchestratorInPlace(t *testing.T) {
	s, _, at := clockStore(t)
	ctx := context.Background()
	orch, ses := replaySpike82(t, s, at)
	mustExec(t, s.DB, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id, prompt, state, binding_json, created_at)
		VALUES ('req_s82_open', 'question', 1, ?, ?, ?, 'Approve the section?', 'open',
		'{"question":"Approve the section?","header":"Approve"}', ?)`,
		orch.ID, ses.ID, orch.ItemID, db.Millis(s.Now()))
	// What the daemon recorded for the dead pane before the fix.
	mustExec(t, s.DB, `UPDATE sessions SET state = 'completed', ended_at = ? WHERE id = ?`, db.Millis(s.Now()), ses.ID)
	mustExec(t, s.DB, `UPDATE agents SET state = 'finished', finished_at = ? WHERE id = ?`, db.Millis(s.Now()), orch.ID)
	at.Advance(time.Minute)

	again, queued, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil || queued {
		t.Fatalf("start: err = %v queued = %v", err, queued)
	}
	if again.ID != orch.ID || again.Name != orch.Name {
		t.Fatalf("start = %s/%q, want the same agent %s/%q (never a suffixed one)", again.ID, again.Name, orch.ID, orch.Name)
	}
	newSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if newSes.ID == ses.ID {
		t.Fatal("no successor session was started")
	}
	// The restarted agent still owns its open request and can recover its prompt.
	out, err := s.Ask(ctx, newSes.ID, AskInput{Kind: "native_prompt", ForMsg: "req_s82_open"})
	if err != nil {
		t.Fatalf("recover own open request after the restart: %v", err)
	}
	if out.NativePrompt == nil || out.NativePrompt.Question != "Approve the section?" {
		t.Fatalf("recovered prompt = %+v", out.NativePrompt)
	}
}
