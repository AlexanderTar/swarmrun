import { act, render, renderHook, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { createApi } from "../api";
import { makeItem } from "../logic/tree";
import type { Item } from "../types";
import { BoardSentinel } from "../views/BoardSentinel";
import { DataProvider, useInvalidate } from "./hooks";
import { useBoardItems } from "./queries";

function Host() {
  const b = useBoardItems({ q: "", type: "", status: "" });
  return (
    <div>
      <span data-testid="roots">{b.rootCount}</span>
      <span data-testid="err">{b.error ? "error" : ""}</span>
      <button type="button" onClick={b.reload}>
        retry
      </button>
      <BoardSentinel
        loadMore={b.loadMore}
        hasMore={b.nextCursor !== ""}
        loadingMore={b.loadingMore}
        appending={b.appending}
      />
    </div>
  );
}

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

  it("marks appending only for cursor appends, not range refetches", async () => {
    const all = roots(48);
    const kids = all.filter((i) => i.parent_key !== null);
    const rootList = all.filter((i) => i.parent_key === null);
    let releaseCursor: (() => void) | null = null;
    const cursorGate = new Promise<void>((res) => {
      releaseCursor = res;
    });
    const fn = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://test");
      if (url.pathname === "/api/auth/me")
        return Response.json({ origin: "local", authenticated: true });
      if (url.pathname === "/api/bootstrap") return Response.json({ token: "t" });
      const limit = Number(url.searchParams.get("limit") ?? 30);
      const cursor = url.searchParams.get("cursor") ?? "";
      if (cursor !== "") await cursorGate;
      const start = cursor === "" ? 0 : Number(cursor);
      const slice = rootList.slice(start, start + limit);
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
    const { result } = renderHook(() => useBoardItems({ q: "", type: "", status: "" }), {
      wrapper,
    });
    await waitFor(() => expect(result.current.loaded).toBe(true));
    expect(result.current.appending).toBe(false);
    act(() => result.current.loadMore());
    await waitFor(() => expect(result.current.appending).toBe(true));
    act(() => releaseCursor?.());
    await waitFor(() =>
      expect(result.current.items.filter((i) => i.parent_key === null)).toHaveLength(48),
    );
    expect(result.current.appending).toBe(false);
    vi.unstubAllGlobals();
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

  it("a failed append sends one request, not a retry loop, until the user retries", async () => {
    const all = roots(48);
    const rootList = all.filter((i) => i.parent_key === null);
    let cursorCalls = 0;
    let failCursor = true;
    const fn = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://test");
      if (url.pathname === "/api/auth/me")
        return Response.json({ origin: "local", authenticated: true });
      if (url.pathname === "/api/bootstrap") return Response.json({ token: "t" });
      const limit = Number(url.searchParams.get("limit") ?? 30);
      const cursor = url.searchParams.get("cursor") ?? "";
      if (cursor !== "") {
        cursorCalls++;
        if (failCursor)
          return Response.json({ error: { code: "internal", message: "boom" } }, { status: 500 });
      }
      const start = cursor === "" ? 0 : Number(cursor);
      return Response.json({
        items: rootList.slice(start, start + limit),
        matches: all.length,
        roots_total: rootList.length,
        next_cursor: start + limit < rootList.length ? String(start + limit) : "",
      });
    });
    vi.stubGlobal("fetch", fn as unknown as typeof fetch);
    // A permanently visible sentinel: every observe() reports visible, like a real observer
    // does for an on-screen node.
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        constructor(private cb: (e: { isIntersecting: boolean }[]) => void) {}
        observe() {
          setTimeout(() => this.cb([{ isIntersecting: true }]), 5);
        }
        unobserve() {}
        disconnect() {}
      },
    );
    const api = createApi();
    render(
      <DataProvider api={api} events={false}>
        <Host />
      </DataProvider>,
    );
    await waitFor(() => expect(screen.getByTestId("err")).toHaveTextContent("error"));
    // Give any retry loop several settle→rearm→append cycles to show itself.
    await new Promise((r) => setTimeout(r, 300));
    expect(cursorCalls).toBe(1);
    // The error banner's retry (reload) clears the error; the settle re-arms the sentinel,
    // which loads the next page once the daemon recovers.
    failCursor = false;
    act(() => screen.getByRole("button", { name: "retry" }).click());
    await waitFor(() => expect(screen.getByTestId("roots")).toHaveTextContent("48"));
    expect(screen.getByTestId("err")).toHaveTextContent("");
    vi.unstubAllGlobals();
  });

  it("range refetch past the server's 1000-root clamp follows next_cursor and keeps every root", async () => {
    const all = roots(1500);
    const calls: string[] = [];
    const fn = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://test");
      if (url.pathname === "/api/auth/me")
        return Response.json({ origin: "local", authenticated: true });
      if (url.pathname === "/api/bootstrap") return Response.json({ token: "t" });
      calls.push(url.pathname + url.search);
      // The daemon clamps limit to [1, 1000] (internal/items/board.go boardMaxLimit).
      const limit = Math.min(Number(url.searchParams.get("limit") ?? 30), 1000);
      const cursor = url.searchParams.get("cursor") ?? "";
      const start = cursor === "" ? 0 : Number(cursor);
      return Response.json({
        items: all.slice(start, start + limit),
        matches: all.length,
        roots_total: all.length,
        next_cursor: start + limit < all.length ? String(start + limit) : "",
      });
    });
    vi.stubGlobal("fetch", fn as unknown as typeof fetch);
    const api = createApi();
    const wrapper = ({ children }: { children: ReactNode }) => (
      <DataProvider api={api} events={false}>
        {children}
      </DataProvider>
    );
    const { result } = renderHook(
      () => ({ board: useBoardItems({ q: "", type: "", status: "" }), inv: useInvalidate() }),
      { wrapper },
    );
    await waitFor(() => expect(result.current.board.loaded).toBe(true));
    for (let n = 30; n < 1500; n += 30) {
      act(() => result.current.board.loadMore());
      await waitFor(() => expect(result.current.board.rootCount).toBe(n + 30));
    }
    expect(result.current.board.nextCursor).toBe("");
    const before = calls.length;
    act(() => result.current.inv(["items"]));
    await waitFor(() => expect(calls.length).toBeGreaterThan(before));
    await waitFor(() => expect(result.current.board.loadingMore).toBe(false));
    // Nothing past the clamp is dropped.
    expect(result.current.board.rootCount).toBe(1500);
    expect(new Set(result.current.board.items.map((i) => i.id)).size).toBe(1500);
    expect(result.current.board.nextCursor).toBe("");
    expect(calls.slice(before)).toEqual([
      "/api/items?view=board&limit=1000",
      "/api/items?view=board&limit=500&cursor=1000",
    ]);
    vi.unstubAllGlobals();
  });

  it("a range refetch stops when a page has no next_cursor or adds no new roots", async () => {
    const all = roots(40);
    let mode: "ok" | "no-cursor" | "stuck" = "ok";
    let refetchCalls = 0;
    const fn = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://test");
      if (url.pathname === "/api/auth/me")
        return Response.json({ origin: "local", authenticated: true });
      if (url.pathname === "/api/bootstrap") return Response.json({ token: "t" });
      if (mode !== "ok") refetchCalls++;
      // Cap a runaway loop so the red run ends: after 50 calls the server says "last page".
      const capped = refetchCalls > 50;
      if (mode === "no-cursor") return Response.json({ items: all.slice(0, 5) });
      return Response.json({
        items: mode === "stuck" ? all.slice(0, 5) : all.slice(0, 30),
        matches: all.length,
        roots_total: all.length,
        next_cursor: mode === "stuck" && !capped ? "same" : "",
      });
    });
    vi.stubGlobal("fetch", fn as unknown as typeof fetch);
    const api = createApi();
    const wrapper = ({ children }: { children: ReactNode }) => (
      <DataProvider api={api} events={false}>
        {children}
      </DataProvider>
    );
    const { result } = renderHook(
      () => ({ board: useBoardItems({ q: "", type: "", status: "" }), inv: useInvalidate() }),
      { wrapper },
    );
    await waitFor(() => expect(result.current.board.loaded).toBe(true));
    for (const m of ["no-cursor", "stuck"] as const) {
      mode = m;
      refetchCalls = 0;
      act(() => result.current.inv(["items"]));
      await waitFor(() => expect(refetchCalls).toBeGreaterThan(0));
      await waitFor(() => expect(result.current.board.loadingMore).toBe(false));
      // One request, plus at most one more that adds nothing, then stop.
      expect(refetchCalls).toBeLessThanOrEqual(2);
    }
    vi.unstubAllGlobals();
  });
});
