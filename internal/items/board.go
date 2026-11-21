package items

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"sort"
	"strings"

	"github.com/AlexanderTar/swarmrun/internal/db"
)

// BoardFilter selects top-level trees for a paged board listing. Q is a
// case-insensitive substring on key or title (the board's semantics, not FTS):
// a root's tree is included when any item in it matches. Cursor is the opaque
// page cursor from BoardPage.NextCursor; "" starts at the first page. Limit is
// roots per page; 0 means the board default (30).
type BoardFilter struct {
	Q      string
	Type   Type
	Status Status
	Cursor string
	Limit  int
}

// BoardPage is one page of top-level trees: Items holds at most Limit roots in
// compareTopLevel order (priority ASC, created_at DESC, id ASC), each followed
// by its whole non-archived subtree. Matches counts matching items across all
// trees (all non-archived items when unfiltered); RootsTotal counts the trees
// across all pages. NextCursor is "" on the last page.
type BoardPage struct {
	Items      []Item
	Matches    int
	RootsTotal int
	NextCursor string
}

const boardDefaultLimit = 30
const boardMaxLimit = 1000

// boardCursor is the keyset position after a page's last root.
type boardCursor struct {
	P  int    `json:"p"`
	C  int64  `json:"c"`
	ID string `json:"id"`
}

