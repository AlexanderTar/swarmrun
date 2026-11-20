package items_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/events"
	"github.com/AlexanderTar/swarmrun/internal/items"
	"github.com/AlexanderTar/swarmrun/internal/workflow"
)

// newCrossRootTask creates a task under the foreign root's story as the orchestrator of another root,
// the way the MCP create path does, and returns it.
func newCrossRootTask(t *testing.T, s *items.Store, story items.Item, orch items.Actor, title string) items.Item {
	t.Helper()
	task, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key, Title: title,
		Workflow: &workflow.Spec{Template: "tdd-reviewed"}}, orch)
	if err != nil {
		t.Fatalf("cross-root create %q: %v", title, err)
	}
	return task
}

func addDepForNew(s *items.Store, key, blockedBy string, by items.Actor) error {
	return s.DB.Tx(ctx, func(tx *sql.Tx) error { return s.AddDepForNewTx(ctx, tx, key, blockedBy, by) })
}

func TestAddDepForNewTxAddsADepInTheNewItemsRootFromAnotherRoot(t *testing.T) {
	s, foreign, story, orch := crossRootFixture(t, items.Ready)
	first := newCrossRootTask(t, s, story, orch, "First")
	second := newCrossRootTask(t, s, story, orch, "Second")
	// A plain AddDepTx refuses this actor: the new item's root isn't the orchestrator's own.
	if err := s.DB.Tx(ctx, func(tx *sql.Tx) error { return s.AddDepTx(ctx, tx, second.Key, first.Key, orch) }); err == nil {
		t.Fatal("AddDepTx from another root accepted; the new-item path would not be needed")
	}
	if err := addDepForNew(s, second.Key, first.Key, orch); err != nil {
		t.Fatalf("AddDepForNewTx: %v", err)
	}
	blockedBy, _, err := s.Deps(ctx, second.Key)
	if err != nil {
		t.Fatal(err)
	}
	if len(blockedBy) != 1 || blockedBy[0].Key != first.Key {
		t.Fatalf("Deps(%s) blockedBy = %v, want [%s]", second.Key, blockedBy, first.Key)
	}
	if _, blocks, err := s.Deps(ctx, first.Key); err != nil || len(blocks) != 1 || blocks[0].Key != second.Key {
		t.Fatalf("Deps(%s) blocks = %v, %v, want [%s]", first.Key, blocks, err, second.Key)
	}
	if got := mustGet(t, s, foreign.Key).Status; got != items.Ready {
		t.Fatalf("foreign root status = %s, want it untouched (ready)", got)
	}
}

func TestAddDepForNewTxRefusesADepInAnotherRoot(t *testing.T) {
	s, _, story, orch := crossRootFixture(t, items.Ready)
	task := newCrossRootTask(t, s, story, orch, "New")
	other := mk(t, s, items.Epic, "", "Unrelated root")
	err := addDepForNew(s, task.Key, other.Key, orch)
	if code(err) != items.CodeBadRequest || !strings.Contains(err.Error(), other.Key+" is in a different root") ||
		!strings.Contains(err.Error(), "same root as the new item") {
		t.Fatalf("err = %v (%s), want bad_request naming %s as in a different root", err, code(err), other.Key)
	}
	if blockedBy, _, err := s.Deps(ctx, task.Key); err != nil || len(blockedBy) != 0 {
		t.Fatalf("Deps = %v, %v; the refused dependency must not be stored", blockedBy, err)
	}
}

func TestAddDepForNewTxKeepsTheCycleAndHierarchyChecks(t *testing.T) {
	s, _, story, orch := crossRootFixture(t, items.Ready)
	first := newCrossRootTask(t, s, story, orch, "First")
	second := newCrossRootTask(t, s, story, orch, "Second")
	if err := addDepForNew(s, second.Key, first.Key, orch); err != nil {
		t.Fatal(err)
	}
	if err := addDepForNew(s, first.Key, second.Key, orch); code(err) != items.CodeConflict || !strings.Contains(err.Error(), items.CycleMessage) {
		t.Fatalf("cycle err = %v (%s), want conflict %q", err, code(err), items.CycleMessage)
	}
	if err := addDepForNew(s, first.Key, first.Key, orch); code(err) != items.CodeConflict {
		t.Fatalf("self dep err = %v (%s), want conflict", err, code(err))
	}
	if err := addDepForNew(s, first.Key, story.Key, orch); code(err) != items.CodeConflict || !strings.Contains(err.Error(), items.HierarchyDepMessage) {
		t.Fatalf("ancestor dep err = %v (%s), want conflict %q", err, code(err), items.HierarchyDepMessage)
	}
}

