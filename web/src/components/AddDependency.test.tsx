import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { makeItem } from "../logic/tree";
import { createMockDaemon } from "../mock/daemon";
import { NOW, seedPaged } from "../mock/fixtures";
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

  it("finds a mid-word title hit with the substring mode, not crowded out by brief-only hits", async () => {
    const d = createMockDaemon(seedPaged());
    // 60 newer items whose brief (not key/title) mentions the term fill the FTS cap of 50.
    for (let i = 0; i < 60; i++)
      d.db.items.push(
        makeItem({
          key: `EPIC-${800 + i}`,
          title: `Brief noise ${i}`,
          brief: "mentions login in the brief",
          updated_at: NOW + i,
        }),
      );
    d.db.items.push(makeItem({ key: "EPIC-900", title: "Relogin form", updated_at: NOW - 5000 }));
    const { user } = renderWithDaemon(<AddDependency itemKey="TASK-101" disabled={false} />, {
      daemon: d,
      events: false,
    });
    await user.click(screen.getByRole("button", { name: /Add dependency/ }));
    await user.type(screen.getByRole("searchbox", { name: "Add dependency" }), "login");
    expect(
      await screen.findByRole("button", { name: "EPIC-900 · Relogin form" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Brief noise/ })).not.toBeInTheDocument();
    expect(d.calls.some((c) => c.path.includes("match=substring"))).toBe(true);
  });
});
