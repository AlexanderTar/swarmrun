package httpapi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/runtime"
)

func TestHandoffSaveFailureReportsAttachmentsFailed(t *testing.T) {
	s, _ := newRuntimeServer(t)
	tm := s.RT.Tmux.(*testTmux)
	a, err := s.RT.Agent(bg, "root-orchestrator")
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.RT.LatestSession(bg, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	tm.panes = []runtime.Pane{{Session: ses.TmuxName}}
	tm.env = map[string]map[string]string{ses.TmuxName: {"SWARM_SESSION": ses.ID}}
	if err := os.WriteFile(filepath.Join(s.RT.Home, "attachments"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(
		`{"request_id":"unsaved","request":"Please read the notes.","attachments":[{"name":"a.png","data":%q}]}`,
		onePNGBase64)
	rec := s.post(t, "/api/agents/root-orchestrator/handoff", body)
	if rec.Code != 202 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		OperationID       string `json:"operation_id"`
		Phase             string `json:"phase"`
		AttachmentsFailed bool   `json:"attachments_failed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Phase == "succeeded" || out.Phase == "cancelled" || out.Phase == "blocked" {
		t.Fatalf("phase = %s, want the handoff still in progress so GET replacement can see it", out.Phase)
	}
	if !out.AttachmentsFailed {
		t.Fatalf("response = %s, want attachments_failed", rec.Body)
	}
	var note string
	if err := s.DB.QueryRowContext(bg, `SELECT note FROM agent_operations WHERE id = ?`, out.OperationID).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "Ask the user to share them another way.") {
		t.Fatalf("note = %q", note)
	}
	got := s.get(t, "/api/agents/root-orchestrator/replacement")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"attachments_failed":true`) {
		t.Fatalf("GET replacement = %d %s", got.Code, got.Body)
	}
	again := s.post(t, "/api/agents/root-orchestrator/handoff", body)
	if again.Code != 202 {
		t.Fatalf("replay status = %d: %s", again.Code, again.Body)
	}
	var replay struct {
		OperationID       string `json:"operation_id"`
		AttachmentsFailed bool   `json:"attachments_failed"`
	}
	if err := json.Unmarshal(again.Body.Bytes(), &replay); err != nil {
		t.Fatal(err)
	}
	if replay.OperationID != out.OperationID || !replay.AttachmentsFailed {
		t.Fatalf("replay = %s", again.Body)
	}
}