func TestAddDepForNewTxInTheOrchestratorsOwnRootUsesTheScopeCheck(t *testing.T) {
	s := newStore(t)
	mine := mk(t, s, items.Chore, "", "Mine")
	a := mk(t, s, items.Task, mine.Key, "A")
	b := mk(t, s, items.Task, mine.Key, "B")
	orch := items.Orchestrator("agt_o", mine.ID)
	if err := addDepForNew(s, b.Key, a.Key, orch); err != nil {
		t.Fatalf("own-root dep: %v", err)
	}
	// A dep from the orchestrator's own new item on another root's item is the ordinary AddDep case:
	// only the item being changed is scope-checked.
	foreign := mk(t, s, items.Epic, "", "Theirs")
	if err := addDepForNew(s, b.Key, foreign.Key, orch); err != nil {
		t.Fatalf("own new item blocked by another root's item: %v", err)
	}
	// A "new" item in another root with a dependency outside that root is refused with the new-item message.
	err := addDepForNew(s, foreign.Key, a.Key, orch)
	if code(err) != items.CodeBadRequest || !strings.Contains(err.Error(), a.Key+" is in a different root") {
		t.Fatalf("err = %v (%s), want bad_request %s is in a different root", err, code(err), a.Key)
	}
	if err := addDepForNew(s, b.Key, "NOPE-1", orch); code(err) != items.CodeNotFound {
		t.Fatalf("unknown dep err = %v (%s), want not_found", err, code(err))
	}
}

func reopen(s *items.Store, key string) error {
	return s.DB.Tx(ctx, func(tx *sql.Tx) error { return s.ReopenDoneRootTx(ctx, tx, key) })
}

func changedEvents(t *testing.T, s *items.Store, key string) int {
	t.Helper()
	evs, err := s.Events.After(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Type == events.ItemChanged && strings.Contains(string(ev.Payload), `"key":"`+key+`"`) {
			n++
		}
	}
	return n
}

func TestReopenDoneRootTxReopensADoneRootForEveryAcceptType(t *testing.T) {
	for _, typ := range []items.Type{items.Epic, items.Bug, items.Chore} {
		s := newStore(t)
		root := mk(t, s, typ, "", "Finished")
		setStatus(t, s, root, items.Done)
		before := mustGet(t, s, root.Key)
		events0 := changedEvents(t, s, root.Key)
		if err := reopen(s, root.Key); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		after := mustGet(t, s, root.Key)
		if after.Status != items.InProgress || after.Revision != before.Revision+1 {
			t.Fatalf("%s: status %s revision %d, want in_progress revision %d", typ, after.Status, after.Revision, before.Revision+1)
		}
		if got := changedEvents(t, s, root.Key); got != events0+1 {
			t.Fatalf("%s: item.changed events = %d, want %d", typ, got, events0+1)
		}
	}
}

func TestReopenDoneRootTxLeavesEverythingElseAlone(t *testing.T) {
	s, bug, chore := resolveFixture(t)
	if err := resolve(s, bug, chore.Key, user); err != nil { // resolved by another root: stays Done
		t.Fatal(err)
	}
	epic := mk(t, s, items.Epic, "", "Epic")
	story := mk(t, s, items.Story, epic.Key, "Story")
	task := mk(t, s, items.Task, story.Key, "Task")
	spike := mk(t, s, items.Spike, "", "Spike")
	for _, it := range []items.Item{story, task, spike} {
		setStatus(t, s, it, items.Done)
	}
	inProgress := mk(t, s, items.Chore, "", "Running")
	setStatus(t, s, inProgress, items.InProgress)
	cancelled := mk(t, s, items.Chore, "", "Dropped")
	setStatus(t, s, cancelled, items.Cancelled)

	cases := []struct {
		name string
		key  string
		want items.Status
	}{
		{"root resolved by another root", bug.Key, items.Done},
		{"done story", story.Key, items.Done},
		{"done task", task.Key, items.Done},
		{"done spike", spike.Key, items.Done},
		{"root not done", inProgress.Key, items.InProgress},
		{"cancelled root", cancelled.Key, items.Cancelled},
	}
	for _, c := range cases {
		before := mustGet(t, s, c.key)
		if err := reopen(s, c.key); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		after := mustGet(t, s, c.key)
		if after.Status != c.want || after.Revision != before.Revision {
			t.Errorf("%s: status %s revision %d, want %s revision %d (no-op)", c.name, after.Status, after.Revision, c.want, before.Revision)
		}
	}
	if err := reopen(s, "NOPE-1"); code(err) != items.CodeNotFound {
		t.Fatalf("unknown key err = %v (%s), want not_found", err, code(err))
	}
}
