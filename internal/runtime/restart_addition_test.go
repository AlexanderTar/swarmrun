package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/attachments"
	"github.com/AlexanderTar/swarmrun/internal/db"
	"github.com/AlexanderTar/swarmrun/internal/items"
)

func restartBatches(home, key string) []string {
	matches, _ := filepath.Glob(filepath.Join(attachments.Dir(home, key), "batch-*"))
	return matches
}

func stopOrchestrator(t *testing.T, s *Store, itemKey string) Agent {
	t.Helper()
	ctx := context.Background()
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: itemKey, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	crashLatestSession(t, s, orch)
	return orch
}

func TestRestartOnStoppedOrchestratorCarriesRequestAndFile(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch := stopOrchestrator(t, s, "EPIC-1")
	beforeBrief := orch.Brief
	files := []attachments.File{{Name: "a.png", Ext: ".png", Body: onePNG}}
	const request = "Please read the notes."

	again, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1",
		Request: request, Attachments: files})
	if err != nil || again.ID != orch.ID {
		t.Fatalf("restart = %s, %v", again.ID, err)
	}
	got, err := s.AgentByID(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Brief, request) || !strings.Contains(got.Brief, "## Additional request") || !strings.Contains(got.Brief, beforeBrief) {
		t.Fatalf("stored brief = %q", got.Brief)
	}
	it, err := s.Items.Get(ctx, "EPIC-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(it.Brief, request) || !strings.Contains(it.Brief, "## Attachments") {
		t.Fatalf("description = %q", it.Brief)
	}
	if batches := restartBatches(s.Home, "EPIC-1"); len(batches) != 1 {
		t.Fatalf("batches = %v, want 1", batches)
	}
	path := attachmentPath(t, s.Home, "EPIC-1", "01-a.png")
	if !strings.Contains(got.Brief, path) || !strings.Contains(it.Brief, path) {
		t.Fatalf("path %s missing from brief or description", path)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.SyncRecovery(ctx, ses.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(view.Assignment.Brief, request) {
		t.Fatalf("sync brief = %q", view.Assignment.Brief)
	}
	synced, err := s.Sync(ctx, ses.ID, nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	notes := 0
	for _, m := range synced.Messages {
		if m.Kind == "assignment_update" && strings.Contains(string(m.Payload), request) {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("notes carrying the request = %d, want 1", notes)
	}
}

func TestRestartRefusalsLeaveNoBatch(t *testing.T) {
	t.Run("in flight", func(t *testing.T) {
		s, _, _ := newStore(t)
		ctx := context.Background()
		seedEpicWithTask(t, s)
		orch := stopOrchestrator(t, s, "EPIC-1")
		ses, err := s.LatestSession(ctx, orch.ID)
		if err != nil {
			t.Fatal(err)
		}
		now := db.Millis(s.Now())
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO agent_operations
			(id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
			VALUES ('op_busy', ?, 'recover', 'queued', 'k1', ?, ?, ?, ?)`,
			orch.ID, ses.ID, ses.Generation, now, now); err != nil {
			t.Fatal(err)
		}
		before, _ := s.Items.Get(ctx, "EPIC-1")
		_, _, err = s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1",
			Request: "again", Attachments: []attachments.File{{Name: "a.png", Ext: ".png", Body: onePNG}}})
		var ie *items.Error
		if !errors.As(err, &ie) || ie.Code != items.CodeConflict {
			t.Fatalf("err = %v, want a conflict", err)
		}
		if batches := restartBatches(s.Home, "EPIC-1"); len(batches) != 0 {
			t.Fatalf("batches = %v", batches)
		}
		it, _ := s.Items.Get(ctx, "EPIC-1")
		if it.Brief != before.Brief {
			t.Fatalf("description changed to %q", it.Brief)
		}
	})

	t.Run("preflight", func(t *testing.T) {
		s, _, _ := newStore(t)
		ctx := context.Background()
		seedEpicWithTask(t, s)
		stopOrchestrator(t, s, "EPIC-1")
		before, _ := s.Items.Get(ctx, "EPIC-1")
		_, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Claude, Model: "claude-sonnet-5",
			Request: "switch", Attachments: []attachments.File{{Name: "a.png", Ext: ".png", Body: onePNG}}})
		if err == nil || !strings.Contains(err.Error(), "isn't installed") {
			t.Fatalf("err = %v, want a preflight refusal", err)
		}
		if batches := restartBatches(s.Home, "EPIC-1"); len(batches) != 0 {
			t.Fatalf("batches = %v", batches)
		}
		it, _ := s.Items.Get(ctx, "EPIC-1")
		if it.Brief != before.Brief {
			t.Fatalf("description changed to %q", it.Brief)
		}
		var n int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_operations`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("operations = %d (%v), want none", n, err)
		}
	})
}

func TestRestartOverCapIs400WithNothingSaved(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch := stopOrchestrator(t, s, "EPIC-1")
	full := strings.Repeat("x", items.MaxBrief)
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET brief = ? WHERE id = ?`, full, orch.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1",
		Request: "this no longer fits", Attachments: []attachments.File{{Name: "a.png", Ext: ".png", Body: onePNG}}})
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeBadRequest || ie.Message != items.ErrBriefTooLong {
		t.Fatalf("err = %v, want 400 %q", err, items.ErrBriefTooLong)
	}
	if batches := restartBatches(s.Home, "EPIC-1"); len(batches) != 0 {
		t.Fatalf("batches = %v", batches)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_operations WHERE agent_id = ?`, orch.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("operations = %d (%v), want none", n, err)
	}
	got, _ := s.AgentByID(ctx, orch.ID)
	if got.Brief != full {
		t.Fatal("stored brief changed")
	}
}

func TestRestartSaveFailureStillRestartsAndSaysToAsk(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch := stopOrchestrator(t, s, "EPIC-1")
	if err := os.WriteFile(filepath.Join(s.Home, "attachments"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var failed bool
	again, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1",
		Request: "Please read the notes.", Attachments: []attachments.File{{Name: "a.png", Ext: ".png", Body: onePNG}},
		AttachmentsFailed: &failed})
	if err != nil || again.ID != orch.ID {
		t.Fatalf("restart = %s, %v", again.ID, err)
	}
	if !failed {
		t.Fatal("AttachmentsFailed was not set")
	}
	got, _ := s.AgentByID(ctx, orch.ID)
	it, _ := s.Items.Get(ctx, "EPIC-1")
	for _, text := range []string{got.Brief, it.Brief} {
		if !strings.Contains(text, "Please read the notes.") || !strings.Contains(text, "Ask the user to share them another way.") {
			t.Fatalf("text = %q", text)
		}
	}
	ses, _ := s.LatestSession(ctx, orch.ID)
	synced, err := s.Sync(ctx, ses.ID, nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range synced.Messages {
		if m.Kind == "assignment_update" && strings.Contains(string(m.Payload), "Ask the user to share them another way.") {
			found = true
		}
	}
	if !found {
		t.Fatal("the note does not tell the orchestrator to ask for the files")
	}
}

func TestBlockedRestartKeepsTextAndFiles(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch := stopOrchestrator(t, s, "EPIC-1")
	tm.startErr = errors.New("tmux refuses to start")
	_, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1",
		Request: "Keep this.", Attachments: []attachments.File{{Name: "a.png", Ext: ".png", Body: onePNG}}})
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeConflict {
		t.Fatalf("err = %v, want a blocked restart", err)
	}
	if batches := restartBatches(s.Home, "EPIC-1"); len(batches) != 1 {
		t.Fatalf("batches = %v, want the files kept", batches)
	}
	got, _ := s.AgentByID(ctx, orch.ID)
	it, _ := s.Items.Get(ctx, "EPIC-1")
	if !strings.Contains(got.Brief, "Keep this.") || !strings.Contains(it.Brief, "Keep this.") {
		t.Fatalf("brief = %q\ndescription = %q", got.Brief, it.Brief)
	}
}

// A phase update that fails after the operation commits is a walk error.
// The batch and the task description stay; only an uncommitted transaction
// removes the batch.
func TestRestartWalkErrorKeepsFilesAndDescription(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch := stopOrchestrator(t, s, "EPIC-1")
	if _, err := s.DB.ExecContext(ctx, `CREATE TRIGGER fail_phase_update
		BEFORE UPDATE OF phase ON agent_operations
		BEGIN SELECT RAISE(ABORT, 'constraint failed'); END`); err != nil {
		t.Fatal(err)
	}
	const request = "Keep this after the walk fails."
	_, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1",
		Request: request, Attachments: []attachments.File{{Name: "a.png", Ext: ".png", Body: onePNG}}})
	if err == nil || !strings.Contains(err.Error(), "constraint failed") {
		t.Fatalf("err = %v, want the phase update to fail", err)
	}
	if batches := restartBatches(s.Home, "EPIC-1"); len(batches) != 1 {
		t.Fatalf("batches = %v, want the files kept", batches)
	}
	got, _ := s.AgentByID(ctx, orch.ID)
	it, _ := s.Items.Get(ctx, "EPIC-1")
	if !strings.Contains(got.Brief, request) || !strings.Contains(it.Brief, request) {
		t.Fatalf("brief = %q\ndescription = %q", got.Brief, it.Brief)
	}
}

