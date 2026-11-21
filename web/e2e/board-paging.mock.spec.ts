import { type Page, expect, test } from "@playwright/test";
import { noHorizontalOverflow } from "./helpers";

const topRows = (page: Page) => page.locator('[role="treeitem"][aria-level="1"]');
// Kanban owns its own scroller (the last [data-board-scroll] inside App's section).
const kanbanScroller = (page: Page) => page.locator("[data-board-scroll]").last();

// The serve daemon seeds 100 generated roots (108 top-level items, 4 pages of 30/30/30/18), so
// one scroll must fetch exactly one page: a stale settle re-check would fire a second cursor
// request while the sentinel sits below the viewport.
function countCursorRequests(page: Page, counter: { n: number }) {
  page.on("request", (r) => {
    if (r.url().includes("/api/items") && r.url().includes("cursor=")) counter.n++;
  });
}

test.beforeEach(async ({ request }) => {
  await request.get("/__mock/reset");
});

test("board loads 30 roots first, each scroll loads exactly one more page", async ({ page }) => {
  const cursors = { n: 0 };
  countCursorRequests(page, cursors);
  await page.goto("/#/hierarchy");
  await expect(topRows(page)).toHaveCount(30);
  // EPIC-30 sorts last: it is outside the first pages.
  await expect(page.getByRole("treeitem", { name: /^EPIC-30 / })).toHaveCount(0);
  await page.getByTestId("view").evaluate((el) => el.scrollTo(0, el.scrollHeight));
  await expect(topRows(page)).toHaveCount(60);
  expect(cursors.n).toBe(1);
  await expect(page.getByRole("treeitem", { name: /^EPIC-30 / })).toHaveCount(0);
  await page.getByTestId("view").evaluate((el) => el.scrollTo(0, el.scrollHeight));
  await expect(topRows(page)).toHaveCount(90);
  expect(cursors.n).toBe(2);
  await page.getByTestId("view").evaluate((el) => el.scrollTo(0, el.scrollHeight));
  await expect(topRows(page)).toHaveCount(108);
  expect(cursors.n).toBe(3);
  await expect(page.getByRole("treeitem", { name: /^EPIC-30 / })).toBeVisible();
  // Nothing extra at the end once every page is loaded.
  await expect(page.getByTestId("board-sentinel")).toHaveCount(0);
  await noHorizontalOverflow(page);
});

test("a tall window fills past the first page without scrolling", async ({ page }) => {
  // Hold the cursor page so the second page cannot land early, however the sentinel reports.
  let release!: () => void;
  const gate = new Promise<void>((res) => {
    release = res;
  });
  await page.route(
    (url) => url.pathname === "/api/items" && url.searchParams.has("cursor"),
    async (route) => {
      await gate;
      await route.continue();
    },
  );
  // Tall enough for all 108 roots: the visible sentinel keeps chaining until the last page.
  await page.setViewportSize({ width: 1440, height: 4000 });
  await page.goto("/#/hierarchy");
  await expect(topRows(page)).toHaveCount(30);
  await expect(page.getByRole("treeitem", { name: /^EPIC-30 / })).toHaveCount(0);
  // Page 1 never overflows this viewport, so the visible sentinel keeps loading.
  release();
  await expect(topRows(page)).toHaveCount(108, { timeout: 10_000 });
  await expect(page.getByRole("treeitem", { name: /^EPIC-30 / })).toBeVisible();
  await noHorizontalOverflow(page);
});

test("kanban loads one more page per scroll from its own scroller, never chaining", async ({
  page,
}) => {
  // Hold the cursor page so the second page cannot land before the scroll, however the
  // sentinel first reports.
  let release!: () => void;
  const gate = new Promise<void>((res) => {
    release = res;
  });
  await page.route(
    (url) => url.pathname === "/api/items" && url.searchParams.has("cursor"),
    async (route) => {
      await gate;
      await route.continue();
    },
  );
  const cursors = { n: 0 };
  countCursorRequests(page, cursors);
  const cursorResponse = () =>
    page.waitForResponse((r) => r.url().includes("/api/items") && r.url().includes("cursor="));
  await page.goto("/#/kanban?level=tasks&group=root");
  await expect(page.getByTestId("board-sentinel")).toHaveCount(1);
  // The observer root is Kanban's own scroller, not App's section: scrolling the lane scroller
  // loads one page. Generated roots hold no tasks, so intermediate pages add no lanes here —
  // the cursor count is what proves exactly one page per scroll (a stale settle re-check would
  // fire a second request while the sentinel sits below the viewport).
  await expect(page.getByTestId("lane-EPIC-30")).toHaveCount(0);
  const first = cursorResponse();
  await kanbanScroller(page).evaluate((el) => el.scrollTo(0, el.scrollHeight));
  release();
  await first;
  await expect(page.getByTestId("lane-EPIC-30")).toHaveCount(0);
  expect(cursors.n).toBe(1);
  // The sentinel stays mounted while pages remain, but settling the append must not chain.
  await expect(page.getByTestId("board-sentinel")).toHaveCount(1);
  const second = cursorResponse();
  await kanbanScroller(page).evaluate((el) => el.scrollTo(0, el.scrollHeight));
  await second;
  expect(cursors.n).toBe(2);
  await expect(page.getByTestId("lane-EPIC-30")).toHaveCount(0);
  const third = cursorResponse();
  await kanbanScroller(page).evaluate((el) => el.scrollTo(0, el.scrollHeight));
  await third;
  await expect(page.getByTestId("lane-EPIC-30")).toBeVisible();
  await expect(page.getByTestId("board-sentinel")).toHaveCount(0);
  expect(cursors.n).toBe(3);
  await noHorizontalOverflow(page);
});

test("dependencies loads the next page from its load-more control", async ({ page }) => {
  await page.goto("/#/dependencies");
  // The graph canvas never scrolls, so its toolbar carries an explicit control instead of a
  // scroll sentinel, with the count of loaded vs total top-level items.
  const more = page.getByRole("button", { name: "Load more" });
  // The count text hides on narrow screens (the pager moves last); the button stays visible.
  const count = (n: string) => page.getByText(`${n} of 108 top-level items`);
  await expect(more).toBeVisible();
  await expect(count("30")).toBeAttached();
  await more.click();
  await expect(count("60")).toBeAttached();
  await more.click();
  await expect(count("90")).toBeAttached();
  await more.click();
  await expect(page.getByRole("button", { name: "Load more" })).toHaveCount(0);
  await noHorizontalOverflow(page);
});

test("loading row render while the next page fetches", async ({ page }) => {
  await page.goto("/#/hierarchy");
  await expect(topRows(page)).toHaveCount(30);
  // Hold the cursor page so the loading row stays on screen for the screenshot.
  await page.route(
    (url) => url.pathname === "/api/items" && url.searchParams.has("cursor"),
    async (route) => {
      await new Promise((r) => setTimeout(r, 2000));
      await route.continue();
    },
  );
  await page.getByTestId("view").evaluate((el) => el.scrollTo(0, el.scrollHeight));
  await expect(page.getByText("Loading more…")).toBeVisible();
  await page.screenshot({ path: "test-results/board-paging-loading.png" });
  await page
    .getByText("Loading more…")
    .screenshot({ path: "test-results/board-paging-loading-row.png" });
  await page.unrouteAll({ behavior: "wait" });
  await expect(topRows(page)).toHaveCount(60);
});
