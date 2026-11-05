// Service Pages kept open like browser tabs (decision 286). A Page's
// frame stays open when another Page is shown, so coming back finds it
// where it was; a frame idle for an hour is dropped and only the path it
// was on is remembered, so the next visit opens there.
//
// A frame is one Page of a Service (ADR 0010), keyed by the Page's host
// name: the Service's own name for its first Page, <service>-<page> for
// the others. That key is also the Page's proxy port and its view's
// label, so one string names the Page everywhere.
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
  /** The Page's host name, which keys its proxy, its view and this frame. */
  key: string;
  /** The Service the Page belongs to, and the Page's own name. */
  service: string;
  page: string;
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
  navigate(key: string, url: string): void;
  reload(key: string): void;
  stop(key: string): void;
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

function update(key: string, f: (fr: Frame) => Frame) {
  const cur = frames.get(key);
  if (cur) frames.set(key, f(cur));
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

/** This Page is the one shown; open it (at the remembered path) if it is not live. */
export function showFrame(key: string, home: string, service = key, page = "") {
  const cur = frames.get(key);
  const path = cur?.path ?? "/";
  const live = cur?.live && cur.home === home;
  frames.set(key, {
    key,
    service,
    page,
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
  shown = key;
  // Over the cap: the idle frame unseen longest goes.
  for (;;) {
    const idle = [...frames.values()].filter((f) => f.live && f.key !== key);
    if (idle.length < MAX_LIVE) break;
    const oldest = idle.reduce((a, b) => ((a.idleSince ?? 0) <= (b.idleSince ?? 0) ? a : b));
    dropFrame(oldest.key);
  }
  emit();
}

/** The Page shown is no longer this one: its frame starts idling. */
export function hideFrame(key: string) {
  if (shown === key) shown = null;
  update(key, (f) => (f.live && f.idleSince === null ? { ...f, idleSince: clock() } : f));
  tick();
}

/** Close the view, remembering only its path. */
export function dropFrame(key: string) {
  if (shown === key) shown = null;
  pending.delete(key);
  update(key, (f) => ({ ...f, live: false, reported: false, loading: false, crashed: false, entries: [], index: 0, idleSince: null }));
}

/** A Page that is gone takes its frame and its memory with it. */
export function forgetFrame(key: string) {
  if (shown === key) shown = null;
  pending.delete(key);
  if (frames.delete(key)) emit();
}

/** A Service that is gone takes every one of its Pages' frames with it. */
export function forgetService(service: string) {
  for (const f of [...frames.values()]) if (f.service === service) forgetFrame(f.key);
}

/** Frames idle for an hour are dropped; runs on a timer while any idles. */
export function tick(now = clock()) {
  let idle = false;
  for (const f of frames.values()) {
    if (f.live && f.idleSince !== null) {
      if (now - f.idleSince >= IDLE_MS) dropFrame(f.key);
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
  pending.set(f.key, { path, index });
  host?.navigate(f.key, new URL(path, f.home).href);
  update(f.key, (x) => ({ ...x, loading: true, crashed: false }));
}

export function frameCommand(key: string, cmd: FrameCommand) {
  const f = frames.get(key);
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
      host?.stop(key);
      update(key, (x) => ({ ...x, loading: false }));
      return;
    case "reload":
      if (!f.live) return;
      host?.reload(key);
      update(key, (x) => ({ ...x, loading: true, crashed: false }));
      return;
    case "home":
      if (!f.live) {
        update(key, (x) => ({ ...x, path: "/" }));
        return;
      }
      if (f.reported) go(f, f.entries.length, "/");
      else {
        host?.navigate(key, f.home);
        update(key, (x) => ({ ...x, path: "/", loading: true, crashed: false }));
      }
  }
}

/** The engine started loading a document in the view. */
export function frameLoadStarted(key: string) {
  update(key, (f) => (f.live && !f.loading ? { ...f, loading: true } : f));
}

/** The engine finished a load (it says nothing of where). */
export function frameLoaded(key: string) {
  update(key, (f) => (f.live && f.loading ? { ...f, loading: false } : f));
}

/** The view could not be opened: the card says why. */
export function frameFailed(key: string, why: string) {
  update(key, (f) => (f.live ? { ...f, loading: false, failed: why } : f));
}

/** The page's web process died: the Console stays up, the page is gone until a reload. */
export function frameCrashed(key: string) {
  update(key, (f) => (f.live ? { ...f, loading: false, crashed: true } : f));
}

export type FrameEvent = "load" | "push" | "replace" | "pop" | "unload";

/** What a page's script reported, through the proxy's beacon. */
export function reportFrame(key: string, event: FrameEvent, url: string) {
  const f = frames.get(key);
  if (!f || !f.live) return;
  const path = pathOf(url, f.home);
  switch (event) {
    case "unload":
      update(f.key, (x) => ({ ...x, loading: true }));
      return;
    case "replace":
      if (f.reported && f.path === path && f.entries[f.index] === path) return;
      update(f.key, (x) => ({ ...x, reported: true, path, entries: x.entries.length ? x.entries.map((e, i) => (i === x.index ? path : e)) : [path] }));
      return;
    case "load":
    case "push":
    case "pop": {
      const p = pending.get(f.key);
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
      pending.delete(f.key);
      const loading = event === "load" ? f.loading : false;
      if (f.reported && f.path === path && f.index === index && f.entries === entries && f.loading === loading && !f.crashed) return;
      update(f.key, (x) => ({ ...x, reported: true, path, entries, index, loading, crashed: false }));
    }
  }
}

function subscribe(l: Listener) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

export function useFrame(key: string): Frame | undefined {
  useSyncExternalStore(subscribe, () => version);
  return frames.get(key);
}

export function useFrames(): Frame[] {
  useSyncExternalStore(subscribe, () => version);
  return [...frames.values()];
}

export function shownFrame(): string | null {
  return shown;
}

export function getFrame(key: string): Frame | undefined {
  return frames.get(key);
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
