package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/attachments"
	"github.com/AlexanderTar/swarmrun/internal/items"
)

func liveOrchestrator(t *testing.T, s *Store) Agent {
	t.Helper()
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(context.Background(), OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	return orch
}

func handoffWith(ctx context.Context, s *Store, id, key, note, request string, files []attachments.File) (Operation, error) {
	return s.RequestHandoffWithAddition(ctx, HandoffAddition{
		AgentID: id, RequestKey: key, Note: note, Request: request, Files: files,
	})
}

func TestSecondHandoffStillSeesEarlierAdditions(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch := liveOrchestrator(t, s)
	files := []attachments.File{{Name: "a.png", Ext: ".png", Body: onePNG}}
	if _, err := handoffWith(ctx, s, orch.ID, "h1", "", "first addition", files); err != nil {
		t.Fatal(err)
	}
	if _, err := handoffWith(ctx, s, orch.ID, "h2", "", "second addition", nil); err != nil {
		t.Fatal(err)
	}
	got, _ := s.AgentByID(ctx, orch.ID)
	it, _ := s.Items.Get(ctx, "EPIC-1")
	for _, text := range []string{got.Brief, it.Brief} {
		if strings.Count(text, "## Additional request") != 2 || !strings.Contains(text, "first addition") || !strings.Contains(text, "second addition") {
			t.Fatalf("text = %q", text)
		}
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.SyncRecovery(ctx, ses.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(view.Assignment.Brief, "first addition") || !strings.Contains(view.Assignment.Brief, "second addition") {
		t.Fatalf("sync brief = %q", view.Assignment.Brief)
	}
}

func TestConcurrentHandoffsLeaveOneBatch(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch := liveOrchestrator(t, s)
	files := []attachments.File{{Name: "a.png", Ext: ".png", Body: onePNG}}
	var wg sync.WaitGroup
	ops := make([]Operation, 2)
	errs := make([]error, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			ops[i], errs[i] = handoffWith(ctx, s, orch.ID, "same", "", "one addition", files)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("handoff %d: %v", i, err)
		}
	}
	if ops[0].ID == "" || ops[0].ID != ops[1].ID {
		t.Fatalf("operations = %s, %s", ops[0].ID, ops[1].ID)
	}
	if batches := restartBatches(s.Home, "EPIC-1"); len(batches) != 1 {
		t.Fatalf("batches = %v, want 1", batches)
	}
	it, _ := s.Items.Get(ctx, "EPIC-1")
	if strings.Count(it.Brief, "## Additional request") != 1 || strings.Count(it.Brief, "one addition") != 1 {
		t.Fatalf("description = %q", it.Brief)
	}
}

func TestCancelledHandoffKeepsTextAndFiles(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	orch := liveOrchestrator(t, s)
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, ses.ID, Running); err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: ses.TmuxName})
	tm.env[ses.TmuxName] = map[string]string{"SWARM_SESSION": ses.ID}
	op, err := handoffWith(ctx, s, orch.ID, "park", "", "Keep this.", []attachments.File{{Name: "a.png", Ext: ".png", Body: onePNG}})
	if err != nil {
		t.Fatal(err)
	}
	if op.Phase == PhaseSucceeded || isTerminalPhase(op.Phase) {
		t.Fatalf("phase = %s, want the handoff parked so cancel is meaningful", op.Phase)
	}
	cancelled, err := s.CancelOperation(ctx, op.ID)
	if err != nil || cancelled.Phase != PhaseCancelled {
		t.Fatalf("cancel = %s, %v", cancelled.Phase, err)
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

func TestHandoffOverCapIs400WithNothingSaved(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch := liveOrchestrator(t, s)
	full := strings.Repeat("x", items.MaxBrief)
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET brief = ? WHERE id = ?`, full, orch.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Items.Get(ctx, "EPIC-1")
	_, err := handoffWith(ctx, s, orch.ID, "cap", "", "this no longer fits", []attachments.File{{Name: "a.png", Ext: ".png", Body: onePNG}})
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeBadRequest || ie.Message != items.ErrBriefTooLong {
		t.Fatalf("err = %v, want 400 %q", err, items.ErrBriefTooLong)
	}
	if batches := restartBatches(s.Home, "EPIC-1"); len(batches) != 0 {
		t.Fatalf("batches = %v", batches)
	}
	got, _ := s.AgentByID(ctx, orch.ID)
	it, _ := s.Items.Get(ctx, "EPIC-1")
	if got.Brief != full || it.Brief != before.Brief {
		t.Fatal("brief or description changed")
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_operations WHERE agent_id = ?`, orch.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("operations = %d (%v), want none", n, err)
	}
}

func TestFullDescriptionHandoffSucceedsAndIsLogged(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch := liveOrchestrator(t, s)
	full := strings.Repeat("y", items.MaxBrief)
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET brief = ? WHERE key = 'EPIC-1'`, full); err != nil {
		t.Fatal(err)
	}
	var logs []string
	s.Log = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	const request = "still store this"
	op, err := handoffWith(ctx, s, orch.ID, "full", "", request, nil)
	if err != nil || op.Phase != PhaseSucceeded {
		t.Fatalf("handoff = %s, %v", op.Phase, err)
	}
	it, _ := s.Items.Get(ctx, "EPIC-1")
	if it.Brief != full {
		t.Fatalf("description changed to %q", it.Brief)
	}
	got, _ := s.AgentByID(ctx, orch.ID)
	if !strings.Contains(got.Brief, request) {
		t.Fatalf("stored brief = %q", got.Brief)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "unchanged") || !strings.Contains(joined, items.ErrBriefTooLong) {
		t.Fatalf("logs = %q", joined)
	}
	ses, _ := s.LatestSession(ctx, orch.ID)
	synced, err := s.Sync(ctx, ses.ID, nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range synced.Messages {
		if m.Kind == "assignment_update" && strings.Contains(string(m.Payload), request) {
			found = true
		}
	}
	if !found {
		t.Fatal("the note does not carry the request")
	}
}
