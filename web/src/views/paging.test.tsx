import { act, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useBoardItems } from "../data/queries";
import { createMockDaemon } from "../mock/daemon";
import { seed, seedPaged } from "../mock/fixtures";
import { renderWithDaemon } from "../test/render";
import type { Filter } from "../types";
import { BoardSentinel } from "./BoardSentinel";
import { Dependencies } from "./Dependencies";
import { Hierarchy } from "./Hierarchy";
import { Kanban } from "./Kanban";

type Entry = { isIntersecting: boolean };
let ioCb: ((entries: Entry[]) => void) | null = null;

function stubIO() {
  ioCb = null;
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      constructor(cb: (entries: Entry[]) => void) {
        ioCb = cb;
      }
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  return () => {
    vi.unstubAllGlobals();
  };
}

const intersect = (v: boolean) => act(() => ioCb?.([{ isIntersecting: v }]));
const none: Filter = { q: "", type: "", status: "" };
const paging = (p: Partial<{ hasMore: boolean; loadingMore: boolean }> = {}) => ({
  loadMore: vi.fn(),
  hasMore: false,
  loadingMore: false,
  ...p,
});

describe("BoardSentinel", () => {
  it("loads one page per scroll-end and ignores repeats while visible", () => {
    stubIO();
    const loadMore = vi.fn();
    renderWithDaemon(<BoardSentinel loadMore={loadMore} hasMore loadingMore={false} />, {
      events: false,
    });
    intersect(false);
    intersect(true);
    intersect(true);
    expect(loadMore).toHaveBeenCalledTimes(1);
    vi.unstubAllGlobals();
  });

  it("never chains pages when permanently visible", () => {
    stubIO();
    const loadMore = vi.fn();
    renderWithDaemon(<BoardSentinel loadMore={loadMore} hasMore loadingMore={false} />, {
      events: false,
    });
    // The first observation reports the current state: a sentinel that starts out visible
    // must not kick off a load, or every page would chain while it stays on screen.
    intersect(true);
    expect(loadMore).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });

  it("shows a small loading row while fetching and nothing extra at the end", () => {
    const { unmount } = renderWithDaemon(<BoardSentinel loadMore={vi.fn()} hasMore loadingMore />, {
      events: false,
    });
    expect(screen.getByTestId("board-sentinel")).toHaveTextContent("Loading more…");
    unmount();
    renderWithDaemon(<BoardSentinel loadMore={vi.fn()} hasMore={false} loadingMore={false} />, {
      events: false,
    });
    expect(screen.queryByTestId("board-sentinel")).not.toBeInTheDocument();
  });
});

describe("view paging wires", () => {
  it("Hierarchy renders the sentinel when more pages remain", () => {
    stubIO();
    const p = paging({ hasMore: true });
    render(
      <Hierarchy
        items={seed().items}
        loaded
        filter={none}
        selected=""
        connected
        onSelect={vi.fn()}
        onAddChild={vi.fn()}
        onNewItem={vi.fn()}
        onClearFilters={vi.fn()}
        paging={p}
      />,
    );
    expect(screen.getByTestId("board-sentinel")).toBeInTheDocument();
    vi.unstubAllGlobals();
  });

  it("Kanban renders the sentinel in its own scroller when more pages remain", async () => {
    stubIO();
    const p = paging({ hasMore: true });
    function Host() {
      const board = useBoardItems(none);
      if (!board.loaded) return null;
      return (
        <Kanban
          items={board.items}
          loaded
          agents={[]}
          requests={[]}
          filter={none}
          selected=""
          connected
          level="tasks"
          group="root"
          onSelect={vi.fn()}
          onLevel={vi.fn()}
          onReview={vi.fn()}
          onClearFilters={vi.fn()}
          onNewItem={vi.fn()}
          paging={p}
        />
      );
    }
    renderWithDaemon(<Host />, { events: false });
    expect(await screen.findByTestId("board-sentinel")).toBeInTheDocument();
    vi.unstubAllGlobals();
  });

  it("Dependencies renders the sentinel when more pages remain", async () => {
    stubIO();
    const p = paging({ hasMore: true });
    function Host() {
      const board = useBoardItems(none);
      if (!board.loaded) return null;
      return (
        <Dependencies
          items={board.items}
          loaded
          filter={none}
          selected="EPIC-12"
          connected
          onSelect={vi.fn()}
          paging={p}
        />
      );
    }
    renderWithDaemon(<Host />, { daemon: createMockDaemon(seedPaged()), events: false });
    expect(await screen.findByTestId("board-sentinel")).toBeInTheDocument();
    vi.unstubAllGlobals();
  });
});
