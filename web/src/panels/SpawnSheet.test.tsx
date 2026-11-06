import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { addImages } from "../logic/requestImages";
import { createMockDaemon } from "../mock/daemon";
import { renderWithDaemon } from "../test/render";
import { comboText, pickOption } from "../test/select";
import { SpawnSheet } from "./SpawnSheet";

vi.mock("../logic/requestImages", async (orig) => ({
  ...(await orig<typeof import("../logic/requestImages")>()),
  addImages: vi.fn(async (_n: number, files: { blob: Blob; name: string }[]) => ({
    added: files.map((f) => ({
      name: f.name,
      blob: f.blob,
      image: f.blob.type.startsWith("image/"),
    })),
  })),
}));

const png = (name = "a.png") =>
  new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], name, { type: "image/png" });

beforeEach(() => {
  let n = 0;
  URL.createObjectURL = vi.fn(() => `blob:mock/${++n}`);
  URL.revokeObjectURL = vi.fn();
});

const roomy = () => {
  const d = createMockDaemon();
  d.db.settings.max_concurrent_agents = 8;
  return d;
};

describe("SpawnSheet (§16.10)", () => {
  it("prefills repos, agent fields and the name", async () => {
    renderWithDaemon(<SpawnSheet itemKey="EPIC-12" onClose={vi.fn()} />, {
      daemon: roomy(),
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "Start orchestrator" });
    expect(within(sheet).getByText("EPIC-12 · Authentication")).toBeInTheDocument();
    expect(await within(sheet).findByText("Selected: endurio-chat")).toBeInTheDocument();
    expect(comboText("Model")).toBe("Opus (latest)");
    expect(within(sheet).getByRole("textbox", { name: "Name" })).toHaveValue(
      "authentication-orchestrator",
    );
    expect(within(sheet).getByRole("button", { name: "Start orchestrator" })).toBeEnabled();
  });

  it("uses the queued label at the orchestrator limit", async () => {
    renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={vi.fn()} />, { events: false });
    expect(await screen.findByRole("button", { name: "Queue orchestrator" })).toBeInTheDocument();
    expect(screen.getByText("Starts when an agent slot becomes available.")).toBeInTheDocument();
  });

  it("re-checks the model when the agent changes and blocks submit", async () => {
    const { user } = renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={vi.fn()} />, {
      daemon: roomy(),
      events: false,
    });
    await screen.findByRole("combobox", { name: "Agent" });
    await pickOption(user, "Agent", "Codex");
    expect(screen.getByText("Choose a model available for this agent.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start orchestrator" })).toBeDisabled();
  });

  it("submits the payload and closes", async () => {
    const onClose = vi.fn();
    const { user, daemon } = renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={onClose} />, {
      daemon: roomy(),
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "Start orchestrator" });
    await user.click(await within(sheet).findByRole("option", { name: /swarmrun/ }));
    const name = within(sheet).getByRole("textbox", { name: "Name" });
    await user.clear(name);
    await user.type(name, "billing-orchestrator");
    await user.dblClick(within(sheet).getByRole("button", { name: "Start orchestrator" }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(await screen.findByText(/^(Started|Queued) /)).toBeInTheDocument();
    const posts = daemon.calls.filter((c) => c.path === "/api/items/EPIC-20/orchestrator");
    expect(posts[0]?.body).toMatchObject({
      agent: "claude",
      model: "opus",
      repos: ["repo_swarm"],
      repos_version: 0,
      name: "billing-orchestrator",
      advisor: { agent: "claude", model: "fable" },
    });
    expect(new Set(posts.map((c) => (c.body as { request_id: string }).request_id)).size).toBe(1);
  });

  it("drops a stale effort Settings holds for a model that no longer offers it (progress.md:77 carry)", async () => {
    // claude-haiku-4-5-20251001 has efforts: [] in the mock catalog, so a stored "high" from an
    // earlier, different model is stale and unsupported. The Effort control is correctly hidden
    // (§16.4 L27), but the submit must not still carry the dead value.
    const d = roomy();
    d.db.settings.roles.orchestrator = { agent: "claude", model: "haiku", effort: "high" };
    const onClose = vi.fn();
    const { user } = renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={onClose} />, {
      daemon: d,
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "Start orchestrator" });
    expect(within(sheet).queryByRole("combobox", { name: "Effort" })).not.toBeInTheDocument();
    await user.click(within(sheet).getByRole("button", { name: "Start orchestrator" }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const post = d.calls.find((c) => c.path === "/api/items/EPIC-20/orchestrator");
    expect(post?.body).toMatchObject({ agent: "claude", model: "haiku" });
    expect(post?.body).not.toHaveProperty("effort");
  });

  it("keeps entries after a failure and retries with a new request id", async () => {
    const d = roomy();
    let calls = 0;
    d.override("POST /api/items/EPIC-20/orchestrator", () =>
      ++calls === 1
        ? {
            status: 422,
            body: {
              error: {
                code: "preflight_failed",
                message: "Commit signing is off for swarmrun. Enable it in git config.",
              },
            },
          }
        : { status: 200, body: d.db.agents[0] },
    );
    const onClose = vi.fn();
    const { user } = renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={onClose} />, {
      daemon: d,
      events: false,
    });
    const name = await screen.findByRole("textbox", { name: "Name" });
    await user.clear(name);
    await user.type(name, "kept-name");
    await user.click(screen.getByRole("button", { name: "Start orchestrator" }));
    expect(
      await screen.findByText("Couldn't start orchestrator. Your entries are saved."),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Commit signing is off for swarmrun. Enable it in git config."),
    ).toBeInTheDocument();
    expect(name).toHaveValue("kept-name");
    await user.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const ids = d.calls
      .filter((c) => c.path === "/api/items/EPIC-20/orchestrator")
      .map((c) => (c.body as { request_id: string }).request_id);
    expect(new Set(ids).size).toBe(2);
  });

  it("shows a taken name under the field", async () => {
    const d = roomy();
    const { user } = renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={vi.fn()} />, {
      daemon: d,
      events: false,
    });
    const name = await screen.findByRole("textbox", { name: "Name" });
    await user.clear(name);
    await user.type(name, "login-review");
    await user.click(screen.getByRole("button", { name: "Start orchestrator" }));
    expect(await screen.findByText("This agent name is already in use.")).toBeInTheDocument();
  });

  // Standing rule (flagged, not in the brief): a failed query gets a message + retry, never a
  // silently-empty sheet. The brief's SpawnSheet returned `null` forever on any of its four queries
  // failing.
  it("surfaces a failed load with a working retry (standing rule)", async () => {
    const d = roomy();
    const real = d.handle({
      method: "GET",
      url: "/api/items/EPIC-12",
      headers: { authorization: `Bearer ${d.db.token}` },
    });
    let attempt = 0;
    d.override("GET /api/items/EPIC-12", () => {
      attempt += 1;
      if (attempt === 1)
        return {
          status: 500,
          body: { error: { code: "internal", message: "x", reason: "Couldn't load the item." } },
        };
      return real;
    });
    const { user } = renderWithDaemon(<SpawnSheet itemKey="EPIC-12" onClose={vi.fn()} />, {
      daemon: d,
      events: false,
    });
    // Note: the error state (SpawnSheet -> Sheet directly) and the loaded state (SpawnSheet -> Form
    // -> Sheet) are different element types at the same tree position, so React unmounts and
    // remounts the whole <Sheet> (a fresh dialog DOM node) once the retry succeeds. Re-query via
    // `screen` after the retry instead of reusing a captured dialog reference, which would go stale.
    expect(await screen.findByText("Couldn't load the item.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() =>
      expect(screen.queryByText("Couldn't load the item.")).not.toBeInTheDocument(),
    );
    expect(await screen.findByRole("textbox", { name: "Name" })).toBeInTheDocument();
  });

  it("submits worker role overrides when customized", async () => {
    const onClose = vi.fn();
    const { user, daemon } = renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={onClose} />, {
      daemon: roomy(),
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "Start orchestrator" });
    await user.click(within(sheet).getByRole("button", { name: "Worker Roles" }));
    const coderGroup = within(sheet).getByRole("group", { name: "Coder" });
    await pickOption(user, "Coder Model", "Opus (latest)");
    await user.click(within(sheet).getByRole("button", { name: "Start orchestrator" }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const posts = daemon.calls.filter((c) => c.path === "/api/items/EPIC-20/orchestrator");
    expect(posts[0]?.body).toMatchObject({
      roles: {
        coder: {
          agent: "claude",
          model: "opus",
        },
      },
    });
  });

  it("shows the Designer default and submits its override", async () => {
    const onClose = vi.fn();
    const { user, daemon } = renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={onClose} />, {
      daemon: roomy(),
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "Start orchestrator" });
    await user.click(within(sheet).getByRole("button", { name: "Worker Roles" }));
    const designer = within(sheet).getByRole("group", { name: "Designer" });
    expect(within(designer).getByRole("combobox", { name: "Designer Agent" })).toHaveTextContent(
      "Claude",
    );
    expect(within(designer).getByRole("combobox", { name: "Designer Model" })).toHaveTextContent(
      "Opus (latest)",
    );
    await pickOption(user, "Designer Model", "Sonnet (latest)");
    await user.click(within(sheet).getByRole("button", { name: "Start orchestrator" }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const post = daemon.calls.find((c) => c.path === "/api/items/EPIC-20/orchestrator");
    expect(post?.body).toMatchObject({ roles: { designer: { agent: "claude", model: "sonnet" } } });
  });

  it("shows the additional request field below the name", async () => {
    renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={vi.fn()} />, {
      daemon: roomy(),
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "Start orchestrator" });
    const name = within(sheet).getByRole("textbox", { name: "Name" });
    const request = within(sheet).getByRole("textbox", { name: "Additional request (optional)" });
    expect(name.compareDocumentPosition(request) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(sheet.querySelector("[data-started]")).toHaveAttribute("data-started", "false");
  });

  it("shows a spinner on Start while sending and keeps the label", async () => {
    const d = roomy();
    const release = d.hold("POST /api/items/EPIC-20/orchestrator");
    const onClose = vi.fn();
    const { user } = renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={onClose} />, {
      daemon: d,
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "Start orchestrator" });
    const start = within(sheet).getByRole("button", { name: "Start orchestrator" });
    await user.click(start);
    expect(start).toBeDisabled();
    expect(start).toHaveTextContent("Start orchestrator");
    expect(start.querySelector("svg.animate-spin")).not.toBeNull();
    release();
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("keeps Start disabled, without a spinner, while a file is still being prepared", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => {
      release = r;
    });
    vi.mocked(addImages).mockImplementationOnce(async (_n, files) => {
      await gate;
      return { added: files.map((f) => ({ ...f, image: true })) };
    });
    renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={vi.fn()} />, {
      daemon: roomy(),
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "Start orchestrator" });
    const request = within(sheet).getByRole("textbox", { name: "Additional request (optional)" });
    fireEvent.paste(request, {
      clipboardData: { files: [png("slow.png")], types: ["Files"], getData: () => "" },
    });
    const start = within(sheet).getByRole("button", { name: "Start orchestrator" });
    expect(start).toBeDisabled();
    expect(start.querySelector("svg.animate-spin")).toBeNull();
    release();
    expect(await within(sheet).findByRole("img", { name: "slow.png" })).toBeInTheDocument();
    expect(start).toBeEnabled();
  });

  it("sends request and attachments only when they are present", async () => {
    const onClose = vi.fn();
    const first = renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={onClose} />, {
      daemon: roomy(),
      events: false,
    });
    const { daemon } = first;
    const sheet = await screen.findByRole("dialog", { name: "Start orchestrator" });
    await first.user.click(within(sheet).getByRole("button", { name: "Start orchestrator" }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    first.unmount();
    const empty = daemon.calls.find((c) => c.path === "/api/items/EPIC-20/orchestrator")
      ?.body as object;
    expect(empty).not.toHaveProperty("request");
    expect(empty).not.toHaveProperty("attachments");

    onClose.mockClear();
    const again = renderWithDaemon(<SpawnSheet itemKey="STORY-41" onClose={onClose} />, {
      daemon,
      events: false,
    });
    const sheet2 = await screen.findByRole("dialog", { name: "Start orchestrator" });
    const request = within(sheet2).getByRole("textbox", { name: "Additional request (optional)" });
    await again.user.type(request, "Look at this");
    fireEvent.paste(request, {
      clipboardData: { files: [png("a.png")], types: ["Files"], getData: () => "" },
    });
    expect(await within(sheet2).findByRole("img", { name: "a.png" })).toBeInTheDocument();
    await again.user.click(within(sheet2).getByRole("button", { name: "Start orchestrator" }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const body = daemon.calls.find((c) => c.path === "/api/items/STORY-41/orchestrator")?.body as {
      request?: string;
      attachments?: { name: string; data: string }[];
    };
    expect(body.request).toBe("Look at this");
    expect(body.attachments).toEqual([{ name: "a.png", data: btoa("\x89PNG") }]);
  });

  const pastePng = (request: HTMLElement, name = "a.png") =>
    fireEvent.paste(request, {
      clipboardData: { files: [png(name)], types: ["Files"], getData: () => "" },
    });

  it("maps a 413 to the too-large message and keeps the request and files", async () => {
    const d = roomy();
    d.db.force413 = true;
    const { user } = renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={vi.fn()} />, {
      daemon: d,
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "Start orchestrator" });
    const request = within(sheet).getByRole("textbox", { name: "Additional request (optional)" });
    await user.type(request, "Look");
    pastePng(request);
    expect(await within(sheet).findByRole("img", { name: "a.png" })).toBeInTheDocument();
    const body = sheet.querySelector<HTMLElement>(".overflow-y-auto") as HTMLElement;
    let top = 120;
    Object.defineProperty(body, "scrollTop", {
      configurable: true,
      get: () => top,
      set: (v: number) => {
        top = v;
      },
    });
    fireEvent.click(within(sheet).getByRole("button", { name: "Start orchestrator" }));
    const title = await within(sheet).findByText(
      "Couldn't start orchestrator. Your entries are saved.",
    );
    const alert = title.closest('[role="alert"]') as HTMLElement;
    expect(alert.querySelector(":scope > svg")).not.toBeNull();
    const text = alert.querySelector(":scope > div") as HTMLElement;
    expect(text).toContainElement(title);
    expect(text).toContainElement(
      within(alert).getByText(
        "The attachments are too large to send over remote access. Remove some and try again.",
      ),
    );
    expect(top).toBe(0);
    const retry = within(sheet).getByRole("button", { name: "Try again" });
    await waitFor(() => expect(retry).toHaveFocus());
    expect(request).toHaveValue("Look");
    expect(within(sheet).getByRole("img", { name: "a.png" })).toBeInTheDocument();
  });

  it("shows the files-not-saved warning, the started state and Done", async () => {
    const d = roomy();
    d.db.failAttachments = true;
    const onClose = vi.fn();
    const { user } = renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={onClose} />, {
      daemon: d,
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "Start orchestrator" });
    const request = within(sheet).getByRole("textbox", { name: "Additional request (optional)" });
    await user.type(request, "Look");
    pastePng(request);
    await within(sheet).findByRole("img", { name: "a.png" });
    const body = sheet.querySelector<HTMLElement>(".overflow-y-auto") as HTMLElement;
    let top = 120;
    Object.defineProperty(body, "scrollTop", {
      configurable: true,
      get: () => top,
      set: (v: number) => {
        top = v;
      },
    });
    await user.click(within(sheet).getByRole("button", { name: "Start orchestrator" }));
    const alert = await within(sheet).findByText(
      "Started, but the attachments could not be saved. Share them with the orchestrator another way.",
    );
    expect(alert.closest('[role="alert"]')).toBeInTheDocument();
    expect(top).toBe(0);
    expect(onClose).not.toHaveBeenCalled();
    const done = within(sheet).getByRole("button", { name: "Done" });
    expect(done).toHaveFocus();
    expect(within(sheet).getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(sheet.querySelector("[data-started]")).toHaveAttribute("inert");
    expect(
      within(sheet).queryByRole("button", { name: "Start orchestrator" }),
    ).not.toBeInTheDocument();
    await user.click(done);
    expect(onClose).toHaveBeenCalled();
    expect(await screen.findByText(/^(Started|Queued) /)).toBeInTheDocument();
  });

  it.each([
    [
      "Escape",
      async (user: ReturnType<typeof renderWithDaemon>["user"]) => user.keyboard("{Escape}"),
    ],
    [
      "the close button",
      async (user: ReturnType<typeof renderWithDaemon>["user"]) =>
        user.click(screen.getByRole("button", { name: "Close" })),
    ],
  ])("treats %s as Done when files were not saved", async (_n, close) => {
    const d = roomy();
    d.db.failAttachments = true;
    const onClose = vi.fn();
    const { user } = renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={onClose} />, {
      daemon: d,
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "Start orchestrator" });
    const request = within(sheet).getByRole("textbox", { name: "Additional request (optional)" });
    pastePng(request);
    await within(sheet).findByRole("img", { name: "a.png" });
    await user.click(within(sheet).getByRole("button", { name: "Start orchestrator" }));
    await within(sheet).findByRole("button", { name: "Done" });
    await close(user);
    expect(onClose).toHaveBeenCalled();
    expect(await screen.findByText(/^(Started|Queued) /)).toBeInTheDocument();
  });

  it("keeps the files-not-saved warning and entries when a reload fails", async () => {
    const d = roomy();
    d.db.failAttachments = true;
    const { user } = renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={vi.fn()} />, {
      daemon: d,
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "Start orchestrator" });
    const request = within(sheet).getByRole("textbox", { name: "Additional request (optional)" });
    await user.type(request, "Look");
    pastePng(request);
    await within(sheet).findByRole("img", { name: "a.png" });
    const agentsBefore = d.calls.filter((c) => c.path.startsWith("/api/agents")).length;
    d.override("GET /api/agents", () => ({
      status: 503,
      body: {
        error: { code: "unavailable", message: "blip", reason: "temporarily unavailable" },
      },
    }));
    await user.click(within(sheet).getByRole("button", { name: "Start orchestrator" }));
    await waitFor(() =>
      expect(d.calls.filter((c) => c.path.startsWith("/api/agents")).length).toBeGreaterThan(
        agentsBefore,
      ),
    );
    const open = screen.getByRole("dialog", { name: "Start orchestrator" });
    expect(
      within(open).getByText(
        "Started, but the attachments could not be saved. Share them with the orchestrator another way.",
      ),
    ).toBeInTheDocument();
    expect(within(open).getByRole("button", { name: "Done" })).toBeInTheDocument();
    expect(within(open).queryByRole("button", { name: "Retry" })).not.toBeInTheDocument();
    expect(
      within(open).getByRole("textbox", { name: "Additional request (optional)" }),
    ).toHaveValue("Look");
    expect(within(open).getByRole("img", { name: "a.png" })).toBeInTheDocument();
  });
});
