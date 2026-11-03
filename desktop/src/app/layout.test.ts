import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { closePanel, fallbackColors, modalClosed, modalOpened, openPanel, pageLayout, resetLayout, setSampler } from "./layout";

// Decision 293: the page slides by the open flyout's width and hides behind
// a stand-in of its own colours while a modal is open.
describe("page layout", () => {
  beforeEach(() => resetLayout());
  afterEach(() => resetLayout());

  it("slides the page by the open panel's width and back", () => {
    expect(pageLayout().offset).toBe(0);
    openPanel(240);
    expect(pageLayout().offset).toBe(240);
    openPanel(480);
    expect(pageLayout().offset).toBe(480);
    closePanel();
    expect(pageLayout().offset).toBe(0);
  });

  it("samples the page before covering it, and uncovers when the last modal closes", async () => {
    const grid = [["red", "blue"], ["green", "white"]];
    let asked = 0;
    setSampler(async () => (asked++, grid));
    modalOpened();
    // Not covered until the sample is in: the stand-in needs its colours first.
    expect(pageLayout().covered).toBe(false);
    await Promise.resolve();
    await Promise.resolve();
    expect(pageLayout().covered).toBe(true);
    expect(pageLayout().colors).toEqual(grid);
    expect(asked).toBe(1);
    // A second modal over the first changes nothing and samples nothing.
    modalOpened();
    expect(asked).toBe(1);
    modalClosed();
    expect(pageLayout().covered).toBe(true);
    modalClosed();
    expect(pageLayout().covered).toBe(false);
    // The colours stay for the next cover, should sampling fail then.
    expect(pageLayout().colors).toEqual(grid);
  });

  it("covers with the theme's fallback when nothing can be sampled", async () => {
    modalOpened();
    expect(pageLayout().covered).toBe(true);
    expect(pageLayout().colors).toEqual(fallbackColors(false));
    modalClosed();
    resetLayout();
    setSampler(() => Promise.reject(new Error("no view")));
    modalOpened();
    await Promise.resolve();
    await Promise.resolve();
    expect(pageLayout().covered).toBe(true);
    expect(pageLayout().colors.length).toBe(3);
  });

  it("ignores a sample that arrives after its modal closed", async () => {
    let resolve: (g: string[][]) => void = () => {};
    setSampler(() => new Promise((r) => (resolve = r)));
    modalOpened();
    modalClosed();
    resolve([["red"]]);
    await Promise.resolve();
    await Promise.resolve();
    expect(pageLayout().covered).toBe(false);
  });

  it("closing more than opened is harmless", () => {
    modalClosed();
    expect(pageLayout().covered).toBe(false);
  });
});