func TestRetriedBlockedRestartWithSameRequestIDSavesOnce(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch := stopOrchestrator(t, s, "EPIC-1")
	tm.startErr = errors.New("tmux refuses to start")
	files := []attachments.File{{Name: "a.png", Ext: ".png", Body: onePNG}}
	in := OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1",
		RequestID: "req-1", Request: "Keep this.", Attachments: files}
	for i := 0; i < 2; i++ {
		_, _, err := s.StartOrchestrator(ctx, in)
		var ie *items.Error
		if !errors.As(err, &ie) || ie.Code != items.CodeConflict {
			t.Fatalf("call %d: err = %v, want a blocked restart", i, err)
		}
	}
	if batches := restartBatches(s.Home, "EPIC-1"); len(batches) != 1 {
		t.Fatalf("batches = %v, want 1", batches)
	}
	got, _ := s.AgentByID(ctx, orch.ID)
	it, _ := s.Items.Get(ctx, "EPIC-1")
	if strings.Count(got.Brief, "Keep this.") != 1 {
		t.Fatalf("stored brief = %q, want the request once", got.Brief)
	}
	if strings.Count(it.Brief, "Keep this.") != 1 {
		t.Fatalf("description = %q, want the request once", it.Brief)
	}
}

func TestReplayedRestartReportsStoredAttachmentsFailed(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch := stopOrchestrator(t, s, "EPIC-1")
	tm.startErr = errors.New("tmux refuses to start")
	// Break saving so the first call records attachments_failed on its operation.
	if err := os.WriteFile(filepath.Join(s.Home, "attachments"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	files := []attachments.File{{Name: "a.png", Ext: ".png", Body: onePNG}}
	var firstFailed bool
	_, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1",
		RequestID: "req-replay-flag", Request: "Please read.", Attachments: files, AttachmentsFailed: &firstFailed})
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeConflict {
		t.Fatalf("first restart err = %v, want a blocked restart", err)
	}
	if !firstFailed {
		t.Fatal("first restart did not report attachments_failed")
	}
	// Heal saving: the replayed call saves fine, but the stored operation still failed.
	if err := os.Remove(filepath.Join(s.Home, "attachments")); err != nil {
		t.Fatal(err)
	}
	var replayFailed bool
	_, _, err = s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1",
		RequestID: "req-replay-flag", Request: "Please read.", Attachments: files, AttachmentsFailed: &replayFailed})
	if !errors.As(err, &ie) || ie.Code != items.CodeConflict {
		t.Fatalf("replay err = %v, want the blocked operation again", err)
	}
	if !replayFailed {
		t.Fatal("replayed restart did not report the stored attachments_failed")
	}
	got, _ := s.AgentByID(ctx, orch.ID)
	if strings.Count(got.Brief, "Please read.") != 1 {
		t.Fatalf("stored brief = %q, want the request once", got.Brief)
	}
	it, _ := s.Items.Get(ctx, "EPIC-1")
	if strings.Count(it.Brief, "Please read.") != 1 {
		t.Fatalf("description = %q, want the request once", it.Brief)
	}
}

