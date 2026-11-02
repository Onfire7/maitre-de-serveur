// Service pages kept open like browser tabs (decision 286). A Service's
// frame stays mounted when another page is shown, so coming back finds it
// where it was; a frame idle for an hour is dropped and only the path it
// was on is remembered, so the next visit opens there.
//
// The page is cross-origin, so the Console learns where it is from the
// script the loopback proxy adds to each document (src-tauri/src/proxy.rs)
// and keeps the frame's history itself: Back and Forward replace the
// frame's address with a remembered one, which leaves the window's own
// history alone and so does not move another Service's frame.
import { useSyncExternalStore } from "react";

export const IDLE_MS = 3600_000;
const TICK_MS = 15_000;
/** Frames kept alive at once, the shown one included (decision 289): every
 * page shares the Console's web process and main thread, so a page that
 * hangs or works hard stalls the Console too; the longest-unseen idle
 * frame is dropped first, its path remembered as after the hour. */
export const MAX_LIVE = 3;

export interface Frame {
  service: string;
  /** The page's own address (the proxy's root), where Home goes. */
  home: string;
  /** The iframe's src when it was last mounted; stable while it lives. */
  src: string;
  /** Where the page is (path, search and hash), remembered after a drop. */
  path: string;
  /** The frame's own history, as paths. */
  entries: string[];
  index: number;
  /** Mounted. A dropped frame keeps only `path`. */
  live: boolean;
  /** The page's script has spoken: Back, Forward, Stop and Reload work. */
  reported: boolean;
  loading: boolean;
  /** When the frame stopped being the page shown; null while shown or dropped. */
  idleSince: number | null;
}

type Listener = () => void;
const frames = new Map<string, Frame>();
const elements = new Map<string, HTMLIFrameElement>();
const listeners = new Set<Listener>();
/** The address a `go` was sent for: its report moves the index instead of adding an entry. */
const pending = new Map<string, { path: string; index: number }>();
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

/** A Service's page is the one shown; mount it (at the remembered path) if it is not live. */
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

/** Unmount the frame, remembering only its path. */
export function dropFrame(service: string) {
  if (shown === service) shown = null;
  pending.delete(service);
  update(service, (f) => ({ ...f, live: false, reported: false, loading: false, entries: [], index: 0, idleSince: null }));
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

export function registerFrameElement(service: string, el: HTMLIFrameElement | null) {
  if (el) elements.set(service, el);
  else elements.delete(service);
}

export type FrameCommand = "back" | "forward" | "stop" | "reload" | "home";

export function canGo(f: Frame | undefined, cmd: "back" | "forward"): boolean {
  if (!f || !f.live || !f.reported) return false;
  return cmd === "back" ? f.index > 0 : f.index < f.entries.length - 1;
}

function post(f: Frame, msg: Record<string, unknown>) {
  const el = elements.get(f.service);
  let origin = "*";
  try {
    origin = new URL(f.home).origin;
  } catch {
    /* a page outside the proxy */
  }
  el?.contentWindow?.postMessage({ maitre: "frame-cmd", ...msg }, origin);
}

function go(f: Frame, index: number, path: string) {
  pending.set(f.service, { path, index });
  post(f, { cmd: "go", url: new URL(path, f.home).href });
  update(f.service, (x) => ({ ...x, loading: true }));
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
      if (f.reported) post(f, { cmd: "stop" });
      update(service, (x) => ({ ...x, loading: false }));
      return;
    case "reload": {
      if (!f.live) return;
      if (f.reported) post(f, { cmd: "reload" });
      else {
        const el = elements.get(service);
        if (el) el.src = f.src;
      }
      update(service, (x) => ({ ...x, loading: true }));
      return;
    }
    case "home": {
      if (!f.live) {
        update(service, (x) => ({ ...x, path: "/" }));
        return;
      }
      if (f.reported) go(f, f.entries.length, "/");
      else {
        const el = elements.get(service);
        if (el) el.src = f.home;
        update(service, (x) => ({ ...x, path: "/", loading: true }));
      }
    }
  }
}

/** The iframe element finished a load (cross-origin, it says nothing more). */
export function frameLoaded(service: string) {
  update(service, (f) => ({ ...f, loading: false }));
}

interface FrameMessage {
  maitre: "frame";
  event: "load" | "push" | "replace" | "pop" | "unload";
  url: string;
}

/** A message from a page's script, matched to its frame by the window it came from. */
export function handleFrameMessage(source: unknown, data: unknown) {
  const m = data as Partial<FrameMessage> | null;
  if (!m || m.maitre !== "frame" || typeof m.url !== "string") return;
  let service: string | undefined;
  for (const [name, el] of elements) if (el.contentWindow === source) service = name;
  const f = service ? frames.get(service) : undefined;
  if (!f || !f.live) return;
  const path = pathOf(m.url, f.home);
  switch (m.event) {
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
      } else if (m.event === "load" && path === entries[index]) {
        // A reload.
      } else if (m.event === "pop" && entries[index - 1] === path) index--;
      else if (m.event === "pop" && entries[index + 1] === path) index++;
      else if (path !== entries[index]) {
        entries = [...entries.slice(0, index + 1), path];
        index = entries.length - 1;
      }
      pending.delete(f.service);
      const loading = m.event === "load" ? f.loading : false;
      if (f.reported && f.path === path && f.index === index && f.entries === entries && f.loading === loading) return;
      update(f.service, (x) => ({ ...x, reported: true, path, entries, index, loading }));
    }
  }
}

let listening = false;
function listen() {
  if (listening || typeof window === "undefined") return;
  listening = true;
  window.addEventListener("message", (e) => handleFrameMessage(e.source, e.data));
}

function subscribe(l: Listener) {
  listen();
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
  elements.clear();
  pending.clear();
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
