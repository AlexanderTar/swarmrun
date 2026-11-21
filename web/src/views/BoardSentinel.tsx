import { useEffect, useRef } from "react";
import { C } from "../copy";

/**
 * Infinite-scroll sentinel for the paged board views. It sits at the end of the view's real
 * scroll container (App's `section[data-testid=view]`, marked `data-board-scroll`; Kanban's own
 * scroller, marked the same) and loads one page at a time while `hasMore` (the server's
 * next_cursor) is set.
 *
 * Two guards keep a permanently visible sentinel from chaining every page: the observer ignores
 * its very first report (the current state on observe, not a scroll), and afterwards only a
 * false→true transition loads — while a load is in flight `loadingMore` disables the trigger so
 * repeated reports collapse into the one running request.
 */
export function BoardSentinel(p: {
  loadMore(): void;
  hasMore: boolean;
  loadingMore: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const state = useRef({ loadMore: p.loadMore, armed: false });
  state.current.loadMore = p.loadMore;
  state.current.armed = p.hasMore && !p.loadingMore;

  useEffect(() => {
    const el = ref.current;
    if (!el || !("IntersectionObserver" in window)) return;
    // The real scroll container, not the viewport: the section for Hierarchy/Dependencies,
    // Kanban's own scroller for Kanban.
    const root = el.closest("[data-board-scroll]");
    let first = true;
    let visible = false;
    const io = new IntersectionObserver(
      (entries) => {
        if (first) {
          // The initial report is the current state, not a scroll — a sentinel that starts out
          // visible must not kick off a load or every page would chain while it stays on screen.
          first = false;
          visible = entries.some((e) => e.isIntersecting);
          return;
        }
        const now = entries.some((e) => e.isIntersecting);
        const was = visible;
        visible = now;
        if (now && !was && state.current.armed) state.current.loadMore();
      },
      { root, rootMargin: "400px" },
    );
    io.observe(el);
    return () => io.disconnect();
  }, []);

  if (!p.hasMore && !p.loadingMore) return null;
  return (
    <div ref={ref} data-testid="board-sentinel" aria-hidden={!p.loadingMore}>
      {p.loadingMore && (
        // text-xs (0.75rem) is the DS caption size (design/tokens.css --text-caption) and the
        // codebase's helper-line convention, in muted-foreground like the header match count.
        <p className="py-2 text-center text-xs text-muted-foreground">{C.loadingMore}</p>
      )}
    </div>
  );
}
