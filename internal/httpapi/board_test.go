package httpapi

import (
	"fmt"
	"strings"
	"testing"
)

type boardList struct {
	Items      []itemJSON `json:"items"`
	Matches    int        `json:"matches"`
	RootsTotal int        `json:"roots_total"`
	NextCursor string     `json:"next_cursor"`
}

func boardRoots(t *testing.T, b []byte) boardList {
	t.Helper()
	return decode[boardList](t, b)
}

func TestBoardViewPages(t *testing.T) {
	e := newEnv(t)
	for i := range 32 {
		e.create(map[string]any{"type": "epic", "title": fmt.Sprintf("Root %02d", i)})
	}
	e.create(map[string]any{"type": "story", "title": "Child story", "parent_key": "EPIC-1"})
	e.create(map[string]any{"type": "task", "title": "Child task", "parent_key": "STORY-1"})

	status, b := e.api("GET", "/api/items?view=board", nil)
	if status != 200 {
		t.Fatalf("board = %d %s", status, b)
	}
	p1 := boardRoots(t, b)
	roots := 0
	for _, it := range p1.Items {
		if it.ParentKey == "" {
			roots++
		}
	}
	if roots != 30 || len(p1.Items) != 30 {
		t.Fatalf("page 1 = %d items (%d roots), want 30 items all roots", len(p1.Items), roots)
	}
	if p1.Matches != 34 || p1.RootsTotal != 32 || p1.NextCursor == "" {
		t.Fatalf("page 1 totals = %+v, want matches 34 roots_total 32 with cursor", p1)
	}

	status, b = e.api("GET", "/api/items?view=board&cursor="+p1.NextCursor, nil)
	if status != 200 {
		t.Fatalf("board page 2 = %d %s", status, b)
	}
	p2 := boardRoots(t, b)
	if len(p2.Items) != 4 || p2.NextCursor != "" {
		t.Fatalf("page 2 = %d items cursor %q, want 4 items and empty cursor", len(p2.Items), p2.NextCursor)
	}
	if p2.Matches != 34 || p2.RootsTotal != 32 {
		t.Fatalf("page 2 totals = %+v, want matches 34 roots_total 32", p2)
	}
	roots1 := map[string]bool{}
	for _, it := range p1.Items {
		if it.ParentKey == "" {
			roots1[it.Key] = true
		}
	}
	for _, it := range p2.Items {
		if it.ParentKey == "" {
			if roots1[it.Key] {
				t.Fatalf("page 2 repeats root %s from page 1", it.Key)
			}
			roots1[it.Key] = true
		}
	}
	if len(roots1) != 32 {
		t.Fatalf("pages visit %d roots, want 32", len(roots1))
	}
}

func TestBoardViewParams(t *testing.T) {
	e := newEnv(t)
	for i := range 5 {
		e.create(map[string]any{"type": "epic", "title": fmt.Sprintf("Root %02d", i)})
	}

	_, b := e.api("GET", "/api/items?view=board&limit=2", nil)
	if p := boardRoots(t, b); len(p.Items) != 2 || p.RootsTotal != 5 || p.Matches != 5 || p.NextCursor == "" {
		t.Fatalf("limit=2 = %+v", p)
	}
	// Out-of-range limits clamp to [1, 1000] instead of erroring.
	_, b = e.api("GET", "/api/items?view=board&limit=5000", nil)
	if p := boardRoots(t, b); len(p.Items) != 5 || p.NextCursor != "" {
		t.Fatalf("limit=5000 = %+v", p)
	}
	_, b = e.api("GET", "/api/items?view=board&limit=0", nil)
	if p := boardRoots(t, b); len(p.Items) != 1 {
		t.Fatalf("limit=0 = %+v, want 1 item", p)
	}

	for _, path := range []string{
		"/api/items?view=board&limit=many",
		"/api/items?view=board&cursor=!!!",
		"/api/items?view=flat&limit=many",
	} {
		status, body := e.api("GET", path, nil)
		if status != 400 || !strings.Contains(string(body), `"code":"bad_request"`) {
			t.Errorf("%s = %d %s, want 400 bad_request", path, status, body)
		}
	}
	if _, body := e.api("GET", "/api/items?view=board&limit=many", nil); !strings.Contains(string(body), "limit must be a number.") {
		t.Errorf("bad limit message = %s", body)
	}
	if _, body := e.api("GET", "/api/items?view=board&cursor=!!!", nil); !strings.Contains(string(body), "cursor is invalid.") {
		t.Errorf("bad cursor message = %s", body)
	}
}

func TestFlatViewLimitAndUnchangedLists(t *testing.T) {
	e := newEnv(t)
	e.create(map[string]any{"type": "epic", "title": "One"})
	e.create(map[string]any{"type": "epic", "title": "Two"})
	e.create(map[string]any{"type": "story", "title": "Sub", "parent_key": "EPIC-1"})

	_, b := e.api("GET", "/api/items?view=flat&limit=1", nil)
	flat := decode[struct {
		Items   []itemJSON `json:"items"`
		Matches int        `json:"matches"`
	}](t, b)
	if len(flat.Items) != 1 || flat.Matches != 3 {
		t.Fatalf("flat limit = %+v, want 1 item and 3 matches", flat)
	}

	// Requests without the new params keep their exact shape.
	for _, path := range []string{"/api/items", "/api/items?view=tree", "/api/items?view=flat"} {
		status, body := e.api("GET", path, nil)
		if status != 200 {
			t.Fatalf("%s = %d %s", path, status, body)
		}
		for _, absent := range []string{"roots_total", "next_cursor"} {
			if strings.Contains(string(body), absent) {
				t.Errorf("%s contains %q: %s", path, absent, body)
			}
		}
	}
}