func encodeBoardCursor(it Item) string {
	raw, _ := json.Marshal(boardCursor{P: it.Priority, C: db.Millis(it.CreatedAt), ID: it.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeBoardCursor(cur string) (boardCursor, error) {
	var c boardCursor
	raw, err := base64.RawURLEncoding.DecodeString(cur)
	if err != nil {
		return c, errf(CodeBadRequest, "cursor is invalid.")
	}
	if err := json.Unmarshal(raw, &c); err != nil || c.ID == "" {
		return c, errf(CodeBadRequest, "cursor is invalid.")
	}
	return c, nil
}

// escapeLike escapes \, % and _ for a LIKE ... ESCAPE '\' match.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	return strings.ReplaceAll(s, `_`, `\_`)
}

// boardPreds builds the q/type/status predicates against alias, appending args.
func boardPreds(alias string, f BoardFilter, args *[]any) []string {
	var where []string
	if f.Type != "" {
		where = append(where, alias+".type = ?")
		*args = append(*args, f.Type)
	}
	if f.Status != "" {
		where = append(where, alias+".status = ?")
		*args = append(*args, f.Status)
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		// Trimmed like the board client's filter before matching.
		pat := "%" + escapeLike(strings.ToLower(q)) + "%"
		where = append(where, "(LOWER("+alias+".key) LIKE ? ESCAPE '\\' OR LOWER("+alias+".title) LIKE ? ESCAPE '\\')")
		*args = append(*args, pat, pat)
	}
	return where
}

// qualifyingRoots is the WHERE fragment selecting top-level, non-archived
// roots whose tree holds a match, appending args.
func qualifyingRoots(alias string, f BoardFilter, args *[]any) string {
	cond := alias + ".parent_id IS NULL AND " + alias + ".archived_at IS NULL"
	var dargs []any
	if preds := boardPreds("d", f, &dargs); len(preds) > 0 {
		cond += " AND EXISTS (SELECT 1 FROM items d WHERE d.root_id = " + alias +
			".id AND d.archived_at IS NULL AND " + strings.Join(preds, " AND ") + ")"
		*args = append(*args, dargs...)
	}
	return cond
}

func (s *Store) ListBoard(ctx context.Context, f BoardFilter) (BoardPage, error) {
	// Board mode accepts chore as a type filter; the tree/flat List does not.
	if f.Type != "" && !slices.Contains([]Type{Epic, Story, Task, Bug, Spike, Chore}, f.Type) {
		return BoardPage{}, errf(CodeBadRequest, "Unknown item type %q.", f.Type)
	}
	if f.Status != "" && !slices.Contains(allStatuses, f.Status) {
		return BoardPage{}, errf(CodeBadRequest, "Unknown status %q.", f.Status)
	}
	limit := f.Limit
	if limit == 0 {
		limit = boardDefaultLimit
	}
	if limit < 1 || limit > boardMaxLimit {
		return BoardPage{}, errf(CodeBadRequest, "limit must be between 1 and 1000.")
	}
	var cur boardCursor
	if f.Cursor != "" {
		var err error
		if cur, err = decodeBoardCursor(f.Cursor); err != nil {
			return BoardPage{}, err
		}
	}

	var args []any
	where := qualifyingRoots("i", f, &args)
	if f.Cursor != "" {
		where += " AND (i.priority > ? OR (i.priority = ? AND i.created_at < ?) OR " +
			"(i.priority = ? AND i.created_at = ? AND i.id > ?))"
		args = append(args, cur.P, cur.P, cur.C, cur.P, cur.C, cur.ID)
	}
	// The root LIMIT happens in SQL, before any enrichment: only this page's
	// trees are loaded below. One extra row detects the last page.
	args = append(args, limit+1)
	roots, err := s.scanItems(ctx, `SELECT `+itemCols+` WHERE `+where+
		` ORDER BY i.priority ASC, i.created_at DESC, i.id ASC LIMIT ?`, args...)
	if err != nil {
		return BoardPage{}, err
	}
	next := ""
	if len(roots) > limit {
		next = encodeBoardCursor(roots[limit-1])
		roots = roots[:limit]
	}

	var totalArgs []any
	var rootsTotal int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM items i WHERE `+
		qualifyingRoots("i", f, &totalArgs), totalArgs...).Scan(&rootsTotal); err != nil {
		return BoardPage{}, err
	}

	var margs []any
	mwhere := "m.archived_at IS NULL"
	if preds := boardPreds("m", f, &margs); len(preds) > 0 {
		mwhere += " AND " + strings.Join(preds, " AND ")
	}
	// Matches count matching items in live trees (an archived root's tree is out).
	mwhere += " AND EXISTS (SELECT 1 FROM items r WHERE r.id = m.root_id AND " +
		"r.parent_id IS NULL AND r.archived_at IS NULL)"
	var matches int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM items m WHERE `+mwhere, margs...).Scan(&matches); err != nil {
		return BoardPage{}, err
	}

	all := make([]Item, 0, len(roots))
	all = append(all, roots...)
	if len(roots) > 0 {
		ids := make([]any, len(roots))
		for i := range roots {
			ids[i] = roots[i].ID
		}
		// One query loads every non-archived descendant of the page's roots.
		desc, err := s.queryItems(ctx, s.DB, `SELECT `+itemCols+` WHERE i.root_id IN (`+
			placeholders(len(ids))+`) AND i.archived_at IS NULL AND i.parent_id IS NOT NULL`, ids...)
		if err != nil {
			return BoardPage{}, err
		}
		all = append(all, desc...)
		if err := s.enrich(ctx, s.DB, roots); err != nil {
			return BoardPage{}, err
		}
		for i := range roots {
			all[i] = roots[i]
		}
	}
	return BoardPage{Items: assembleTree(all), Matches: matches, RootsTotal: rootsTotal, NextCursor: next}, nil
}

// scanItems runs query and scans each row with no enrichment.
func (s *Store) scanItems(ctx context.Context, query string, args ...any) ([]Item, error) {
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// assembleTree orders items roots-first like the tree view: top-level items by
// priority ASC, created_at DESC, id ASC, each followed depth-first by its
// subtree (children by sort_order, then key number). Orphans are dropped.
func assembleTree(all []Item) []Item {
	children := map[string][]Item{}
	var tops []Item
	for _, it := range all {
		if it.ParentID == "" {
			tops = append(tops, it)
		} else {
			children[it.ParentID] = append(children[it.ParentID], it)
		}
	}
	sort.Slice(tops, func(i, j int) bool {
		if tops[i].Priority != tops[j].Priority {
			return tops[i].Priority < tops[j].Priority
		}
		if !tops[i].CreatedAt.Equal(tops[j].CreatedAt) {
			return tops[i].CreatedAt.After(tops[j].CreatedAt)
		}
		return tops[i].ID < tops[j].ID // migrated rows share timestamps
	})
	out := []Item{}
	var walk func(it Item)
	walk = func(it Item) {
		out = append(out, it)
		kids := children[it.ID]
		sort.Slice(kids, func(i, j int) bool {
			if kids[i].SortOrder != kids[j].SortOrder {
				return kids[i].SortOrder < kids[j].SortOrder
			}
			return keyNumber(kids[i].Key) < keyNumber(kids[j].Key)
		})
		for _, k := range kids {
			walk(k)
		}
	}
	for _, t := range tops {
		walk(t)
	}
	return out
}
