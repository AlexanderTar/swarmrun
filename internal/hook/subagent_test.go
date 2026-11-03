package hook

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/runtime"
)

// Claude Code runs post-turn work (memory extraction, forks) in a subagent
// whose hooks carry agent_id. They never move the main session's turn or busy
// state: a PreToolUse straight after Stop used to reopen the turn and leave a
// tool open for good, so the finished session never read idle.
func TestSubagentHooksNeverMoveTheMainTurn(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	ctx := context.Background()
	handle := func(ev, body string) []byte {
		t.Helper()
		out, err := h.Handle(ctx, runtime.Claude, ev, ses, []byte(body))
		if err != nil {
			t.Fatalf("%s: %v", ev, err)
		}
		return out
	}
	handle("UserPromptSubmit", `{"session_id":"p1","prompt":"hi"}`)
	handle("Stop", `{}`)
	if got := h.RT.HookTurn(ses); got.Open || got.CloseEvent != "Stop" {
		t.Fatalf("after Stop: %+v, want a closed turn", got)
	}

	handle("PreToolUse", `{"session_id":"p1","agent_id":"agent-abc","tool_name":"Read","tool_input":{"file_path":"/tmp/x"}}`)
	if got := h.RT.HookTurn(ses); got.Open || got.CloseEvent != "Stop" {
		t.Fatalf("after a subagent PreToolUse: %+v, want the turn still closed by Stop", got)
	}
	if _, busy := h.RT.BusyNow(ses); busy {
		t.Fatal("a subagent PreToolUse left the main session busy")
	}

	// the same tool without agent_id is the main agent and does open the turn
	handle("PreToolUse", `{"session_id":"p1","tool_name":"Read","tool_input":{"file_path":"/tmp/x"}}`)
	if got := h.RT.HookTurn(ses); !got.Open {
		t.Fatalf("main-agent PreToolUse: %+v, want an open turn", got)
	}
}

// A subagent's tool still goes through decide(): policy blocks apply to it.
func TestSubagentPreToolUseIsStillBlockedByPolicy(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	out, err := h.Handle(context.Background(), runtime.Claude, "PreToolUse", ses,
		[]byte(`{"session_id":"p1","agent_id":"agent-abc","tool_name":"Workflow","tool_input":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]map[string]string
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	if m["hookSpecificOutput"]["permissionDecision"] != "deny" {
		t.Fatalf("subagent Workflow call: want deny, got %s", out)
	}
}

// Claude Code fires PostToolUseFailure, not PostToolUse, for a tool that failed
// or was denied. It closes the open call like PostToolUse does; without it the
// call stays open and the session reads busy until the turn ends.
func TestPostToolUseFailureClosesTheOpenToolCall(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	ctx := context.Background()
	for _, step := range []struct{ ev, body string }{
		{"UserPromptSubmit", `{"session_id":"p1","prompt":"hi"}`},
		{"PreToolUse", `{"session_id":"p1","tool_name":"Read","tool_input":{"file_path":"/tmp/x"}}`},
	} {
		if _, err := h.Handle(ctx, runtime.Claude, step.ev, ses, []byte(step.body)); err != nil {
			t.Fatalf("%s: %v", step.ev, err)
		}
	}
	if _, busy := h.RT.BusyNow(ses); !busy {
		t.Fatal("test setup: an open tool call should read busy")
	}
	if _, err := h.Handle(ctx, runtime.Claude, "PostToolUseFailure", ses,
		[]byte(`{"session_id":"p1","tool_name":"Read","tool_input":{"file_path":"/tmp/x"},"error":"not found"}`)); err != nil {
		t.Fatal(err)
	}
	if _, busy := h.RT.BusyNow(ses); busy {
		t.Fatal("PostToolUseFailure left the tool call open")
	}
}
