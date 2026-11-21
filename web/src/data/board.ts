import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import type { BoardItemsResponse, Item } from "../types";
import { useApi, useConnection, useQueryStore } from "./hooks";
import { qk } from "./queries";

export interface BoardFilter {
  q: string;
  type: string;
  status: string;
}

interface BoardData {
  items: Item[];
  matches: number;
  rootsTotal: number;
  nextCursor: string;
  rootCount: number;
}

export interface BoardResult extends BoardData {
  /** False until the first page settles; views show no empty state before that. */
  loaded: boolean;
  /** The first page is in flight. */
  loading: boolean;
  /** An append or range refetch is in flight with data already shown. */
  loadingMore: boolean;
  /** True only while a cursor append (not a range refetch) is in flight. */
  appending: boolean;
  error: unknown;
  loadMore(): void;
  reload(): void;
}

export const BOARD_PAGE = 30;
/** The daemon clamps a board request's limit to this (internal/items/board.go boardMaxLimit). */
const BOARD_MAX_LIMIT = 1000;

const countRoots = (items: Item[]) => items.filter((i) => i.parent_key === null).length;

function toBoard(res: BoardItemsResponse, prev?: BoardData): BoardData {
  const items = [...(prev?.items ?? [])];
  const seen = new Set(items.map((i) => i.id));
  for (const it of res.items)
    if (!seen.has(it.id)) {
      seen.add(it.id);
      items.push(it);
    }
  return {
    items,
    matches: res.matches,
    rootsTotal: res.roots_total,
    nextCursor: res.next_cursor,
    rootCount: countRoots(items),
  };
}

/**
 * Paged board items (GET /api/items?view=board). Loads the first page only, appends one page
 * per loadMore() while next_cursor is set, and refetches the whole loaded range (one request, or chunks of 1000 past the server clamp)
 * (limit = loaded root count, from the start) whenever the store invalidates the key — SSE item
 * events, reconnect `open` and mutations all invalidate the "items" prefix this key lives under.
 * Loaded pages are kept across refetches (never a snap back to page 1) and items de-dupe by id.
 * A filter change is a new key, so it restarts from page 1.
 */
export function useBoardItems(f: BoardFilter): BoardResult {
  const key = qk.board(f.q, f.type, f.status);
  const api = useApi();
  const store = useQueryStore();
  const { connected } = useConnection();
  // loadMore arms a cursor append; every other load (initial, invalidation, reload) is a range
  // refetch from the start. Read and cleared synchronously at the top of the loader, so a
  // concurrent invalidation always falls back to the range refetch once the append lands.
  const appendRef = useRef<string | null>(null);
  const [appending, setAppending] = useState(false);
  const filterRef = useRef(f);
  filterRef.current = f;
  const loadRef = useRef<() => Promise<BoardData>>(async () => ({
    items: [],
    matches: 0,
    rootsTotal: 0,
    nextCursor: "",
    rootCount: 0,
  }));
  loadRef.current = async () => {
    const append = appendRef.current;
    appendRef.current = null;
    const isAppend = append !== null;
    const flt = filterRef.current;
    const params = {
      q: flt.q || undefined,
      type: flt.type || undefined,
      status: flt.status || undefined,
    };
    if (isAppend) {
      try {
        const cur = store.get(key)?.data as BoardData | undefined;
        const res = await api.boardItems({ ...params, cursor: append, limit: BOARD_PAGE });
        return toBoard(res, cur);
      } finally {
        setAppending(false);
      }
    }
    const loaded = (store.get(key)?.data as BoardData | undefined)?.rootCount ?? 0;
    const want = Math.max(loaded, BOARD_PAGE);
    // The server clamps limit to BOARD_MAX_LIMIT, so a larger loaded range is fetched in chunks
    // that follow next_cursor; the data is replaced once, from the combined result.
    let res = await api.boardItems({ ...params, limit: Math.min(want, BOARD_MAX_LIMIT) });
    let board = toBoard(res);
    while (board.rootCount < want && res.next_cursor !== "") {
      res = await api.boardItems({
        ...params,
        cursor: res.next_cursor,
        limit: Math.min(want - board.rootCount, BOARD_MAX_LIMIT),
      });
      board = toBoard(res, board);
    }
    return board;
  };
  const subscribe = useCallback((fn: () => void) => store.subscribe(key, fn), [store, key]);
  const entry = useSyncExternalStore(subscribe, () => store.get(key));
  useEffect(() => {
    appendRef.current = null;
    setAppending(false);
    void store.fetch(key, () => loadRef.current());
  }, [key, store]);

  const loadMore = useCallback(() => {
    const entry = store.get(key);
    const data = entry?.data as BoardData | undefined;
    if (!data || data.nextCursor === "" || entry?.loading) return;
    // A failed load or a closed connection stops auto-loading: the sentinel re-arms after every
    // settle, so without this a rejecting append retries in a tight loop. Retry belongs to the
    // error banner (reload) and to the reconnect refetch, whose settle re-arms the sentinel.
    if (entry?.error !== undefined || !connected) return;
    appendRef.current = data.nextCursor;
    setAppending(true);
    void store.fetch(key, () => loadRef.current(), true);
  }, [store, key, connected]);
  const reload = useCallback(() => {
    appendRef.current = null;
    void store.fetch(key, () => loadRef.current(), true);
  }, [store, key]);

  const data = entry?.data as BoardData | undefined;
  const loading = entry?.loading ?? true;
  return {
    items: data?.items ?? [],
    matches: data?.matches ?? 0,
    rootsTotal: data?.rootsTotal ?? 0,
    nextCursor: data?.nextCursor ?? "",
    rootCount: data?.rootCount ?? 0,
    loaded: data !== undefined,
    loading: loading && data === undefined,
    loadingMore: loading && data !== undefined,
    appending,
    error: entry?.error,
    loadMore,
    reload,
  };
}
