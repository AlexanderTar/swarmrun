import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { RequestField } from "../components/RequestField";
import { addImages } from "./requestImages";
import { useRequestAttachments } from "./useRequestAttachments";

vi.mock("./requestImages", async (orig) => ({
  ...(await orig<typeof import("./requestImages")>()),
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
const pdf = (name = "Spec.pdf") => new File(["%PDF-1.4"], name, { type: "application/pdf" });
const filesDrag = (files: File[] = []) => ({
  dataTransfer: { types: ["Files"], files, dropEffect: "" },
});
const textDrag = () => ({ dataTransfer: { types: ["text/plain"], files: [], dropEffect: "" } });

function Harness() {
  const state = useRequestAttachments({ disabled: false });
  return <RequestField label="Request (optional)" state={state} />;
}

function open() {
  const user = userEvent.setup();
  const view = render(<Harness />);
  const request = screen.getByRole("textbox", { name: "Request (optional)" });
  const zone = request.closest("[data-drop-zone]") as HTMLElement;
  return { ...view, user, request, zone };
}

beforeEach(() => {
  let n = 0;
  URL.createObjectURL = vi.fn(() => `blob:mock/${++n}`);
  URL.revokeObjectURL = vi.fn();
});

describe("useRequestAttachments and RequestField", () => {
  it("adds a pasted image, leaves the text alone, and removes the tile", async () => {
    const { request, user } = open();
    fireEvent.paste(request, {
      clipboardData: { files: [png("")], types: ["Files"], getData: () => "" },
    });
    expect(await screen.findByRole("img", { name: "Pasted image.png" })).toBeInTheDocument();
    expect(request).toHaveValue("");
    expect(screen.getByText("1 of 10")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Remove Pasted image.png" }));
    expect(screen.queryByRole("img", { name: "Pasted image.png" })).not.toBeInTheDocument();
  });

  it("adds a dropped file and clears the overlay", async () => {
    const { zone } = open();
    fireEvent.dragEnter(zone, filesDrag([pdf()]));
    expect(screen.getByText("Drop files to attach")).toBeInTheDocument();
    fireEvent.drop(zone, filesDrag([pdf()]));
    expect(await screen.findByRole("img", { name: "Spec.pdf" })).toBeInTheDocument();
    expect(screen.queryByText("Drop files to attach")).not.toBeInTheDocument();
  });

  it("shows a pending tile while a file is still being read, then the real one", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => {
      release = r;
    });
    vi.mocked(addImages).mockImplementationOnce(async (_n, files) => {
      await gate;
      return { added: files.map((f) => ({ ...f, image: true })) };
    });
    const { request } = open();
    fireEvent.paste(request, {
      clipboardData: { files: [png("slow.png")], types: ["Files"], getData: () => "" },
    });
    expect(document.querySelector('li[aria-hidden="true"]')).not.toBeNull();
    release();
    expect(await screen.findByRole("img", { name: "slow.png" })).toBeInTheDocument();
    await waitFor(() => expect(document.querySelector('li[aria-hidden="true"]')).toBeNull());
  });

  it("cancels a file drag outside the zone, leaves text drags, and detaches on unmount", () => {
    const { unmount, zone } = open();
    expect(fireEvent.dragOver(document.body, filesDrag())).toBe(false);
    expect(fireEvent.drop(document.body, filesDrag())).toBe(false);
    expect(fireEvent.dragOver(document.body, textDrag())).toBe(true);
    const dt = { types: ["Files"], files: [] as File[], dropEffect: "copy" };
    fireEvent.dragOver(document.body, { dataTransfer: dt });
    expect(dt.dropEffect).toBe("none");
    // Inside the zone the guard stays out; the zone's own handler decides.
    expect(zone.contains(zone.querySelector("textarea"))).toBe(true);
    unmount();
    expect(fireEvent.dragOver(document.body, filesDrag())).toBe(true);
  });
});
