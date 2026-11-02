// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { IDLE_MS, MAX_LIVE, __subscribeForTest, canGo, dropFrame, frameCommand, getFrame, handleFrameMessage, hideFrame, idleRemaining, registerFrameElement, resetFrames, showFrame, tick } from "./frames";

const HOME = "http://127.0.0.1:4100/";

/** A stand-in for the frame's window: what the Console posts to it, and a source for its messages. */
function mountFrame(service: string) {
  const posted: unknown[] = [];
  const win = { postMessage: (m: unknown) => posted.push(m) };
  const el = { contentWindow: win, src: "" } as unknown as HTMLIFrameElement;
  registerFrameElement(service, el);
  return { posted, win, el, say: (event: string, path: string) => handleFrameMessage(win, { maitre: "frame", event, url: new URL(path, HOME).href }) };
}

describe("frames (286)", () => {
  let now = 1_000_000;
  beforeEach(() => {
    vi.useFakeTimers();
    now = 1_000_000;
    resetFrames(() => now);
  });
  afterEach(() => vi.useRealTimers());

  it("keeps a frame's own history and steps through it by replacing the address", () => {
    showFrame("jellyfin", HOME);
    const f = mountFrame("jellyfin");
    f.say("load", "/");
    f.say("push", "/web/movies");
    f.say("push", "/web/movies/42");
    expect(getFrame("jellyfin")?.entries).toEqual(["/", "/web/movies", "/web/movies/42"]);
    expect(canGo(getFrame("jellyfin"), "back")).toBe(true);
    expect(canGo(getFrame("jellyfin"), "forward")).toBe(false);

    frameCommand("jellyfin", "back");
    expect(f.posted.at(-1)).toEqual({ maitre: "frame-cmd", cmd: "go", url: `${HOME}web/movies` });
    expect(getFrame("jellyfin")?.loading).toBe(true);
    f.say("load", "/web/movies");
    expect(getFrame("jellyfin")?.index).toBe(1);
    expect(getFrame("jellyfin")?.entries).toHaveLength(3);
    expect(canGo(getFrame("jellyfin"), "forward")).toBe(true);

    // A new page from the middle drops the forward entries, as a browser does.
    f.say("push", "/web/settings");
    expect(getFrame("jellyfin")?.entries).toEqual(["/", "/web/movies", "/web/settings"]);
    expect(canGo(getFrame("jellyfin"), "forward")).toBe(false);

    // The page's own back (popstate) is followed, not recorded twice.
    f.say("pop", "/web/movies");
    expect(getFrame("jellyfin")?.index).toBe(1);
    // A reload of the same address adds nothing.
    f.say("unload", "/web/movies");
    expect(getFrame("jellyfin")?.loading).toBe(true);
    f.say("load", "/web/movies");
    expect(getFrame("jellyfin")?.entries).toHaveLength(3);
    expect(getFrame("jellyfin")?.index).toBe(1);
  });

  it("Home adds an entry at the root; Stop and Refresh speak to the page once it has spoken", () => {
    showFrame("memos", HOME);
    const f = mountFrame("memos");
    // Before the page's script has spoken, nothing can be sent to it: Refresh sets the src again.
    expect(canGo(getFrame("memos"), "back")).toBe(false);
    frameCommand("memos", "reload");
    expect(f.el.src).toBe(HOME);
    f.say("load", "/explore");
    frameCommand("memos", "home");
    expect(f.posted.at(-1)).toEqual({ maitre: "frame-cmd", cmd: "go", url: HOME });
    f.say("load", "/");
    expect(getFrame("memos")?.entries).toEqual(["/explore", "/"]);
    expect(getFrame("memos")?.index).toBe(1);
    frameCommand("memos", "stop");
    expect(f.posted.at(-1)).toEqual({ maitre: "frame-cmd", cmd: "stop" });
    expect(getFrame("memos")?.loading).toBe(false);
    frameCommand("memos", "reload");
    expect(f.posted.at(-1)).toEqual({ maitre: "frame-cmd", cmd: "reload" });
  });

  it("an idle frame drains over an hour, is dropped, and comes back at the path it was on", () => {
    showFrame("jellyfin", HOME);
    const f = mountFrame("jellyfin");
    f.say("load", "/web/movies/42");
    expect(idleRemaining(getFrame("jellyfin"))).toBeNull();
    hideFrame("jellyfin");
    expect(idleRemaining(getFrame("jellyfin"))).toBe(1);
    now += IDLE_MS / 2;
    expect(idleRemaining(getFrame("jellyfin"))).toBeCloseTo(0.5);
    vi.advanceTimersByTime(15_000);
    expect(getFrame("jellyfin")?.live).toBe(true);
    now += IDLE_MS / 2;
    vi.advanceTimersByTime(15_000);
    const dropped = getFrame("jellyfin")!;
    expect(dropped.live).toBe(false);
    expect(dropped.path).toBe("/web/movies/42");
    expect(idleRemaining(dropped)).toBeNull();
    // Shown again: mounted where it was, with a fresh history.
    showFrame("jellyfin", HOME);
    expect(getFrame("jellyfin")?.src).toBe(`${HOME}web/movies/42`);
    expect(getFrame("jellyfin")?.entries).toEqual([]);
  });

  it("coming back before the hour keeps the frame and stops the clock", () => {
    showFrame("adguard", HOME);
    mountFrame("adguard").say("load", "/");
    hideFrame("adguard");
    now += IDLE_MS - 1;
    tick();
    expect(getFrame("adguard")?.live).toBe(true);
    showFrame("adguard", HOME);
    expect(idleRemaining(getFrame("adguard"))).toBeNull();
    now += IDLE_MS;
    tick();
    expect(getFrame("adguard")?.live).toBe(true);
    expect(getFrame("adguard")?.entries).toEqual(["/"]);
  });

  it("a frame that goes with its Service remembers its path but not its page", () => {
    showFrame("adguard", HOME);
    mountFrame("adguard").say("load", "/dns");
    dropFrame("adguard");
    expect(getFrame("adguard")).toMatchObject({ live: false, path: "/dns", reported: false });
    // A message from a window that is not a live frame is ignored.
    handleFrameMessage({}, { maitre: "frame", event: "push", url: `${HOME}x` });
    expect(getFrame("adguard")?.path).toBe("/dns");
  });

  it("at most MAX_LIVE frames stay alive; the one unseen longest goes first, keeping its path (289)", () => {
    const names = ["a", "b", "c", "d"];
    for (const n of names) {
      showFrame(n, HOME);
      mountFrame(n).say("load", `/${n}/page`);
      hideFrame(n);
      now += 60_000;
    }
    // Showing d (the fourth) dropped a, the one left longest ago.
    expect(names.map((n) => getFrame(n)?.live)).toEqual([false, true, true, true]);
    expect(getFrame("a")?.path).toBe("/a/page");
    expect(MAX_LIVE).toBe(3);
    // Coming back to a drops b, not the frame just shown.
    showFrame("a", HOME);
    expect(names.map((n) => getFrame(n)?.live)).toEqual([true, false, true, true]);
  });

  it("a page saying the same address again costs nothing (289)", () => {
    showFrame("memos", HOME);
    const f = mountFrame("memos");
    f.say("load", "/");
    let renders = 0;
    const unsub = __subscribeForTest(() => renders++);
    for (let i = 0; i < 50; i++) f.say("replace", "/");
    for (let i = 0; i < 50; i++) f.say("load", "/");
    expect(renders).toBe(0);
    f.say("replace", "/other");
    expect(renders).toBe(1);
    unsub();
  });
});
