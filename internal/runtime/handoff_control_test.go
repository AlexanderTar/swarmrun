package runtime

import (
	"context"
	"encoding/json"
	"testing"
)

// controlActions returns the action of every control message in res.
func controlActions(t *testing.T, res SyncResult) []string {
	t.Helper()
	var out []string
	for _, e := range res.Messages {
		if e.Kind != "control" {
			continue
		}
		var p struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p.Action)
	}
	return out
}

// BUG-164: a handoff whose predecessor dies before acking the preservation
// control message must not hand that message to its successor, on the first
// sync or any later one.
func TestSuccessorNeverSeesPredecessorHandoffControl(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	pred, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, pred.ID, Running); err != nil {
		t.Fatal(err)
	}
	panes(tm) // the predecessor pane is gone: it dies without acking
	if _, err := s.RequestReplacement(ctx, orch.ID, ModeHandoff, "dies-unacked", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	succ, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if succ.ID == pred.ID {
		t.Fatal("setup: no successor session started")
	}
	for i := 1; i <= 2; i++ {
		res, err := s.Sync(ctx, succ.ID, nil, 50)
		if err != nil {
			t.Fatal(err)
		}
		if got := controlActions(t, res); len(got) != 0 {
			t.Fatalf("sync %d on the successor returned control actions %v, want none", i, got)
		}
	}
}

// A row an older daemon wrote (the successor was started without retiring
// the predecessor's handoff control) is swept at the next daemon start, so a
// successor already running does not keep receiving it.
func TestDaemonStartRetiresStaleHandoffControl(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	pred, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, pred.ID, Running); err != nil {
		t.Fatal(err)
	}
	panes(tm)
	if _, err := s.RequestReplacement(ctx, orch.ID, ModeHandoff, "old-binary", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	succ, err := s.LatestSession(ctx, orch.ID)
	if err != nil || succ.ID == pred.ID {
		t.Fatalf("setup: no successor session started (%v)", err)
	}
	// Put the row back the way an old binary left it.
	if _, err := s.DB.ExecContext(ctx, `UPDATE messages SET state = 'pending', acked_at = NULL
		WHERE kind = 'control' AND to_agent_id = ?`, orch.ID); err != nil {
		t.Fatal(err)
	}
	res, err := s.Sync(ctx, succ.ID, nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got := controlActions(t, res); len(got) == 0 {
		t.Fatal("setup: the stale control row should reach the successor before the sweep")
	}
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	res, err = s.Sync(ctx, succ.ID, nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got := controlActions(t, res); len(got) != 0 {
		t.Fatalf("after the daemon-start sweep the successor got control actions %v, want none", got)
	}
}
