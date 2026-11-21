import { type Page, type TestInfo, expect, test } from "@playwright/test";
import { noHorizontalOverflow } from "./helpers";

const shot = (testInfo: TestInfo, name: string) =>
  `/Users/alexandertar/.swarm/screenshots/CHORE-237/board-paging-${testInfo.project.name}-${name}.png`;

const topRows = (page: Page) => page.locator('[role="treeitem"][aria-level="1"]');

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

test("loading row render while the next page fetches", async ({ page }, testInfo) => {
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
  await page.screenshot({ path: shot(testInfo, "loading"), animations: "disabled" });
  await page.unrouteAll({ behavior: "wait" });
  await expect(topRows(page)).toHaveCount(48);
});
