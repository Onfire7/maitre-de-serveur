// Service pages kept open like browser tabs (decision 286). A Service's
// frame stays open when another page is shown, so coming back finds it
// where it was; a frame idle for an hour is dropped and only the path it
// was on is remembered, so the next visit opens there.
//
// A frame is a native web view of its own (decision 294, ServiceViews),
// which the Console cannot read, so it learns where the page is from the
// script the loopback proxy adds to each document (src-tauri/src/proxy.rs),
// reported back as `service-view` events, and keeps the frame's history
// itself: Back and Forward send the view to a remembered address. The
// view is driven through a `FrameHost`, which the view layer provides.
import { useSyncExternalStore } from "react";

export const IDLE_MS = 3600_000;
const TICK_MS = 15_000;
/** Frames kept alive at once, the shown one included (decision 289): each
 * is a web process with a page's worth of memory; the longest-unseen idle
 * frame is dropped first, its path remembered as after the hour. */
export const MAX_LIVE = 3;

export interface Frame {
  service: string;
  /** The page's own address (the proxy's root), where Home goes. */
  home: string;
  /** The address the view opened at; stable while the frame lives. */
  src: string;
  /** Where the page is (path, search and hash), remembered after a drop. */
  path: string;
  /** The frame's own history, as paths. */
  entries: string[];
  index: number;
  /** Open. A dropped frame keeps only `path`. */
  live: boolean;
  /** The page's script has spoken: Back and Forward work. */
  reported: boolean;
  loading: boolean;
  /** The page's web process died; the view shows nothing until a reload. */
  crashed: boolean;
  /** Why the view could not be opened, when it could not. */
  failed?: string;
  /** When the frame stopped being the page shown; null while shown or dropped. */
  idleSince: number | null;
}

/** What drives the views: provided by the view layer, absent in tests. */
export interface FrameHost {
  navigate(service: string, url: string): void;
  reload(service: string): void;
  stop(service: string): void;
}

type Listener = () => void;
const frames = new Map<string, Frame>();
const listeners = new Set<Listener>();
/** The address a `go` was sent for: its report moves the index instead of adding an entry. */
const pending = new Map<string, { path: string; index: number }>();
let host: FrameHost | undefined;
let version = 0;
let ticker: ReturnType<typeof setInterval> | undefined;
let shown: string | null = null;
let clock = () => Date.now();

function emit() {
  version++;
  listeners.forEach((l) => l());
}

function update(service: string, f: (fr: Frame) => Frame) {
  const cur = frames.get(service);
  if (cur) frames.set(service, f(cur));
  emit();
}

function pathOf(url: string, base: string): string {
  try {
    const u = new URL(url, base);
    return u.pathname + u.search + u.hash;
  } catch {
    return "/";
  }
}

export function setFrameHost(h: FrameHost | undefined) {
  host = h;
}

/** A Service's page is the one shown; open it (at the remembered path) if it is not live. */
export function showFrame(service: string, home: string) {
  const cur = frames.get(service);
  const path = cur?.path ?? "/";
  const live = cur?.live && cur.home === home;
  frames.set(service, {
    service,
    home,
    src: live ? cur.src : new URL(path, home).href,
    path,
    entries: live ? cur.entries : [],
    index: live ? cur.index : 0,
    live: true,
    reported: live ? cur.reported : false,
    loading: live ? cur.loading : true,
    crashed: live ? cur.crashed : false,
    idleSince: null,
  });
  shown = service;
  // Over the cap: the idle frame unseen longest goes.
  for (;;) {
    const idle = [...frames.values()].filter((f) => f.live && f.service !== service);
    if (idle.length < MAX_LIVE) break;
    const oldest = idle.reduce((a, b) => ((a.idleSince ?? 0) <= (b.idleSince ?? 0) ? a : b));
    dropFrame(oldest.service);
  }
  emit();
}

/** The page shown is no longer this Service's: its frame starts idling. */
export function hideFrame(service: string) {
  if (shown === service) shown = null;
  update(service, (f) => (f.live && f.idleSince === null ? { ...f, idleSince: clock() } : f));
  tick();
}

/** Close the view, remembering only its path. */
export function dropFrame(service: string) {
  if (shown === service) shown = null;
  pending.delete(service);
  update(service, (f) => ({ ...f, live: false, reported: false, loading: false, crashed: false, entries: [], index: 0, idleSince: null }));
}

/** A Service that is gone takes its frame and its memory with it. */
export function forgetFrame(service: string) {
  if (shown === service) shown = null;
  pending.delete(service);
  if (frames.delete(service)) emit();
}

/** Frames idle for an hour are dropped; runs on a timer while any idles. */
export function tick(now = clock()) {
  let idle = false;
  for (const f of frames.values()) {
    if (f.live && f.idleSince !== null) {
      if (now - f.idleSince >= IDLE_MS) dropFrame(f.service);
      else idle = true;
    }
  }
  if (idle && !ticker) ticker = setInterval(() => tick(), TICK_MS);
  if (!idle && ticker) {
    clearInterval(ticker);
    ticker = undefined;
  }
  emit();
}

/** 1 when the frame was just left, 0 when it is about to be dropped; null when not idling. */
export function idleRemaining(f: Frame | undefined, now = clock()): number | null {
  if (!f || !f.live || f.idleSince === null) return null;
  return Math.max(0, 1 - (now - f.idleSince) / IDLE_MS);
}

