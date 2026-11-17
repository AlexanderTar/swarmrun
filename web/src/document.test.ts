import { existsSync, readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

describe("document theme", () => {
  it("advertises the dark palette to browser controls", () => {
    const html = readFileSync("index.html", "utf8");
    const document = new DOMParser().parseFromString(html, "text/html");
    expect(document.querySelector('meta[name="color-scheme"]')?.getAttribute("content")).toBe(
      "dark",
    );
    expect(document.querySelector('meta[name="theme-color"]')?.getAttribute("content")).toBe(
      "#0B0B0E",
    );
  });
});

describe("app icon", () => {
  const document = new DOMParser().parseFromString(readFileSync("index.html", "utf8"), "text/html");

  it("serves the Liquid Glass app icon as the favicon", () => {
    const link = document.querySelector('link[rel="icon"]');
    expect(link?.getAttribute("href")).toBe("/favicon.png");
    expect(link?.getAttribute("type")).toBe("image/png");
    expect(existsSync("public/favicon.svg")).toBe(false); // the old flat drawing is gone
    const png = readFileSync("public/favicon.png");
    expect(png.subarray(1, 4).toString()).toBe("PNG");
    expect([png.readUInt32BE(16), png.readUInt32BE(20)]).toEqual([96, 96]); // IHDR width, height
    expect(png[25]).toBe(6); // RGBA: the rounded corners stay transparent
    // The site and the relay's offline page ship the same bytes (apps/menubar/scripts/build-icon.sh writes all three).
    expect(readFileSync("../site/assets/favicon.png").equals(png)).toBe(true);
    expect(readFileSync("../internal/relay/favicon.png").equals(png)).toBe(true);
  });

  it("links a 180px apple-touch-icon for phone home screens", () => {
    const href = document.querySelector('link[rel="apple-touch-icon"]')?.getAttribute("href");
    expect(existsSync("public/apple-touch-icon.png")).toBe(true);
    const png = readFileSync("public/apple-touch-icon.png");
    // Inlined so iOS needn't fetch it at Share time (a slow fetch freezes a title monogram into the webclip).
    expect(href?.startsWith("data:image/png;base64,")).toBe(true);
    expect(Buffer.from(href!.slice("data:image/png;base64,".length), "base64").equals(png)).toBe(
      true,
    );
    expect(png.subarray(1, 4).toString()).toBe("PNG");
    expect([png.readUInt32BE(16), png.readUInt32BE(20)]).toEqual([180, 180]); // IHDR width, height
    expect(png[25]).toBe(2); // IHDR colour type 2 = RGB: iOS fills alpha with black, so the square must be opaque
    expect(readFileSync("../site/assets/apple-touch-icon.png").equals(png)).toBe(true);
  });
});
