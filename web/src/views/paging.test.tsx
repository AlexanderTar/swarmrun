import { act, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useBoardItems } from "../data/queries";
import { createMockDaemon } from "../mock/daemon";
import { seedPaged } from "../mock/fixtures";
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
const paging = (
  p: Partial<{ hasMore: boolean; loadingMore: boolean; appending: boolean }> = {},
) => ({
  loadMore: vi.fn(),
  hasMore: false,
  loadingMore: false,
  appending: false,
  ...p,
});
const livePaging = (b: {
  loadMore(): void;
  nextCursor: string;
  loadingMore: boolean;
  appending: boolean;
}) => ({
  loadMore: b.loadMore,
  hasMore: b.nextCursor !== "",
  loadingMore: b.loadingMore,
  appending: b.appending,
});

describe("BoardSentinel", () => {
  it("loads one page per scroll-end and ignores repeats while visible", () => {
    stubIO();
    const loadMore = vi.fn();
    renderWithDaemon(
      <BoardSentinel loadMore={loadMore} hasMore loadingMore={false} appending={false} />,
      { events: false },
    );
    intersect(false);
    intersect(true);
    intersect(true);
    expect(loadMore).toHaveBeenCalledTimes(1);
    vi.unstubAllGlobals();
  });

  it("loads when the first report shows a visible sentinel", () => {
    stubIO();
    const loadMore = vi.fn();
    renderWithDaemon(
      <BoardSentinel loadMore={loadMore} hasMore loadingMore={false} appending={false} />,
      { events: false },
    );
    // A first page that does not overflow leaves the sentinel visible: the first report must
    // load, or roots past page 1 can never be reached.
    intersect(true);
    expect(loadMore).toHaveBeenCalledTimes(1);
    vi.unstubAllGlobals();
  });

  it("shows a small loading row while appending and nothing extra at the end", () => {
    const { unmount } = renderWithDaemon(
      <BoardSentinel loadMore={vi.fn()} hasMore loadingMore appending />,
      { events: false },
    );
    const row = screen.getByTestId("board-sentinel");
    expect(row).toHaveTextContent("Loading more…");
    expect(within(row).getByRole("status")).toHaveTextContent("Loading more…");
    unmount();
    // A background range refetch with every page loaded shows no row.
    renderWithDaemon(
      <BoardSentinel loadMore={vi.fn()} hasMore={false} loadingMore appending={false} />,
      { events: false },
    );
    expect(screen.queryByTestId("board-sentinel")).not.toBeInTheDocument();
  });

  it("re-checks visibility after a load settles without a new scroll event", () => {
    stubIO();
    const loadMore = vi.fn();
    const { rerender } = renderWithDaemon(
      <BoardSentinel loadMore={loadMore} hasMore loadingMore={false} appending={false} />,
      { events: false },
    );
    intersect(true);
    expect(loadMore).toHaveBeenCalledTimes(1);
    // A load starts and settles with the sentinel still on screen and IO silent: the settle
    // re-check loads the next page even though the observer never re-fires.
    rerender(<BoardSentinel loadMore={loadMore} hasMore loadingMore appending />);
    rerender(<BoardSentinel loadMore={loadMore} hasMore loadingMore={false} appending={false} />);
    expect(loadMore).toHaveBeenCalledTimes(2);
    vi.unstubAllGlobals();
  });
});

describe("view paging wires", () => {
  it("Hierarchy intersection fetches the next page once", async () => {
    stubIO();
    function Host() {
      const board = useBoardItems(none);
      if (!board.loaded) return null;
      return (
        <Hierarchy
          items={board.items}
          loaded
          filter={none}
          selected=""
          connected
          onSelect={vi.fn()}
          onAddChild={vi.fn()}
          onNewItem={vi.fn()}
          onClearFilters={vi.fn()}
          paging={livePaging(board)}
        />
      );
    }
    renderWithDaemon(<Host />, { daemon: createMockDaemon(seedPaged()), events: false });
    expect(await screen.findByTestId("board-sentinel")).toBeInTheDocument();
    // EPIC-30 sorts last, so it rides on the second page only.
    expect(screen.queryByRole("treeitem", { name: /^EPIC-30 / })).not.toBeInTheDocument();
    intersect(true);
    expect(await screen.findByRole("treeitem", { name: /^EPIC-30 / })).toBeInTheDocument();
    expect(screen.queryByTestId("board-sentinel")).not.toBeInTheDocument();
    vi.unstubAllGlobals();
  });

  it("Kanban intersection fetches the next page once in its own scroller", async () => {
    stubIO();
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
          paging={livePaging(board)}
        />
      );
    }
    renderWithDaemon(<Host />, { daemon: createMockDaemon(seedPaged()), events: false });
    expect(await screen.findByTestId("board-sentinel")).toBeInTheDocument();
    expect(screen.queryByTestId("lane-EPIC-30")).not.toBeInTheDocument();
    intersect(true);
    expect(await screen.findByTestId("lane-EPIC-30")).toBeInTheDocument();
    vi.unstubAllGlobals();
  });

  it("Dependencies intersection fetches the next page", async () => {
    stubIO();
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
          paging={livePaging(board)}
        />
      );
    }
    const { daemon } = renderWithDaemon(<Host />, {
      daemon: createMockDaemon(seedPaged()),
      events: false,
    });
    expect(await screen.findByTestId("board-sentinel")).toBeInTheDocument();
    intersect(true);
    await waitFor(() => expect(daemon.calls.some((c) => c.path.includes("cursor="))).toBe(true));
    vi.unstubAllGlobals();
  });
});
