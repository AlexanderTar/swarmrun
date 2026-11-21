import { type Page, expect, test } from "@playwright/test";
import { noHorizontalOverflow } from "./helpers";

const topRows = (page: Page) => page.locator('[role="treeitem"][aria-level="1"]');
// Kanban owns its own scroller (the last [data-board-scroll] inside App's section).
const kanbanScroller = (page: Page) => page.locator("[data-board-scroll]").last();

test.beforeEach(async ({ request }) => {
  await request.get("/__mock/reset");
});

test("board loads 30 roots first, scrolling loads the rest once", async ({ page }) => {
  await page.goto("/#/hierarchy");
  await expect(topRows(page)).toHaveCount(30);
  // EPIC-30 sorts last: it is outside the first page.
  await expect(page.getByRole("treeitem", { name: /^EPIC-30 / })).toHaveCount(0);
  await page.getByTestId("view").evaluate((el) => el.scrollTo(0, el.scrollHeight));
  await expect(topRows(page)).toHaveCount(48);
  await expect(page.getByRole("treeitem", { name: /^EPIC-30 / })).toBeVisible();
  // Nothing extra at the end once every page is loaded.
  await expect(page.getByTestId("board-sentinel")).toHaveCount(0);
  await noHorizontalOverflow(page);
});

test("a tall window fills past the first page without scrolling", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 2200 });
  await page.goto("/#/hierarchy");
  await expect(topRows(page)).toHaveCount(30);
  // Page 1 never overflows this viewport, so the visible sentinel keeps loading.
  await expect(topRows(page)).toHaveCount(48, { timeout: 10_000 });
  await expect(page.getByRole("treeitem", { name: /^EPIC-30 / })).toBeVisible();
  await noHorizontalOverflow(page);
});

test("kanban loads the rest once from its own scroller", async ({ page }) => {
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
  let cursors = 0;
  page.on("request", (r) => {
    if (r.url().includes("/api/items") && r.url().includes("cursor=")) cursors++;
  });
  await page.goto("/#/kanban?level=tasks&group=root");
  await expect(page.getByTestId("board-sentinel")).toHaveCount(1);
  // EPIC-30's lane rides on the second page only.
  await expect(page.getByTestId("lane-EPIC-30")).toHaveCount(0);
  await kanbanScroller(page).evaluate((el) => el.scrollTo(0, el.scrollHeight));
  release();
  await expect(page.getByTestId("lane-EPIC-30")).toBeVisible();
  await expect(page.getByTestId("board-sentinel")).toHaveCount(0);
  expect(cursors).toBe(1);
  await noHorizontalOverflow(page);
});

test("dependencies loads the next page from its load-more control", async ({ page }) => {
  await page.goto("/#/dependencies");
  // The graph canvas never scrolls, so its toolbar carries an explicit control instead of a
  // scroll sentinel, with the count of loaded vs total top-level items.
  const more = page.getByRole("button", { name: "Load more" });
  await expect(more).toBeVisible();
  await expect(page.getByText("30 of 48 top-level items")).toBeVisible();
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
  await expect(topRows(page)).toHaveCount(48);
});
