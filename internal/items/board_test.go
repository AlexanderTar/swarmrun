package items_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/items"
)

// boardRoots returns the top-level keys of a board page, in page order.
func boardRoots(p items.BoardPage) []string {
	var out []string
	for _, it := range p.Items {
		if it.ParentID == "" {
			out = append(out, it.Key)
		}
	}
	return out
}

// seedBoardRoots creates n top-level epics with cycling priorities 0..2 and a
// story+task under every 5th root, so pages carry descendants too.
func seedBoardRoots(t *testing.T, s *items.Store, n int) {
	t.Helper()
	for i := range n {
		prio := i % 3
		e, err := s.Create(ctx, items.CreateInput{
			Type: items.Epic, Title: fmt.Sprintf("Root %02d", i), Priority: &prio,
		}, user)
		if err != nil {
			t.Fatal(err)
		}
		if i%5 == 0 {
			st := mk(t, s, items.Story, e.Key, fmt.Sprintf("Story %02d", i))
			mk(t, s, items.Task, st.Key, fmt.Sprintf("Task %02d", i))
		}
	}
}

// treeRootKeys returns the top-level keys of the full tree listing in order.
func treeRootKeys(t *testing.T, s *items.Store) []string {
	t.Helper()
	all, _, err := s.List(ctx, items.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, it := range all {
		if it.ParentID == "" {
			out = append(out, it.Key)
		}
	}
	return out
}

func TestBoardPagesRootsInOrder(t *testing.T) {
	s := newStore(t)
	seedBoardRoots(t, s, 35)

	p1, err := s.ListBoard(ctx, items.BoardFilter{Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	if roots := boardRoots(p1); len(roots) != 30 {
		t.Fatalf("page 1 roots = %d, want 30", len(roots))
	}
	if p1.NextCursor == "" {
		t.Fatal("page 1 next_cursor is empty, want a cursor")
	}
	if p1.RootsTotal != 35 {
		t.Fatalf("roots_total = %d, want 35", p1.RootsTotal)
	}
	// 35 roots + 7 stories + 7 tasks, all non-archived.
	if p1.Matches != 49 {
		t.Fatalf("matches = %d, want 49", p1.Matches)
	}
	// Page 1 holds exactly the first 30 roots of the tree order
	// (priority ASC, created_at DESC, id ASC) and only their trees.
	want := treeRootKeys(t, s)
	if got := boardRoots(p1); !slices.Equal(got, want[:30]) {
		t.Fatalf("page 1 roots = %v, want %v", got, want[:30])
	}
	inPage := map[string]bool{}
	for _, k := range boardRoots(p1) {
		inPage[k] = true
	}
	for _, it := range p1.Items {
		if !inPage[it.RootKey] {
			t.Fatalf("page 1 holds %s from outside tree %s", it.Key, it.RootKey)
		}
	}

	p2, err := s.ListBoard(ctx, items.BoardFilter{Limit: 30, Cursor: p1.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := boardRoots(p2), want[30:]; !slices.Equal(got, want) {
		t.Fatalf("page 2 roots = %v, want %v", got, want)
	}
	if p2.NextCursor != "" {
		t.Fatalf("last page next_cursor = %q, want empty", p2.NextCursor)
	}
	if p2.RootsTotal != 35 || p2.Matches != 49 {
		t.Fatalf("page 2 totals = %d/%d, want 35/49", p2.RootsTotal, p2.Matches)
	}
	for _, it := range p2.Items {
		if inPage[it.RootKey] {
			t.Fatalf("page 2 repeats tree %s from page 1", it.RootKey)
		}
	}
}

// Board filters select whole trees: q is the board's case-insensitive
// substring on key or title (not FTS), type/status match as today, and a
// tree is included when any item in it matches.
func TestBoardFiltersSelectWholeTrees(t *testing.T) {
	s := newStore(t)
	a := mk(t, s, items.Epic, "", "Alpha project")
	st := mk(t, s, items.Story, a.Key, "Login form")
	mk(t, s, items.Task, st.Key, "Build form")
	b := mk(t, s, items.Bug, "", "Crash on login")
	t2 := mk(t, s, items.Task, b.Key, "Fix null session")
	mk(t, s, items.Epic, "", "Unrelated")
	c, err := s.Create(ctx, items.CreateInput{Type: items.Chore, Title: "Cleanup"}, user)
	if err != nil {
		t.Fatal(err)
	}
	mk(t, s, items.Task, c.Key, "Sweep")

	// "LOGIN" matches the story title and the bug title; both whole trees
	// come along, including non-matching members.
	page, err := s.ListBoard(ctx, items.BoardFilter{Q: "LOGIN"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := boardRoots(page), []string{b.Key, a.Key}; !slices.Equal(got, want) {
		t.Fatalf("q roots = %v, want %v", got, want)
	}
	if keys, _ := listKeys(page.Items); !slices.Equal(keys, []string{b.Key, t2.Key, a.Key, st.Key, "TASK-1"}) {
		t.Fatalf("q items = %v", keys)
	}
	if page.Matches != 2 || page.RootsTotal != 2 {
		t.Fatalf("q totals = %d/%d, want 2/2", page.Matches, page.RootsTotal)
	}

	// Key substring: "task-2" matches TASK-2's key, pulling in its tree.
	keyed, err := s.ListBoard(ctx, items.BoardFilter{Q: "task-2"})
	if err != nil {
		t.Fatal(err)
	}
	if got := boardRoots(keyed); !slices.Equal(got, []string{b.Key}) {
		t.Fatalf("key q roots = %v", got)
	}
	if keyed.Matches != 1 || keyed.RootsTotal != 1 {
		t.Fatalf("key q totals = %d/%d, want 1/1", keyed.Matches, keyed.RootsTotal)
	}

	// The board client trims its filter, so padded queries match too.
	padded, err := s.ListBoard(ctx, items.BoardFilter{Q: "  login  "})
	if err != nil {
		t.Fatal(err)
	}
	if got := boardRoots(padded); !slices.Equal(got, []string{b.Key, a.Key}) {
		t.Fatalf("padded q roots = %v", got)
	}

	// Not FTS: reversed words match nothing.
	rev, err := s.ListBoard(ctx, items.BoardFilter{Q: "form login"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rev.Items) != 0 || rev.Matches != 0 || rev.RootsTotal != 0 || rev.NextCursor != "" {
		t.Fatalf("reversed q = %+v, want empty", rev)
	}

	// Type selects trees holding that type; chore is accepted in board mode.
	chores, err := s.ListBoard(ctx, items.BoardFilter{Type: items.Chore})
	if err != nil {
		t.Fatal(err)
	}
	if got := boardRoots(chores); !slices.Equal(got, []string{c.Key}) {
		t.Fatalf("chore roots = %v", got)
	}
	if chores.Matches != 1 || chores.RootsTotal != 1 {
		t.Fatalf("chore totals = %d/%d, want 1/1", chores.Matches, chores.RootsTotal)
	}

	// Status selects trees holding that status.
	setStatus(t, s, t2, items.Blocked)
	blocked, err := s.ListBoard(ctx, items.BoardFilter{Status: items.Blocked})
	if err != nil {
		t.Fatal(err)
	}
	if got := boardRoots(blocked); !slices.Equal(got, []string{b.Key}) {
		t.Fatalf("blocked roots = %v", got)
	}
	if blocked.Matches != 1 || blocked.RootsTotal != 1 {
		t.Fatalf("blocked totals = %d/%d, want 1/1", blocked.Matches, blocked.RootsTotal)
	}

	for _, f := range []items.BoardFilter{{Type: "saga"}, {Status: "doing"}} {
		if _, err := s.ListBoard(ctx, f); code(err) != items.CodeBadRequest {
			t.Errorf("%+v: err = %v", f, err)
		}
	}
}

// Filtered totals count across all pages: every page of a filtered listing
// reports the same matches and roots_total, and the pages partition the trees.
func TestBoardFilteredTotalsSpanPages(t *testing.T) {
	s := newStore(t)
	seedBoardRoots(t, s, 35)

	seen := map[string]int{}
	cursor := ""
	pages := 0
	for {
		p, err := s.ListBoard(ctx, items.BoardFilter{Q: "root", Limit: 10, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if p.RootsTotal != 35 || p.Matches != 35 {
			t.Fatalf("page totals = %d/%d, want 35/35", p.RootsTotal, p.Matches)
		}
		for _, it := range p.Items {
			seen[it.RootKey]++
		}
		pages++
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
		if pages > 10 {
			t.Fatal("paging did not terminate")
		}
	}
	if pages != 4 || len(seen) != 35 {
		t.Fatalf("pages = %d over %d trees, want 4 over 35", pages, len(seen))
	}
}

// A root inserted between page requests that sorts BEFORE the cursor (the
// usual case: a new root is the newest in its priority group, so it sorts
// first) is missed by forward paging, but every pre-existing tree is still
// visited exactly once: page 2 holds exactly the tail of the pre-insert
// order. OFFSET 30 would repeat page 1's last root here, so this test pins
// the keyset property. Roots are counted once per page, not per item.
func TestBoardInsertBetweenPages(t *testing.T) {
	s := newStore(t)
	seedBoardRoots(t, s, 35)
	want := treeRootKeys(t, s)

	p1, err := s.ListBoard(ctx, items.BoardFilter{Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	if got := boardRoots(p1); !slices.Equal(got, want[:30]) {
		t.Fatalf("page 1 roots = %v, want %v", got, want[:30])
	}
	// Priority 0, created after every seed: sorts before the cursor.
	zero := 0
	late, err := s.Create(ctx, items.CreateInput{
		Type: items.Epic, Title: "Late root", Priority: &zero,
	}, user)
	if err != nil {
		t.Fatal(err)
	}

	p2, err := s.ListBoard(ctx, items.BoardFilter{Limit: 30, Cursor: p1.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if got := boardRoots(p2); !slices.Equal(got, want[30:]) {
		t.Fatalf("page 2 roots = %v, want %v (tail of pre-insert order)", got, want[30:])
	}
	if p2.NextCursor != "" {
		t.Fatalf("next_cursor = %q, want empty on the last page", p2.NextCursor)
	}
	if p2.RootsTotal != 36 {
		t.Fatalf("roots_total = %d, want 36", p2.RootsTotal)
	}

	// Each pre-existing tree is visited exactly once across the two pages;
	// the late root sorts before the cursor so forward paging misses it.
	seen := map[string]int{}
	for _, k := range boardRoots(p1) {
		seen[k]++
	}
	for _, k := range boardRoots(p2) {
		seen[k]++
	}
	if len(seen) != 35 {
		t.Fatalf("forward paging visited %d trees, want the 35 pre-existing ones", len(seen))
	}
	for _, k := range want {
		if seen[k] != 1 {
			t.Fatalf("tree %s visited %d times, want exactly once", k, seen[k])
		}
	}
	if _, ok := seen[late.Key]; ok {
		t.Fatalf("late root %s sorts before the cursor but was visited by forward paging", late.Key)
	}

	// A refetch from the start picks the late root up first.
	fresh, err := s.ListBoard(ctx, items.BoardFilter{Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	if roots := boardRoots(fresh); len(roots) == 0 || roots[0] != late.Key {
		t.Fatalf("refetch first root = %v, want %s first", roots, late.Key)
	}
	if fresh.RootsTotal != 36 {
		t.Fatalf("refetch roots_total = %d, want 36", fresh.RootsTotal)
	}
}

// Board q folds case like the web client's toLowerCase, not just ASCII like
// SQLite's LOWER: 'über' finds 'Über' and 'ÉCOLE' finds 'école' (key/title).
func TestBoardQFoldsUnicodeCase(t *testing.T) {
	s := newStore(t)
	mk(t, s, items.Epic, "", "Über alles")
	mk(t, s, items.Epic, "", "école primaire")
	mk(t, s, items.Epic, "", "Plain")
	for _, c := range []struct {
		q    string
		want []string
	}{
		{"über", []string{"EPIC-1"}},
		{"ÜBER", []string{"EPIC-1"}},
		{"École", []string{"EPIC-2"}},
		{"ÉCOLE", []string{"EPIC-2"}},
	} {
		p, err := s.ListBoard(ctx, items.BoardFilter{Q: c.q})
		if err != nil {
			t.Fatal(err)
		}
		if got := boardRoots(p); !slices.Equal(got, c.want) {
			t.Errorf("q=%q roots = %v, want %v", c.q, got, c.want)
		}
	}
}
