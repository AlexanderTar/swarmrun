package settings

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/catalog"
	"github.com/AlexanderTar/swarmrun/internal/events"
	"github.com/AlexanderTar/swarmrun/internal/kinds"
)

func raw(fields map[string]string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, v := range fields {
		out[k] = json.RawMessage(v)
	}
	return out
}

func TestPatchChangesOnlyTheGivenSettings(t *testing.T) {
	s := newStore(t, kinds.Claude, kinds.Codex)
	before, _ := s.Get(ctx)
	before.ScanExcludes = []string{"~/Downloads"}
	if _, err := s.Put(ctx, before); err != nil {
		t.Fatal(err)
	}
	prev, saved, err := s.Patch(ctx, raw(map[string]string{"max_concurrent_agents": "7", "usage_poll_sec": "900"}))
	if err != nil {
		t.Fatal(err)
	}
	if prev.MaxConcurrentAgents != before.MaxConcurrentAgents {
		t.Errorf("prev = %d, want the replaced %d", prev.MaxConcurrentAgents, before.MaxConcurrentAgents)
	}
	again, _ := s.Get(ctx)
	if saved.MaxConcurrentAgents != 7 || again.MaxConcurrentAgents != 7 || again.UsagePollSec != 900 {
		t.Fatalf("patched = %+v / stored %+v", saved, again)
	}
	if !slices.Equal(again.ScanExcludes, []string{"~/Downloads"}) || again.Roles[kinds.RoleCoder] != before.Roles[kinds.RoleCoder] {
		t.Fatalf("an unpatched setting changed: %+v", again)
	}
}

func TestPatchRefusesUnknownMistypedAndInvalidValues(t *testing.T) {
	s := newStore(t, kinds.Claude)
	for _, tc := range []struct {
		fields map[string]string
		msg    string
	}{
		{map[string]string{"no_such_setting": "1"}, "no_such_setting isn't a setting that can be changed here."},
		{map[string]string{"low_token_mode": "true"}, "low_token_mode isn't a setting that can be changed here."},
		{map[string]string{"max_concurrent_agents": `"lots"`}, "max_concurrent_agents must be a JSON int."},
		{map[string]string{"max_concurrent_agents": "0"}, ""}, // Put's own validation
	} {
		before, _ := s.Get(ctx)
		_, _, err := s.Patch(ctx, raw(tc.fields))
		var ve *ValidationError
		if !errors.As(err, &ve) || (tc.msg != "" && ve.Message != tc.msg) {
			t.Errorf("%v: err = %v, want %q", tc.fields, err, tc.msg)
		}
		if after, _ := s.Get(ctx); after.MaxConcurrentAgents != before.MaxConcurrentAgents || after.LowTokenMode != before.LowTokenMode {
			t.Errorf("%v: a refused patch changed the settings", tc.fields)
		}
	}
}

