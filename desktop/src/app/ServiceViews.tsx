// The layer of Service views (decision 294). Each live frame is a native
// web view of the main window (src-tauri/src/views.rs), drawn by the
// engine above the Console's HTML; this layer tells the app where the
// page area is, which view is the one shown, when a modal covers it, and
// takes what the views report. The area is the page surface (decision
// 293): it slides right by an open panel's width, so the views follow it,
// measured each frame of the slide.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";
import { frameCrashed, frameFailed, frameLoadStarted, frameLoaded, reportFrame, setFrameHost, shownFrame, useFrames, type FrameEvent, type FrameHost } from "./frames";
import { PageSurface } from "./PageSurface";
import { setSampler, usePageLayout } from "./layout";

/** Where a view sits: the page surface's box, in the window's CSS pixels. */
export interface ViewBounds {
  x: number;
  y: number;
  width: number;
  height: number;
}

interface ViewReport {
  service: string;
  event: "started" | "finished" | "crashed" | FrameEvent;
  url: string;
}

const host: FrameHost = {
  navigate: (service, url) => void invoke("view_navigate", { service, url }).catch(() => {}),
  reload: (service) => void invoke("view_reload", { service }).catch(() => {}),
  stop: (service) => void invoke("view_stop", { service }).catch(() => {}),
};

export function onViewReport({ service, event, url }: ViewReport) {
  switch (event) {
    case "started":
      return frameLoadStarted(service);
    case "finished":
      return frameLoaded(service);
    case "crashed":
      return frameCrashed(service);
    default:
      return reportFrame(service, event, url);
  }
}

export function ServiceViews() {
  const frames = useFrames().filter((f) => f.live);
  const shown = shownFrame();
  const { covered } = usePageLayout();
  const ref = useRef<HTMLDivElement>(null);
  const bounds = useBounds(ref);
  useEffect(() => {
    setFrameHost(host);
    const off = listen<ViewReport>("service-view", (e) => onViewReport(e.payload));
    return () => {
      setFrameHost(undefined);
      off.then((f) => f()).catch(() => {});
    };
  }, []);
  // A modal's stand-in takes its colours from the shown view (decision 293).
  useEffect(() => {
    if (!shown) return;
    setSampler(() => invoke<string[][]>("view_sample", { service: shown, cols: 4, rows: 3 }));
    return () => setSampler(undefined);
  }, [shown]);
  return (
    <PageSurface ref={ref} className="pointer-events-none z-0">
      {frames.map((f) => (
        <NativeView key={f.service} service={f.service} src={f.src} bounds={bounds} visible={f.service === shown && !covered && !f.crashed} />
      ))}
    </PageSurface>
  );
}

/** One view: opened where the surface is, moved with it, shown while it
 * is the page and nothing covers it, closed with its frame. The calls go
 * one after another, so a view is never shown before it is open. */
function NativeView({ service, src, bounds, visible }: { service: string; src: string; bounds: ViewBounds | null; visible: boolean }) {
  const queue = useRef<Promise<unknown>>(Promise.resolve());
  const opened = useRef<string | null>(null);
  const shown = useRef(false);
  const run = (f: () => Promise<unknown>) => {
    queue.current = queue.current.then(f, f).catch(() => {});
  };
  useEffect(() => {
    if (!bounds) return;
    if (opened.current === null) {
      // Opened, or reopened after the dev-time double mount, at this src.
      opened.current = src;
      shown.current = visible;
      run(() => invoke("view_open", { service, url: src, bounds, shown: visible }).catch((e) => frameFailed(service, String(e))));
    } else run(() => invoke("view_place", { service, bounds }));
  }, [service, bounds]);
  useEffect(() => {
    if (opened.current !== null && opened.current !== src) {
      opened.current = src;
      run(() => invoke("view_navigate", { service, url: src }));
    }
  }, [service, src]);
  useEffect(() => {
    if (opened.current === null || shown.current === visible) return;
    shown.current = visible;
    run(() => invoke("view_show", { service, shown: visible }));
  }, [service, visible]);
  // Unmounted: the view closes. (React's dev-time double mount closes and
  // reopens it, since `opened` is reset here.)
  useEffect(() => {
    return () => {
      if (opened.current === null) return;
      opened.current = null;
      run(() => invoke("view_close", { service }));
    };
  }, [service]);
  return null;
}

/** The element's box, kept current: on its resize, the window's, and
 * through each frame of its transform transition (the slide). */
export function useBounds(ref: React.RefObject<HTMLElement | null>): ViewBounds | null {
  const [bounds, setBounds] = useState<ViewBounds | null>(null);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    let last: ViewBounds | null = null;
    const measure = () => {
      const r = el.getBoundingClientRect();
      const b = { x: Math.round(r.left), y: Math.round(r.top), width: Math.round(r.width), height: Math.round(r.height) };
      if (last && last.x === b.x && last.y === b.y && last.width === b.width && last.height === b.height) return;
      last = b;
      setBounds(b);
    };
    measure();
    let raf = 0;
    let sliding = 0;
    const frame = () => {
      measure();
      raf = sliding > 0 ? requestAnimationFrame(frame) : 0;
    };
    const start = () => {
      sliding++;
      if (!raf) raf = requestAnimationFrame(frame);
    };
    const end = () => {
      sliding = Math.max(0, sliding - 1);
      measure();
    };
    el.addEventListener("transitionstart", start);
    el.addEventListener("transitionend", end);
    el.addEventListener("transitioncancel", end);
    window.addEventListener("resize", measure);
    const ro = typeof ResizeObserver !== "undefined" ? new ResizeObserver(measure) : undefined;
    ro?.observe(el);
    // The sidebar's width animates and the bars above the page come and go:
    // the surface's box moves without its own size changing.
    const parent = el.parentElement;
    if (parent && ro) ro.observe(parent);
    return () => {
      el.removeEventListener("transitionstart", start);
      el.removeEventListener("transitionend", end);
      el.removeEventListener("transitioncancel", end);
      window.removeEventListener("resize", measure);
      ro?.disconnect();
      if (raf) cancelAnimationFrame(raf);
    };
  }, [ref]);
  return bounds;
}
