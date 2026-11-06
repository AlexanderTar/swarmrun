package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/attachments"
)

func userAddedPrefix(t *testing.T, note string) string {
	t.Helper()
	lines := strings.SplitN(strings.TrimSpace(note), "\n", 2)
	if len(lines) == 0 {
		t.Fatalf("note is empty")
	}
	return strings.TrimSpace(lines[0])
}

func TestHandoffNoteOpensWithUserAddedLine(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch := liveOrchestrator(t, s)
	files := []attachments.File{{Name: "a.png", Ext: ".png", Body: onePNG}}
	const request = "Please read the notes for unit one."
	op, err := handoffWith(ctx, s, orch.ID, "ua-handoff", "", request, files)
	if err != nil {
		t.Fatal(err)
	}
	first := userAddedPrefix(t, op.Note)
	if !strings.Contains(strings.ToLower(first), "user added") {
		t.Fatalf("handoff note first line = %q, want a line saying the user added this", first)
	}
	if !strings.Contains(op.Note, request) {
		t.Fatalf("handoff note = %q, want the full request text", op.Note)
	}
	// Attachments list follows the text.
	if !strings.Contains(op.Note, "## Attachments") {
		t.Fatalf("handoff note = %q, want the attachments list", op.Note)
	}
	got, _ := s.AgentByID(ctx, orch.ID)
	if strings.Contains(strings.ToLower(got.Brief), "user added") {
		t.Fatalf("stored brief = %q, must not carry the user-added line", got.Brief)
	}
	if !strings.Contains(got.Brief, request) {
		t.Fatalf("stored brief = %q, want the request text", got.Brief)
	}
}

func TestHandoffNoteKeepsExistingNoteFirst(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch := liveOrchestrator(t, s)
	const note = "check the logs"
	const request = "Please read the notes for unit one."
	op, err := handoffWith(ctx, s, orch.ID, "ua-both", note, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(op.Note), note) {
		t.Fatalf("handoff note = %q, want the existing note first", op.Note)
	}
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(op.Note), note))
	if !strings.Contains(strings.ToLower(rest), "user added") {
		t.Fatalf("handoff note = %q, want the user-added line after the note", op.Note)
	}
	if !strings.Contains(op.Note, request) {
		t.Fatalf("handoff note = %q, want the full request text", op.Note)
	}
}

func TestRestartNoteOpensWithUserAddedLine(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch := stopOrchestrator(t, s, "EPIC-1")
	const request = "Please read the notes for unit one."
	_, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1",
		RequestID: "ua-restart", Request: request, Attachments: []attachments.File{{Name: "a.png", Ext: ".png", Body: onePNG}}})
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.operationByKey(ctx, orch.ID, "ua-restart")
	if err != nil {
		t.Fatal(err)
	}
	first := userAddedPrefix(t, op.Note)
	if !strings.Contains(strings.ToLower(first), "user added") {
		t.Fatalf("restart note first line = %q, want a line saying the user added this", first)
	}
	if !strings.Contains(op.Note, request) {
		t.Fatalf("restart note = %q, want the full request text", op.Note)
	}
	got, _ := s.AgentByID(ctx, orch.ID)
	if strings.Contains(strings.ToLower(got.Brief), "user added") {
		t.Fatalf("stored brief = %q, must not carry the user-added line", got.Brief)
	}
}

func TestPlainNoteUnchangedWhenNoAddition(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch := liveOrchestrator(t, s)
	const note = "check the logs"
	op, err := s.RequestReplacement(ctx, orch.ID, ModeHandoff, "ua-plain", note)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(op.Note) != note {
		t.Fatalf("plain note = %q, want %q unchanged", op.Note, note)
	}
}