export type FrameCommand = "back" | "forward" | "stop" | "reload" | "home";

export function canGo(f: Frame | undefined, cmd: "back" | "forward"): boolean {
  if (!f || !f.live || !f.reported) return false;
  return cmd === "back" ? f.index > 0 : f.index < f.entries.length - 1;
}

function go(f: Frame, index: number, path: string) {
  pending.set(f.service, { path, index });
  host?.navigate(f.service, new URL(path, f.home).href);
  update(f.service, (x) => ({ ...x, loading: true, crashed: false }));
}

export function frameCommand(service: string, cmd: FrameCommand) {
  const f = frames.get(service);
  if (!f) return;
  switch (cmd) {
    case "back":
      if (canGo(f, "back")) go(f, f.index - 1, f.entries[f.index - 1]);
      return;
    case "forward":
      if (canGo(f, "forward")) go(f, f.index + 1, f.entries[f.index + 1]);
      return;
    case "stop":
      if (!f.live) return;
      host?.stop(service);
      update(service, (x) => ({ ...x, loading: false }));
      return;
    case "reload":
      if (!f.live) return;
      host?.reload(service);
      update(service, (x) => ({ ...x, loading: true, crashed: false }));
      return;
    case "home":
      if (!f.live) {
        update(service, (x) => ({ ...x, path: "/" }));
        return;
      }
      if (f.reported) go(f, f.entries.length, "/");
      else {
        host?.navigate(service, f.home);
        update(service, (x) => ({ ...x, path: "/", loading: true, crashed: false }));
      }
  }
}

/** The engine started loading a document in the view. */
export function frameLoadStarted(service: string) {
  update(service, (f) => (f.live && !f.loading ? { ...f, loading: true } : f));
}

/** The engine finished a load (it says nothing of where). */
export function frameLoaded(service: string) {
  update(service, (f) => (f.live && f.loading ? { ...f, loading: false } : f));
}

/** The view could not be opened: the card says why. */
export function frameFailed(service: string, why: string) {
  update(service, (f) => (f.live ? { ...f, loading: false, failed: why } : f));
}

/** The page's web process died: the Console stays up, the page is gone until a reload. */
export function frameCrashed(service: string) {
  update(service, (f) => (f.live ? { ...f, loading: false, crashed: true } : f));
}

export type FrameEvent = "load" | "push" | "replace" | "pop" | "unload";

/** What a page's script reported, through the proxy's beacon. */
export function reportFrame(service: string, event: FrameEvent, url: string) {
  const f = frames.get(service);
  if (!f || !f.live) return;
  const path = pathOf(url, f.home);
  switch (event) {
    case "unload":
      update(f.service, (x) => ({ ...x, loading: true }));
      return;
    case "replace":
      if (f.reported && f.path === path && f.entries[f.index] === path) return;
      update(f.service, (x) => ({ ...x, reported: true, path, entries: x.entries.length ? x.entries.map((e, i) => (i === x.index ? path : e)) : [path] }));
      return;
    case "load":
    case "push":
    case "pop": {
      const p = pending.get(f.service);
      let { entries, index } = f;
      if (entries.length === 0) {
        entries = [path];
        index = 0;
      } else if (p && p.path === path) {
        // The address the Console asked for: a step back or forward, or Home as a new entry.
        if (p.index < entries.length) index = p.index;
        else {
          entries = [...entries.slice(0, f.index + 1), path];
          index = entries.length - 1;
        }
      } else if (event === "load" && path === entries[index]) {
        // A reload.
      } else if (event === "pop" && entries[index - 1] === path) index--;
      else if (event === "pop" && entries[index + 1] === path) index++;
      else if (path !== entries[index]) {
        entries = [...entries.slice(0, index + 1), path];
        index = entries.length - 1;
      }
      pending.delete(f.service);
      const loading = event === "load" ? f.loading : false;
      if (f.reported && f.path === path && f.index === index && f.entries === entries && f.loading === loading && !f.crashed) return;
      update(f.service, (x) => ({ ...x, reported: true, path, entries, index, loading, crashed: false }));
    }
  }
}

function subscribe(l: Listener) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

export function useFrame(service: string): Frame | undefined {
  useSyncExternalStore(subscribe, () => version);
  return frames.get(service);
}

export function useFrames(): Frame[] {
  useSyncExternalStore(subscribe, () => version);
  return [...frames.values()];
}

export function shownFrame(): string | null {
  return shown;
}

export function getFrame(service: string): Frame | undefined {
  return frames.get(service);
}

/** Tests: start over, with their own clock. */
export function resetFrames(now?: () => number) {
  frames.clear();
  pending.clear();
  host = undefined;
  shown = null;
  if (ticker) clearInterval(ticker);
  ticker = undefined;
  clock = now ?? (() => Date.now());
  emit();
}

/** Tests: hear every change. */
export function __subscribeForTest(l: Listener): () => void {
  return subscribe(l);
}

// A handle for the dev tools: `__frames` in the Console's window.
if (import.meta.env.DEV && typeof window !== "undefined") (window as unknown as { __frames: unknown }).__frames = { frames, shown: () => shown };
