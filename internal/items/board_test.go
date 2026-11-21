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

// A root created between page requests with worse precedence than the cursor
// lands on a later page, so following next_cursor still visits every tree
// exactly once.
func TestBoardInsertBetweenPages(t *testing.T) {
	s := newStore(t)
	seedBoardRoots(t, s, 35)

	p1, err := s.ListBoard(ctx, items.BoardFilter{Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	worst := 3
	if _, err := s.Create(ctx, items.CreateInput{
		Type: items.Epic, Title: "Late root", Priority: &worst,
	}, user); err != nil {
		t.Fatal(err)
	}

	seen := map[string]int{}
	for _, it := range p1.Items {
		seen[it.RootKey]++
	}
	p2, err := s.ListBoard(ctx, items.BoardFilter{Limit: 30, Cursor: p1.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range p2.Items {
		seen[it.RootKey]++
	}
	if len(seen) != 36 {
		t.Fatalf("visited %d trees, want 36", len(seen))
	}
	for root, n := range seen {
		if n == 0 {
			t.Fatalf("tree %s never visited", root)
		}
	}
	if p2.NextCursor != "" {
		t.Fatalf("next_cursor = %q, want empty on the last page", p2.NextCursor)
	}
	if p2.RootsTotal != 36 {
		t.Fatalf("roots_total = %d, want 36", p2.RootsTotal)
	}
}
