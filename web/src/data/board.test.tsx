import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { createApi } from "../api";
import { makeItem } from "../logic/tree";
import type { Item } from "../types";
import { DataProvider, useInvalidate } from "./hooks";
import { useBoardItems } from "./queries";

const roots = (n: number, off = 0): Item[] =>
  Array.from({ length: n }, (_, i) =>
    makeItem({
      key: `EPIC-${off + i}`,
      title: `Epic ${off + i}`,
      status: "ready",
      priority: 2,
      created_at: 1_000_000 - (off + i) * 1_000,
    }),
  );

// A mini board server: limit + opaque offset cursor, whole subtrees per root.
function setup(all: Item[]) {
  const calls: string[] = [];
  const kids = all.filter((i) => i.parent_key !== null);
  const rootList = all.filter((i) => i.parent_key === null);
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://test");
    calls.push(url.pathname + url.search);
    if (url.pathname === "/api/auth/me")
      return Response.json({ origin: "local", authenticated: true });
    if (url.pathname === "/api/bootstrap") return Response.json({ token: "t" });
    const limit = Number(url.searchParams.get("limit") ?? 30);
    const cursor = url.searchParams.get("cursor") ?? "";
    const start = cursor === "" ? 0 : Number(cursor);
    const slice = rootList.slice(start, start + limit);
    // Overlap the boundary root so the dedupe path is exercised, not just asserted.
    if (start > 0 && rootList[start - 1]) slice.unshift(rootList[start - 1]!);
    const keys = new Set(slice.map((r) => r.key));
    const items = [...slice, ...kids.filter((k) => keys.has(k.root_key))];
    const next = start + limit < rootList.length ? String(start + limit) : "";
    return Response.json({
      items,
      matches: all.length,
      roots_total: rootList.length,
      next_cursor: next,
    });
  });
  vi.stubGlobal("fetch", fn as unknown as typeof fetch);
  const api = createApi();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <DataProvider api={api} events={false}>
      {children}
    </DataProvider>
  );
  return { calls, wrapper };
}

const itemCalls = (calls: string[]) => calls.filter((c) => c.startsWith("/api/items"));

describe("useBoardItems", () => {
  it("loads only the first page with limit=30", async () => {
    const { calls, wrapper } = setup(roots(48));
    const { result } = renderHook(() => useBoardItems({ q: "", type: "", status: "" }), {
      wrapper,
    });
    await waitFor(() => expect(result.current.loaded).toBe(true));
    expect(itemCalls(calls)).toEqual(["/api/items?view=board&limit=30"]);
    expect(result.current.items.filter((i) => i.parent_key === null)).toHaveLength(30);
    expect(result.current.matches).toBe(48);
    expect(result.current.rootsTotal).toBe(48);
    expect(result.current.nextCursor).toBe("30");
  });

  it("loadMore appends the next page and dedupes by id", async () => {
    const { calls, wrapper } = setup(roots(48));
    const { result } = renderHook(() => useBoardItems({ q: "", type: "", status: "" }), {
      wrapper,
    });
    await waitFor(() => expect(result.current.loaded).toBe(true));
    const before = calls.length;
    act(() => result.current.loadMore());
    await waitFor(() =>
      expect(result.current.items.filter((i) => i.parent_key === null)).toHaveLength(48),
    );
    const fresh = itemCalls(calls.slice(before));
    expect(fresh).toHaveLength(1);
    expect(fresh[0]).toContain("cursor=30");
    const ids = result.current.items.map((i) => i.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(result.current.nextCursor).toBe("");
  });

  it("invalidation refetches the loaded range in one request without snapping back", async () => {
    const { calls, wrapper } = setup(roots(48));
    const { result } = renderHook(
      () => ({ board: useBoardItems({ q: "", type: "", status: "" }), inv: useInvalidate() }),
      { wrapper },
    );
    await waitFor(() => expect(result.current.board.loaded).toBe(true));
    act(() => result.current.board.loadMore());
    await waitFor(() =>
      expect(result.current.board.items.filter((i) => i.parent_key === null)).toHaveLength(48),
    );
    const lastPageOnly = result.current.board.items.find((i) => i.key === "EPIC-47")?.key;
    expect(lastPageOnly).toBe("EPIC-47");
    const before = calls.length;
    act(() => result.current.inv(["items"]));
    await waitFor(() => expect(itemCalls(calls.slice(before))).toHaveLength(1));
    expect(itemCalls(calls.slice(before))[0]).toBe("/api/items?view=board&limit=48");
    // Loaded pages are kept, not snapped back to page 1.
    expect(result.current.board.items.find((i) => i.key === "EPIC-47")?.key).toBe("EPIC-47");
    expect(result.current.board.items.filter((i) => i.parent_key === null)).toHaveLength(48);
  });

  it("a filter change restarts from page 1", async () => {
    const { calls, wrapper } = setup(roots(48));
    const { result, rerender } = renderHook(
      ({ q }: { q: string }) => useBoardItems({ q, type: "", status: "" }),
      { wrapper, initialProps: { q: "" } },
    );
    await waitFor(() => expect(result.current.loaded).toBe(true));
    expect(itemCalls(calls)).toEqual(["/api/items?view=board&limit=30"]);
    rerender({ q: "epic-1" });
    await waitFor(() =>
      expect(itemCalls(calls)).toEqual([
        "/api/items?view=board&limit=30",
        "/api/items?view=board&limit=30&q=epic-1",
      ]),
    );
  });
});
