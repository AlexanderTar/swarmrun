package runtime

import (
	"context"
	"testing"
)

// exhaustedFixture drives the seeded build->review workflow through every
// round with changes_requested until it escalates "rounds exhausted", and
// returns the build step's agent id at the escalated round.
func exhaustedFixture(t *testing.T) (s *Store, orch Agent, taskKey, wfID, builderID string) {
	t.Helper()
	s, _, _ = newStore(t)
	orch, taskKey = seedWorkflowTask(t, s, gatedBuildReviewSpec(t, 2))
	esc, _ := driveChangesRequested(t, s, orch, taskKey, 2, nil)
	if esc == "" {
		t.Fatal("setup: workflow did not escalate")
	}
	if err := s.DB.QueryRow(`SELECT id FROM workflows ORDER BY created_at DESC LIMIT 1`).Scan(&wfID); err != nil {
		t.Fatal(err)
	}
	return s, orch, taskKey, wfID, agentIDForStep(t, s, wfID, "build")
}

func newestBuildAgent(t *testing.T, s *Store, wfID string) string {
	t.Helper()
	var id string
	if err := s.DB.QueryRow(`SELECT COALESCE(agent_id, '') FROM workflow_runs
		WHERE workflow_id = ? AND step_id = 'build' ORDER BY round DESC LIMIT 1`, wfID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// BUG-178: a resume retry after the review rounds ran out must not revive a
// builder whose session already finished: it gets a fresh agent, so current
// role overrides and settings apply.
func TestResumeRetryAfterExhaustedRoundsSpawnsFreshAgentForFinishedBuilder(t *testing.T) {
	s, orch, taskKey, wfID, builderID := exhaustedFixture(t)
	ctx := context.Background()
	ses, err := s.LatestSession(ctx, builderID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, ses.ID, Completed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResumeWorkflow(ctx, orch, ResumeWorkflowInput{ItemKey: taskKey, Decision: "retry", Note: "one more"}); err != nil {
		t.Fatal(err)
	}
	got := newestBuildAgent(t, s, wfID)
	if got == "" || got == builderID {
		t.Fatalf("newest build run agent = %q, want a fresh agent (old %q)", got, builderID)
	}
}

// A builder whose session is still live keeps it across the same resume.
func TestResumeRetryAfterExhaustedRoundsKeepsLiveBuilder(t *testing.T) {
	s, orch, taskKey, wfID, builderID := exhaustedFixture(t)
	ctx := context.Background()
	if _, err := s.ResumeWorkflow(ctx, orch, ResumeWorkflowInput{ItemKey: taskKey, Decision: "retry", Note: "one more"}); err != nil {
		t.Fatal(err)
	}
	if got := newestBuildAgent(t, s, wfID); got != builderID {
		t.Fatalf("newest build run agent = %q, want the live builder %q", got, builderID)
	}
}