// Two clients patching different settings at once both land: neither write
// starts from a copy read before the other's.
func TestConcurrentPatchesOfDifferentSettingsBothLand(t *testing.T) {
	for range 20 {
		s := newStore(t, kinds.Claude)
		var wg sync.WaitGroup
		for _, f := range []map[string]string{{"usage_poll_sec": "900"}, {"pause_deadline_sec": "120"}} {
			wg.Go(func() {
				if _, _, err := s.Patch(ctx, raw(f)); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if got, _ := s.Get(ctx); got.UsagePollSec != 900 || got.PauseDeadlineSec != 120 {
			t.Fatalf("usage_poll_sec = %d, pause_deadline_sec = %d: one patch overwrote the other", got.UsagePollSec, got.PauseDeadlineSec)
		}
	}
}

// The other writers don't take Patch's lock. A PATCH writes only what it
// changes, so instructions saved between its read and its write survive.
func TestPatchKeepsInstructionsSavedWhileItRuns(t *testing.T) {
	s := newStore(t, kinds.Claude)
	models := s.ModelsFor
	var once sync.Once
	s.ModelsFor = func(c context.Context, k kinds.AgentKind) ([]catalog.CatalogModel, string, error) {
		// validate runs after put's read and before its write: the agent's
		// swarm_instructions call lands right then.
		once.Do(func() {
			if err := s.SetInstructions(c, "from the agent"); err != nil {
				t.Error(err)
			}
		})
		return models(c, k)
	}
	_, saved, err := s.Patch(ctx, raw(map[string]string{"usage_poll_sec": "900"}))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx)
	if got.Instructions != "from the agent" || got.UsagePollSec != 900 {
		t.Fatalf("instructions = %q, usage_poll_sec = %d: the patch undid the concurrent change", got.Instructions, got.UsagePollSec)
	}
	// The response and the settings.changed event say what is stored, not the
	// patch's stale copy (the menu bar adopts the response).
	if saved.Instructions != "from the agent" || saved.UsagePollSec != 900 {
		t.Errorf("returned instructions = %q, usage_poll_sec = %d", saved.Instructions, saved.UsagePollSec)
	}
	evs, _ := s.Events.After(ctx, 0, 100)
	last := evs[len(evs)-1]
	var published Settings
	if err := json.Unmarshal(last.Payload, &published); err != nil || last.Type != events.SettingsChanged ||
		published.Instructions != "from the agent" || published.UsagePollSec != 900 {
		t.Errorf("last event = %s %s (%v)", last.Type, last.Payload, err)
	}
}

func TestPatchChangesOnlyTheNamedEntriesOfAMapSetting(t *testing.T) {
	s := newStore(t, kinds.Claude, kinds.Codex)
	cur, _ := s.Get(ctx)
	reviewer := RoleDefault{kinds.Codex, "gpt-6-astra", "high", ""}
	cur.Roles[kinds.RoleReviewer] = reviewer
	cur.Notifications["info"] = NotifyPref{}
	if _, err := s.Put(ctx, cur); err != nil {
		t.Fatal(err)
	}
	_, saved, err := s.Patch(ctx, raw(map[string]string{
		"roles":         `{"coder":{"agent":"codex","model":"gpt-6-astra","effort":"high"}}`,
		"notifications": `{"action":{"center":true,"sound":false}}`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if want := (RoleDefault{kinds.Codex, "gpt-6-astra", "high", ""}); saved.Roles[kinds.RoleCoder] != want {
		t.Errorf("coder = %+v, want %+v", saved.Roles[kinds.RoleCoder], want)
	}
	if saved.Roles[kinds.RoleReviewer] != reviewer {
		t.Errorf("reviewer = %+v, want the stored %+v", saved.Roles[kinds.RoleReviewer], reviewer)
	}
	if want := (NotifyPref{Center: true}); saved.Notifications["action"] != want {
		t.Errorf("action = %+v, want %+v", saved.Notifications["action"], want)
	}
	if saved.Notifications["info"] != (NotifyPref{}) {
		t.Errorf("info = %+v, want the stored off", saved.Notifications["info"])
	}
}

func TestPatchNullResetsAMapEntryOrTheWholeSetting(t *testing.T) {
	s := newStore(t, kinds.Claude, kinds.Codex)
	def := Defaults(nil)
	custom := RoleDefault{kinds.Codex, "gpt-6-astra", "high", ""}
	cur, _ := s.Get(ctx)
	cur.Roles[kinds.RoleCoder], cur.Roles[kinds.RoleReviewer] = custom, custom
	if _, err := s.Put(ctx, cur); err != nil {
		t.Fatal(err)
	}
	_, saved, err := s.Patch(ctx, raw(map[string]string{"roles": `{"reviewer":null}`}))
	if err != nil {
		t.Fatal(err)
	}
	if saved.Roles[kinds.RoleReviewer] != def.Roles[kinds.RoleReviewer] || saved.Roles[kinds.RoleCoder] != custom {
		t.Fatalf("reviewer = %+v, coder = %+v, want the default and the stored", saved.Roles[kinds.RoleReviewer], saved.Roles[kinds.RoleCoder])
	}
	_, saved, err = s.Patch(ctx, raw(map[string]string{"roles": `null`}))
	if err != nil {
		t.Fatal(err)
	}
	if saved.Roles[kinds.RoleCoder] != def.Roles[kinds.RoleCoder] {
		t.Fatalf("coder = %+v, want the default after a null setting", saved.Roles[kinds.RoleCoder])
	}
}

func TestPatchRefusesAMapSettingThatIsNotAnObject(t *testing.T) {
	s := newStore(t, kinds.Claude, kinds.Codex)
	before, _ := s.Get(ctx)
	if _, _, err := s.Patch(ctx, raw(map[string]string{"notifications": `5`})); err == nil {
		t.Fatal("a number replaced the notifications")
	}
	if again, _ := s.Get(ctx); again.Notifications["info"] != before.Notifications["info"] {
		t.Fatalf("a refused patch changed the notifications: %+v", again.Notifications)
	}
}

// A patch names fields, not whole entries: what an entry leaves out keeps its
// stored value, or `{"info":{"sound":false}}` would also turn the banner off and
// `{"advisor":{"model":"none"}}` would store a blank agent the menu bar can't read.
func TestPatchKeepsTheFieldsAnEntryLeavesOut(t *testing.T) {
	s := newStore(t, kinds.Claude, kinds.Codex)
	cur, _ := s.Get(ctx)
	cur.Roles[kinds.RoleCoder] = RoleDefault{kinds.Codex, "gpt-6-astra", "high", ""}
	cur.Notifications["info"] = NotifyPref{Center: true, Sound: true}
	if _, err := s.Put(ctx, cur); err != nil {
		t.Fatal(err)
	}
	advisor := cur.Roles[kinds.RoleAdvisor].Agent
	if advisor == "" {
		t.Fatal("the default advisor has no agent, so this test proves nothing")
	}
	_, saved, err := s.Patch(ctx, raw(map[string]string{
		"notifications": `{"info":{"sound":false}}`,
		"roles":         `{"coder":{"effort":null},"advisor":{"model":"` + NoAdvisor + `"}}`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if want := (NotifyPref{Center: true}); saved.Notifications["info"] != want {
		t.Errorf("info = %+v, want %+v", saved.Notifications["info"], want)
	}
	if want := (RoleDefault{Agent: kinds.Codex, Model: "gpt-6-astra"}); saved.Roles[kinds.RoleCoder] != want {
		t.Errorf("coder = %+v, want %+v (null clears one field)", saved.Roles[kinds.RoleCoder], want)
	}
	if got := saved.Roles[kinds.RoleAdvisor]; got.Model != NoAdvisor || got.Agent != advisor {
		t.Errorf("advisor = %+v, want model %q with the stored agent %q", got, NoAdvisor, advisor)
	}
}
