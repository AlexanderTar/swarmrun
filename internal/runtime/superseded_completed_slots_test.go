package runtime

import (
	"context"
	"testing"
	"time"
)

// BUG-170: an agent that kept working after a completed that a later
// agent-written checkpoint superseded still occupies its max_concurrent_agents
// slot. Replays accepted, progress, completed, (close declined) progress.
func TestSupersededCompletedStillHoldsASlot(t *testing.T) {
	s, _, at := clockStore(t)
	ctx := context.Background()
	setLimits(t, s, 1)
	seedEpicWithTwoTasks(t, s)
	first, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "one"}})
	if err != nil || queued {
		t.Fatalf("first = %v, queued = %v, err = %v", first.Name, queued, err)
	}
	ses, err := s.LatestSession(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	insertAgentCkp(t, s, "ckp_slot_accepted", first, ses, Accepted, "starting")
	at.Advance(time.Minute)
	insertAgentCkp(t, s, "ckp_slot_progress1", first, ses, Progress, "building")
	at.Advance(time.Minute)
	insertAgentCkp(t, s, "ckp_slot_completed", first, ses, CompletedCkp, "done")
	at.Advance(time.Minute)
	insertAgentCkp(t, s, "ckp_slot_progress2", first, ses, Progress, "close declined, continuing")

	_, queued, err = s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if !queued {
		t.Fatal("the reopened agent must still hold the only slot: second spawn was admitted")
	}
}
