package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/items"
)

func agentBrief(t *testing.T, s *runtimeEnv, name string) string {
	t.Helper()
	var brief string
	if err := s.DB.QueryRowContext(bg, `SELECT brief FROM agents WHERE name = ?`, name).Scan(&brief); err != nil {
		t.Fatal(err)
	}
	return brief
}

func assignmentUpdates(t *testing.T, s *runtimeEnv, name string) []string {
	t.Helper()
	rows, err := s.DB.QueryContext(bg, `SELECT payload_json FROM messages
		WHERE kind = 'assignment_update' AND to_agent_id = (SELECT id FROM agents WHERE name = ?)`, name)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func handoffOpID(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.OperationID == "" {
		t.Fatalf("operation id from %s: %v", body, err)
	}
	return out.OperationID
}

func TestSwitchHandoffCarriesRequestAndFile(t *testing.T) {
	s, seed := newRuntimeServer(t)
	const request = "Please read the notes."
	rec := s.post(t, "/api/agents/root-orchestrator/handoff", fmt.Sprintf(
		`{"request_id":"sw-add","agent":"fake","model":"fake-1","note":"ignore-this-note","request":%q,"attachments":[{"name":"a.png","data":%q}]}`,
		request, onePNGBase64))
	if rec.Code != 202 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	path := batchFile(t, s.RT.Home, seed.RootKey, "01-a.png")
	brief := agentBrief(t, s, "root-orchestrator")
	desc := itemBrief(t, s, seed.RootKey)
	for _, text := range []string{brief, desc} {
		if !strings.Contains(text, request) || !strings.Contains(text, "## Additional request") || !strings.Contains(text, path) {
			t.Fatalf("text = %q, want the request and %s", text, path)
		}
	}
	if n := batchCount(t, s.RT.Home, seed.RootKey); n != 1 {
		t.Fatalf("batches = %d, want 1", n)
	}
	notes := 0
	for _, p := range assignmentUpdates(t, s, "root-orchestrator") {
		if strings.Contains(p, request) {
			notes++
			if strings.Contains(p, "ignore-this-note") {
				t.Fatalf("switch handoff kept the note: %s", p)
			}
		}
	}
	if notes != 1 {
		t.Fatalf("notes carrying the request = %d, want 1", notes)
	}
	a, err := s.RT.Agent(bg, "root-orchestrator")
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.RT.LatestSession(bg, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.RT.SyncRecovery(bg, ses.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(view.Assignment.Brief, request) {
		t.Fatalf("sync brief = %q", view.Assignment.Brief)
	}
}

func TestSameAgentHandoffNoteComesBeforeTheRequest(t *testing.T) {
	s, seed := newRuntimeServer(t)
	const request = "Please read the notes."
	const note = "check the logs"
	rec := s.post(t, "/api/agents/root-orchestrator/handoff", fmt.Sprintf(
		`{"request_id":"same-add","note":%q,"request":%q,"attachments":[{"name":"a.png","data":%q}]}`,
		note, request, onePNGBase64))
	if rec.Code != 202 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	path := batchFile(t, s.RT.Home, seed.RootKey, "01-a.png")
	if !strings.Contains(agentBrief(t, s, "root-orchestrator"), request) || !strings.Contains(itemBrief(t, s, seed.RootKey), path) {
		t.Fatal("stored brief or description is missing the request or the file")
	}
	var payload string
	for _, p := range assignmentUpdates(t, s, "root-orchestrator") {
		if strings.Contains(p, request) {
			payload = p
		}
	}
	if payload == "" {
		t.Fatal("no note carries the request")
	}
	if strings.Index(payload, note) < 0 || strings.Index(payload, note) > strings.Index(payload, request) {
		t.Fatalf("payload = %s, want %q before the request", payload, note)
	}
}

func TestHandoffAcceptsABodyOverOneMiB(t *testing.T) {
	s, seed := newRuntimeServer(t)
	big := strings.Repeat("a", 1500*1024)
	rec := s.post(t, "/api/agents/root-orchestrator/handoff", fmt.Sprintf(
		`{"request_id":"big","request":"a long file","attachments":[{"name":"big.txt","data":%q}]}`, big))
	if rec.Code != 202 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if batchCount(t, s.RT.Home, seed.RootKey) != 1 {
		t.Fatal("the large body was not saved")
	}
}

func TestHandoffFilesWithoutRequestIDAre400(t *testing.T) {
	s, seed := newRuntimeServer(t)
	beforeBrief := agentBrief(t, s, "root-orchestrator")
	beforeDesc := itemBrief(t, s, seed.RootKey)
	rec := s.post(t, "/api/agents/root-orchestrator/handoff", fmt.Sprintf(
		`{"attachments":[{"name":"a.png","data":%q}]}`, onePNGBase64))
	wantErr(t, rec.Code, rec.Body.Bytes(), 400, "bad_request", "Attachments need a request_id.")
	if batchCount(t, s.RT.Home, seed.RootKey) != 0 {
		t.Fatal("files were saved")
	}
	if agentBrief(t, s, "root-orchestrator") != beforeBrief || itemBrief(t, s, seed.RootKey) != beforeDesc {
		t.Fatal("brief or description changed")
	}
}

func TestHandoffBadFileIs400AndChangesNothing(t *testing.T) {
	s, seed := newRuntimeServer(t)
	beforeBrief := agentBrief(t, s, "root-orchestrator")
	beforeDesc := itemBrief(t, s, seed.RootKey)
	rec := s.post(t, "/api/agents/root-orchestrator/handoff",
		`{"request_id":"bad","request":"nope","attachments":[{"name":"x.pdf","data":"%%%"}]}`)
	if rec.Code != 400 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if batchCount(t, s.RT.Home, seed.RootKey) != 0 {
		t.Fatal("a bad file left a batch")
	}
	if agentBrief(t, s, "root-orchestrator") != beforeBrief || itemBrief(t, s, seed.RootKey) != beforeDesc {
		t.Fatal("brief or description changed")
	}
	var n int
	if err := s.DB.QueryRowContext(bg, `SELECT COUNT(*) FROM agent_operations`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("operations = %d (%v), want none", n, err)
	}
}

func TestWorkerHandoffWithRequestIs400(t *testing.T) {
	s, seed := newRuntimeServer(t)
	before := itemBrief(t, s, seed.RootKey)
	rec := s.post(t, "/api/agents/task-worker/handoff", `{"request_id":"w1","request":"do this"}`)
	wantErr(t, rec.Code, rec.Body.Bytes(), 400, "bad_request", "Only an orchestrator can take a request or files.")
	if batchCount(t, s.RT.Home, seed.RootKey) != 0 || itemBrief(t, s, seed.RootKey) != before {
		t.Fatal("a worker handoff saved something")
	}
}

func TestRefusedHandoffWithFilesLeavesNoBatch(t *testing.T) {
	s, seed := newRuntimeServer(t)
	beforeBrief := agentBrief(t, s, "root-orchestrator")
	beforeDesc := itemBrief(t, s, seed.RootKey)
	rec := s.post(t, "/api/agents/root-orchestrator/handoff", fmt.Sprintf(
		`{"request_id":"bad-model","agent":"fake","model":"nope","request":"switch","attachments":[{"name":"a.png","data":%q}]}`,
		onePNGBase64))
	wantErr(t, rec.Code, rec.Body.Bytes(), 422, "preflight_failed", "")
	if batchCount(t, s.RT.Home, seed.RootKey) != 0 {
		t.Fatal("a refused handoff left a batch")
	}
	if agentBrief(t, s, "root-orchestrator") != beforeBrief || itemBrief(t, s, seed.RootKey) != beforeDesc {
		t.Fatal("brief or description changed")
	}
}

func TestHandoffReplayOnAcceptedRootWritesNothing(t *testing.T) {
	s, seed := newRuntimeServer(t)
	body := fmt.Sprintf(
		`{"request_id":"replay","request":"Please read the notes.","attachments":[{"name":"a.png","data":%q}]}`,
		onePNGBase64)
	first := s.post(t, "/api/agents/root-orchestrator/handoff", body)
	if first.Code != 202 {
		t.Fatalf("first status = %d: %s", first.Code, first.Body)
	}
	id := handoffOpID(t, first.Body.Bytes())
	if _, err := s.DB.ExecContext(bg, `UPDATE items SET status = 'done' WHERE key = ?`, seed.RootKey); err != nil {
		t.Fatal(err)
	}
	second := s.post(t, "/api/agents/root-orchestrator/handoff", body)
	if second.Code != 202 {
		t.Fatalf("replay status = %d: %s", second.Code, second.Body)
	}
	if got := handoffOpID(t, second.Body.Bytes()); got != id {
		t.Fatalf("replay operation = %s, want %s", got, id)
	}
	if n := batchCount(t, s.RT.Home, seed.RootKey); n != 1 {
		t.Fatalf("batches = %d, want 1", n)
	}
	if strings.Count(itemBrief(t, s, seed.RootKey), "## Additional request") != 1 {
		t.Fatalf("description = %q", itemBrief(t, s, seed.RootKey))
	}
}

func TestNoEntriesHandoffNearTheCapStillWorks(t *testing.T) {
	s, _ := newRuntimeServer(t)
	full := strings.Repeat("x", items.MaxBrief)
	if _, err := s.DB.ExecContext(bg, `UPDATE agents SET brief = ? WHERE name = 'root-orchestrator'`, full); err != nil {
		t.Fatal(err)
	}
	rec := s.post(t, "/api/agents/root-orchestrator/handoff", `{"request_id":"near"}`)
	if rec.Code != 202 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if agentBrief(t, s, "root-orchestrator") != full {
		t.Fatal("a no-entries handoff changed a full stored brief")
	}
}

func TestNoEntriesHandoffLeavesBriefAndDescriptionUnchanged(t *testing.T) {
	s, seed := newRuntimeServer(t)
	beforeBrief := agentBrief(t, s, "root-orchestrator")
	beforeDesc := itemBrief(t, s, seed.RootKey)
	rec := s.post(t, "/api/agents/root-orchestrator/handoff", `{"request_id":"plain","note":"check the logs"}`)
	if rec.Code != 202 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "attachments_failed") {
		t.Fatalf("response grew a flag: %s", rec.Body)
	}
	if agentBrief(t, s, "root-orchestrator") != beforeBrief || itemBrief(t, s, seed.RootKey) != beforeDesc {
		t.Fatal("brief or description changed")
	}
	if batchCount(t, s.RT.Home, seed.RootKey) != 0 {
		t.Fatal("a no-entries handoff saved files")
	}
	for _, p := range assignmentUpdates(t, s, "root-orchestrator") {
		if strings.Contains(p, "## Additional request") {
			t.Fatalf("note gained an addition: %s", p)
		}
	}
}
