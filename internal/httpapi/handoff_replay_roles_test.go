package httpapi

import (
	"testing"
)

func TestReplayedHandoffWithRequestKeepsChangedOverrides(t *testing.T) {
	s, _ := newRuntimeServer(t)
	const body = `{"request_id":"replay-roles","request":"Please read this.","roles":{"coder":{"agent":"fake","model":"fake-1","effort":""}}}`
	first := s.post(t, "/api/agents/root-orchestrator/handoff", body)
	if first.Code != 202 {
		t.Fatalf("first status = %d: %s", first.Code, first.Body)
	}
	firstID := handoffOpID(t, first.Body.Bytes())
	if got := handoffOverrides(t, s, "root-orchestrator"); got["coder"]["model"] != "fake-1" {
		t.Fatalf("overrides after first handoff = %v, want coder fake-1", got)
	}
	a, err := s.RT.Agent(bg, "root-orchestrator")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RT.RestoreRoleOverrides(bg, a.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got := handoffOverrides(t, s, "root-orchestrator"); len(got) != 0 {
		t.Fatalf("overrides after change = %v, want cleared", got)
	}
	second := s.post(t, "/api/agents/root-orchestrator/handoff", body)
	if second.Code != 202 {
		t.Fatalf("replay status = %d: %s", second.Code, second.Body)
	}
	if got := handoffOpID(t, second.Body.Bytes()); got != firstID {
		t.Fatalf("replay operation = %s, want %s", got, firstID)
	}
	if got := handoffOverrides(t, s, "root-orchestrator"); len(got) != 0 {
		t.Fatalf("overrides after replay = %v, want the changed (cleared) overrides kept", got)
	}
}
