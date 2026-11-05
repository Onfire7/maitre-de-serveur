// @vitest-environment jsdom
import "@/test/dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render } from "@testing-library/react";

const invoke = vi.fn().mockResolvedValue(undefined);
vi.mock("@tauri-apps/api/core", () => ({ invoke: (...args: unknown[]) => invoke(...args) }));
vi.mock("@tauri-apps/api/event", () => ({ listen: vi.fn().mockResolvedValue(() => {}) }));

import { ServiceViews, onViewReport } from "./ServiceViews";
import { dropFrame, getFrame, resetFrames, showFrame } from "./frames";
import { modalClosed, modalOpened, openPanel, resetLayout } from "./layout";

const HOME = "http://127.0.0.1:4100/";
const calls = () => invoke.mock.calls.map(([cmd, args]) => [cmd, args]);
const settle = () => act(async () => {});

describe("ServiceViews (294)", () => {
  beforeEach(() => {
    resetFrames();
    resetLayout();
    invoke.mockClear();
    // jsdom lays nothing out: the surface says where it is.
    Element.prototype.getBoundingClientRect = () => ({ left: 240, top: 0, width: 800, height: 600, right: 1040, bottom: 600, x: 240, y: 0, toJSON() {} });
  });
  afterEach(() => resetLayout());

  it("opens the shown frame's view where the surface is, hides it under a modal, closes it when the frame is dropped", async () => {
    render(<ServiceViews />);
    act(() => showFrame("jellyfin", HOME));
    await settle();
    expect(calls()).toEqual([["view_open", { view: "jellyfin", url: HOME, bounds: { x: 240, y: 0, width: 800, height: 600 }, shown: true }]]);
    invoke.mockClear();
    // A modal covers the page: the stand-in's colours come from the view, then it hides.
    invoke.mockImplementation((cmd: string) => Promise.resolve(cmd === "view_sample" ? [["#111111", "#222222", "#333333", "#444444"]] : undefined));
    await act(async () => modalOpened());
    await settle();
    expect(calls()[0]).toEqual(["view_sample", { view: "jellyfin", cols: 4, rows: 3 }]);
    expect(calls()).toContainEqual(["view_show", { view: "jellyfin", shown: false }]);
    invoke.mockClear();
    act(() => modalClosed());
    await settle();
    expect(calls()).toEqual([["view_show", { view: "jellyfin", shown: true }]]);
    invoke.mockClear();
    act(() => dropFrame("jellyfin"));
    await settle();
    expect(calls()).toEqual([["view_close", { view: "jellyfin" }]]);
  });

  it("a second frame opens hidden; a crash hides the view until it speaks again", async () => {
    render(<ServiceViews />);
    act(() => showFrame("jellyfin", HOME));
    act(() => showFrame("memos", "http://127.0.0.1:4200/"));
    await settle();
    expect(calls()).toContainEqual(["view_open", { view: "memos", url: "http://127.0.0.1:4200/", bounds: { x: 240, y: 0, width: 800, height: 600 }, shown: true }]);
    // jellyfin is no longer the page shown.
    expect(calls()).toContainEqual(["view_show", { view: "jellyfin", shown: false }]);
    invoke.mockClear();
    act(() => onViewReport({ view: "memos", event: "crashed", url: "Crashed" }));
    await settle();
    expect(getFrame("memos")?.crashed).toBe(true);
    expect(calls()).toEqual([["view_show", { view: "memos", shown: false }]]);
    invoke.mockClear();
    act(() => onViewReport({ view: "memos", event: "load", url: "http://127.0.0.1:4200/explore" }));
    await settle();
    expect(getFrame("memos")).toMatchObject({ crashed: false, path: "/explore" });
    expect(calls()).toEqual([["view_show", { view: "memos", shown: true }]]);
  });

  it("the engine's load events drive the loading state without a path", () => {
    showFrame("jellyfin", HOME);
    onViewReport({ view: "jellyfin", event: "finished", url: HOME });
    expect(getFrame("jellyfin")).toMatchObject({ loading: false, reported: false });
    onViewReport({ view: "jellyfin", event: "started", url: `${HOME}web` });
    expect(getFrame("jellyfin")?.loading).toBe(true);
  });

  it("the view is placed again when the surface moves", async () => {
    render(<ServiceViews />);
    act(() => showFrame("jellyfin", HOME));
    await settle();
    invoke.mockClear();
    Element.prototype.getBoundingClientRect = () => ({ left: 480, top: 0, width: 800, height: 600, right: 1280, bottom: 600, x: 480, y: 0, toJSON() {} });
    act(() => {
      openPanel(240);
      window.dispatchEvent(new Event("resize"));
    });
    await settle();
    expect(calls()).toEqual([["view_place", { view: "jellyfin", bounds: { x: 480, y: 0, width: 800, height: 600 } }]]);
  });
});
