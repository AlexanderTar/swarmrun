import { screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { createMockDaemon } from "../mock/daemon";
import { seedPaged } from "../mock/fixtures";
import { renderWithDaemon } from "../test/render";
import { AddDependency } from "./AddDependency";

describe("AddDependency", () => {
  it("searches its own flat query so results include items outside loaded board pages", async () => {
    const d = createMockDaemon(seedPaged());
    const { user } = renderWithDaemon(<AddDependency itemKey="TASK-101" disabled={false} />, {
      daemon: d,
      events: false,
    });
    await user.click(screen.getByRole("button", { name: /Add dependency/ }));
    await user.type(screen.getByRole("searchbox", { name: "Add dependency" }), "EPIC-139");
    // EPIC-139 sorts last: it is outside the board's first page but must still be offered.
    expect(
      await screen.findByRole("button", { name: "EPIC-139 · Generated epic 139" }),
    ).toBeInTheDocument();
    expect(d.calls.some((c) => c.path.startsWith("/api/items?view=flat&q=EPIC-139"))).toBe(true);
  });
});
