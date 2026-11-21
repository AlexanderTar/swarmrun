import { useCallback, useEffect, useRef } from "react";
import { C } from "../copy";

/**
 * Infinite-scroll sentinel for the paged board views. It sits at the end of the view's real
 * scroll container (App's `section[data-testid=view]`, marked `data-board-scroll`; Kanban's own
 * scroller, marked the same) and loads one page at a time while `hasMore` (the server's
 * next_cursor) is set.
 *
 * A permanently visible sentinel never chains every page on its own: the observer only fires on
 * a hidden→visible transition, so repeated reports while it stays on screen collapse into the
 * one running request (and concurrent calls collapse in the board hook's store guard). The one
 * case the observer cannot see is a first page that never overflows its container — no scroll
 * event ever fires — so when a load settles the sentinel re-arms the observer (resetting the
 * visible flag and re-observing the node) and waits for its fresh report. That re-check stops
 * exactly when content overflows (the fresh report says hidden) or the server clears
 * next_cursor, so it is bounded.
 */
export function BoardSentinel(p: {
  loadMore(): void;
  hasMore: boolean;
  loadingMore: boolean;
  /** True only while a cursor append is in flight; a range refetch shows no row. */
  appending: boolean;
}) {
  const live = useRef({ ...p, visible: false });
  live.current.loadMore = p.loadMore;
  live.current.hasMore = p.hasMore;
  live.current.loadingMore = p.loadingMore;

  const nodeRef = useRef<HTMLDivElement | null>(null);
  const ioRef = useRef<IntersectionObserver | null>(null);

  // A load just settled: re-arm the observer so the next decision comes from a fresh IO report,
  // not the stale visible flag (which still says visible until IO reports the post-append
  // position). Resetting visible first means the fresh true report is a real false→visible
  // trigger; a fresh false report loads nothing. Keyed on the true→false transition only, so
  // the mount render and repeats while visible never fire here — the observer owns those. A
  // fresh mount after an SSE-created page is covered by the observer's first report, so hasMore
  // needs no dep here. This fires for range refetches too (an SSE/mutation refetch of the loaded
  // range also settles loadingMore): with the sentinel inside the margin that loads the next
  // page, which is acceptable because the user is sitting near the end.
  const prevLoading = useRef(p.loadingMore);
  useEffect(() => {
    const was = prevLoading.current;
    prevLoading.current = p.loadingMore;
    if (!was || p.loadingMore) return;
    const s = live.current;
    if (!s.hasMore || s.loadingMore) return;
    const io = ioRef.current;
    const node = nodeRef.current;
    if (io && node) {
      s.visible = false;
      io.unobserve(node);
      io.observe(node);
      return;
    }
    if (s.visible) s.loadMore();
  }, [p.loadingMore]);

  // A callback ref, not an effect: the component renders null while there is no next page, so
  // the observer attaches when the element mounts — including a later mount (e.g. an
  // SSE-created 31st root after a range refetch) — and detaches via the cleanup on unmount.
  const setNode = useCallback((el: HTMLDivElement | null) => {
    if (ioRef.current) {
      ioRef.current.disconnect();
      ioRef.current = null;
    }
    nodeRef.current = el;
    if (!el || !("IntersectionObserver" in window)) return;
    // The real scroll container, not the viewport: the section for Hierarchy, Kanban's own
    // scroller for Kanban.
    const root = el.closest("[data-board-scroll]");
    const io = new IntersectionObserver(
      (entries) => {
        const now = entries.some((e) => e.isIntersecting);
        const s = live.current;
        const was = s.visible;
        s.visible = now;
        // The first report is the current state, and a first page that doesn't overflow starts
        // out visible: that false→visible transition is a real trigger, or roots past page 1
        // could never be reached.
        if (now && !was && s.hasMore && !s.loadingMore) s.loadMore();
      },
      { root, rootMargin: "400px" },
    );
    ioRef.current = io;
    io.observe(el);
    return () => io.disconnect();
  }, []);

  if (!p.hasMore && !p.appending) return null;
  return (
    <div ref={setNode} data-testid="board-sentinel">
      {p.appending && (
        // text-xs (0.75rem) is the DS caption size (design/tokens.css --text-caption) and the
        // codebase's helper-line convention, in muted-foreground like the header match count.
        // output's implicit status role announces the async append to screen readers.
        <output className="block py-2 text-center text-xs text-muted-foreground">
          {C.loadingMore}
        </output>
      )}
    </div>
  );
}