func TestPlainRestartRetrySucceedsAfterRecovery(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch := stopOrchestrator(t, s, "EPIC-1")
	tm.startErr = errors.New("tmux refuses to start")
	in := OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1", RequestID: "req-plain"}
	_, _, err := s.StartOrchestrator(ctx, in)
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeConflict {
		t.Fatalf("first restart err = %v, want a blocked restart", err)
	}
	// The session recovers: the same plain retry must behave as on main and start fresh.
	tm.startErr = nil
	again, _, err := s.StartOrchestrator(ctx, in)
	if err != nil || again.ID != orch.ID {
		t.Fatalf("retry = %s, %v, want the orchestrator back", again.ID, err)
	}
}

func TestRestartFilesOnCancelledRootAreRefused(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	stopOrchestrator(t, s, "EPIC-1")
	before, _ := s.Items.Get(ctx, "EPIC-1")
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = ? WHERE key = 'EPIC-1'`, items.Cancelled); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1",
		Request: "do not save", Attachments: []attachments.File{{Name: "a.png", Ext: ".png", Body: onePNG}}})
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeConflict || ie.Message != "This item's root is closed." {
		t.Fatalf("err = %v, want a conflict that the root is closed", err)
	}
	if batches := restartBatches(s.Home, "EPIC-1"); len(batches) != 0 {
		t.Fatalf("batches = %v, want none", batches)
	}
	it, _ := s.Items.Get(ctx, "EPIC-1")
	if it.Brief != before.Brief {
		t.Fatalf("description = %q", it.Brief)
	}
}

func TestRestartWithNoEntriesLeavesBriefAndDescriptionUnchanged(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch := stopOrchestrator(t, s, "EPIC-1")
	beforeAgent, _ := s.AgentByID(ctx, orch.ID)
	beforeItem, _ := s.Items.Get(ctx, "EPIC-1")
	again, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil || again.ID != orch.ID {
		t.Fatalf("restart = %s, %v", again.ID, err)
	}
	got, _ := s.AgentByID(ctx, orch.ID)
	it, _ := s.Items.Get(ctx, "EPIC-1")
	if got.Brief != beforeAgent.Brief || it.Brief != beforeItem.Brief {
		t.Fatalf("brief or description changed")
	}
	if batches := restartBatches(s.Home, "EPIC-1"); len(batches) != 0 {
		t.Fatalf("batches = %v", batches)
	}
	var notes int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'assignment_update'`, orch.ID).Scan(&notes); err != nil {
		t.Fatal(err)
	}
	if notes != 0 {
		t.Fatalf("notes = %d, want none", notes)
	}
}
