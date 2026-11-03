// How the Service page shares the window with the Console's own chrome
// (decision 293). The page is on its way to being a native web view that
// always paints above the Console's HTML, so nothing of the Console may
// float over it: a flyout is a full-height panel beside the sidebar and the
// page slides right by the panel's width while one is open, its size
// untouched, its far side past the window's edge; a modal covers the page
// with a blurred stand-in made from a few of its colours, taken before the
// page is hidden, and the page comes back when the modal closes.
//
// This store is the one place both the HTML frame layer (today) and the
// native views (next) read from: the offset to sit at, and whether to be
// hidden under a modal.
import { useSyncExternalStore } from "react";

/** A grid of CSS colours, rows of columns, as sampled from the page. */
export type ColorGrid = string[][];

export interface PageLayout {
  /** How far right the page sits, in px: the open flyout's width, else 0. */
  offset: number;
  /** A modal is open: the page is hidden and its stand-in drawn. */
  covered: boolean;
  /** The colours the stand-in is drawn from; the last sample, or the fallback. */
  colors: ColorGrid;
}

/** Samples the shown page's colours. Set by whoever can see the page:
 * the demo reads its stand-in's DOM, the native view will take a thumbnail.
 * Rejects or resolves empty when it cannot. */
export type Sampler = () => Promise<ColorGrid>;

let panel = 0;
let modals = 0;
let covered = false;
let colors: ColorGrid = [];
let sampler: Sampler | undefined;
let snapshot: PageLayout = { offset: 0, covered: false, colors };
const listeners = new Set<() => void>();
/** The latest cover request; an older sample arriving late is ignored. */
let covering = 0;

function emit() {
  snapshot = { offset: panel, covered, colors };
  listeners.forEach((l) => l());
}

/** A flyout opened beside the sidebar, this wide. */
export function openPanel(width: number) {
  if (panel === width) return;
  panel = width;
  emit();
}

/** The flyout closed. */
export function closePanel() {
  if (panel === 0) return;
  panel = 0;
  emit();
}

export function setSampler(s: Sampler | undefined) {
  sampler = s;
}

/** The stand-in's colours when nothing could be sampled: the theme's own surface. */
export function fallbackColors(dark = typeof document !== "undefined" && document.documentElement.classList.contains("dark")): ColorGrid {
  const a = dark ? "#1c1c1e" : "#f4f4f5";
  const b = dark ? "#2a2a2e" : "#ffffff";
  return [
    [a, b, b, a],
    [b, a, a, b],
    [a, b, b, a],
  ];
}

/** A modal opened: sample the page, then hide it behind its stand-in.
 * Several modals at once count as one. */
export function modalOpened() {
  modals++;
  if (modals > 1) return;
  const id = ++covering;
  const done = (grid: ColorGrid) => {
    if (id !== covering || modals === 0) return;
    colors = grid.length && grid.every((r) => r.length) ? grid : colors.length ? colors : fallbackColors();
    covered = true;
    emit();
  };
  if (!sampler) return done([]);
  sampler().then(done, () => done([]));
}

/** A modal closed: when the last one has, the page shows again. */
export function modalClosed() {
  if (modals === 0) return;
  modals--;
  if (modals > 0) return;
  covering++;
  covered = false;
  emit();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

export function usePageLayout(): PageLayout {
  return useSyncExternalStore(subscribe, () => snapshot);
}

export function pageLayout(): PageLayout {
  return snapshot;
}

/** Tests: start over. */
export function resetLayout() {
  panel = 0;
  modals = 0;
  covered = false;
  colors = [];
  sampler = undefined;
  covering++;
  emit();
}

/** Reads a grid of background colours off a DOM subtree: at each point of
 * a cols-by-rows grid over `root`, the first ancestor of the element there
 * with a background that is not transparent. The demo's stand-in page and
 * any same-origin document can be read this way; a cross-origin frame
 * cannot, which is what the native view's thumbnail is for. */
export function sampleElement(root: Element, cols = 4, rows = 3): ColorGrid {
  const doc = root.ownerDocument;
  if (typeof doc.elementFromPoint !== "function") return [];
  const box = root.getBoundingClientRect();
  if (box.width === 0 || box.height === 0) return [];
  const grid: ColorGrid = [];
  for (let r = 0; r < rows; r++) {
    const row: string[] = [];
    for (let c = 0; c < cols; c++) {
      const x = box.left + ((c + 0.5) / cols) * box.width;
      const y = box.top + ((r + 0.5) / rows) * box.height;
      row.push(backgroundAt(doc.elementFromPoint(x, y), root) ?? "transparent");
    }
    grid.push(row);
  }
  return grid;
}

function backgroundAt(el: Element | null, root: Element): string | undefined {
  for (let e: Element | null = el; e; e = e === root ? null : e.parentElement) {
    const bg = getComputedStyle(e).backgroundColor;
    if (bg && bg !== "transparent" && !/^rgba\(\s*\d+\s*,\s*\d+\s*,\s*\d+\s*,\s*0\s*\)$/.test(bg)) return bg;
  }
  return undefined;
}
