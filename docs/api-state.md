# GET /api/state: bounded snapshot

The menu bar refetches `GET /api/state` on every daemon event, so the response must stay small and cheap whatever the history. `agentNodeOut` (`internal/httpapi/runtime.go`) costs about ten DB queries per node. On 2026-10-23 the unbounded response was 3.6 MB and 28–49 s: 3966 nodes for 249 top-level agents, 241 of them finished.

## Contract (extends contracts §3.2 AgentNode and §4 state)

- `/api/state` trims finished agents; `/api/agents` and `/api/items/{key}` do not and return the full tree.
- Per parent, `finished` holds at most 10 finished children, newest first by `finished_at` (else `created_at`). The top-level `agents` list holds at most 10 finished top-level agents.
- A finished node keeps only the finished children that lead to a live agent; its other finished descendants are dropped.
- A non-finished agent, and every ancestor of one, is never dropped, so `active_count` is the same as for the untrimmed tree. Finished means `agents.state` is `finished` or `acknowledged`; a child whose session ended while its agent state is still active is never trimmed, so in that rare case `finished` can hold more than 10.
- Each node carries `finished_count`: the untrimmed number of finished children. The state carries `finished_count`: the untrimmed number of finished top-level agents. The node field is absent on `/api/agents` and `/api/items/{key}`; clients fall back to `finished.length` when a daemon doesn't send them.
- `finished` is always `[]`, never null.

The trim is `pruneFinishedForState`, which runs on the flat agent list before `buildAgentTree`, so pruned agents cost no queries. Tests: `internal/httpapi/state_prune_test.go`.

## Rule for agents changing /api/state

- Never add per-node DB work for finished agents, and never build a node and then drop it. Prune the flat list first; keep the response bounded by the caps above, not by history.
- Anything that adds a field per node must be cheap for a node that isn't live.

## Measuring

```sh
curl -s -o /dev/null -w 'bytes=%{size_download} time=%{time_total}s\n' \
  -H "Authorization: Bearer $(cat ~/.swarm/run/daemon.token)" \
  "http://127.0.0.1:$(cat ~/.swarm/run/daemon.port)/api/state"
```

Expect well under 1 MB and a few seconds. Count nodes with `... | jq '[.agents | .. | objects | select(has("finished_count"))] | length'` (start at `.agents`: the state object carries `finished_count` too). Run the tests with `go test ./internal/httpapi -run 'PruneFinished|StateTrims|StateFinishedCount|StateNeverBuilds'`.

## GET /api/items views

- No `view` (or `view=tree`) returns the whole forest in tree order with
  `{items, matches}`; `view=flat` returns the flat match list in the same
  shape. Requests without the new params below are byte-identical to before.
- `view=flat&limit=N` caps the returned rows at N for picker search;
  `matches` still counts every match. A non-numeric or negative limit is a
  400 `bad_request` ("limit must be a number.").
- `view=board` is the opt-in paged mode for the web board: at most `limit`
  top-level items (default 30, clamped to [1, 1000]) per request, each with
  its whole non-archived subtree, ordered exactly like the web client's
  `compareTopLevel` (priority ASC, `created_at` DESC, id ASC).
- Paging is keyset-based: `next_cursor` encodes the last root of the page, so
  following it never repeats or skips a pre-existing tree. A root created
  ahead of the cursor (the usual case: a new root is the newest in its
  priority group, so it sorts first) appears only on a refetch from the
  start — the client refetches its loaded range in one request with
  `limit` set to the loaded count. `next_cursor` is `""` on the last page.
  A bad cursor or a non-numeric limit is a 400 `bad_request` with a plain
  message.
- Filters (`q`, `type`, `status`) select whole trees: a root's tree is
  included when any item in it matches. Board `q` is the board's
  case-insensitive substring on key or title, NOT the tree view's FTS query.
  `type` also accepts `chore` here. The response is
  `{items, matches, roots_total, next_cursor}`: `matches` counts matching
  items across all trees (every non-archived item when unfiltered),
  `roots_total` counts the trees across all pages.
- The root `LIMIT` happens in SQL before enrichment, so a page loads only its
  own trees. Tests: `internal/items/board_test.go`,
  `internal/httpapi/board_test.go`.
