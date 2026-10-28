// Long-term metrics history (decision 210): each Node keeps its own and the
// Console asks it directly for the series a graph shows, over the range
// picked once per page. A Node from before answers Unimplemented; the
// graphs then fall back on what the Console has seen (history.ts) or say
// that Node keeps none yet.
import { useSyncExternalStore } from "react";
import { useQuery } from "@tanstack/react-query";
import { Code, ConnectError, createClient, type Transport } from "@connectrpc/connect";
import { NodeService, type MetricSeries } from "@/gen/maitre/v1/node_pb";
import { transportFor, type Target } from "@/transport";

export type RangeId = "1h" | "1d" | "7d" | "30d" | "1y";

export interface Range {
  id: RangeId;
  /** On the picker. */
  label: string;
  /** After "last", as in "CPU, last 7 days". */
  span: string;
  ms: number;
}

const HOUR = 3600_000;
const DAY = 24 * HOUR;

export const RANGES: Range[] = [
  { id: "1h", label: "1 hour", span: "hour", ms: HOUR },
  { id: "1d", label: "1 day", span: "day", ms: DAY },
  { id: "7d", label: "7 days", span: "7 days", ms: 7 * DAY },
  { id: "30d", label: "30 days", span: "30 days", ms: 30 * DAY },
  { id: "1y", label: "1 year", span: "year", ms: 365 * DAY },
];

export const rangeOf = (id: RangeId) => RANGES.find((r) => r.id === id)!;

/** About one point per pixel of a chart. */
export const CHART_POINTS = 300;

/** The request for a range ending now: the Node picks the level of detail. */
export function rangeQuery(id: RangeId, now: number, points = CHART_POINTS) {
  return { fromUnixMs: BigInt(now - rangeOf(id).ms), toUnixMs: 0n, points };
}

/** The last hour moves every 10 s (the finest level); longer ranges every minute. */
export function refetchMs(id: RangeId) {
  return id === "1h" ? 10_000 : 60_000;
}

// The picked range: one for the Console while it runs, so it stays when
// switching tabs (and pages that show graphs share it).
let picked: RangeId = "1h";
const listeners = new Set<() => void>();

export function setRange(id: RangeId) {
  if (id === picked) return;
  picked = id;
  listeners.forEach((f) => f());
}

export function useRange(): RangeId {
  return useSyncExternalStore(
    (f) => {
      listeners.add(f);
      return () => void listeners.delete(f);
    },
    () => picked,
  );
}

export interface Point {
  t: number;
  /** NaN where the Node has no reading (it was off). */
  v: number;
  max: number;
}

/** A series' points, each at the middle of its step. */
export function pointsOf(s: MetricSeries | undefined): Point[] {
  if (!s) return [];
  const start = Number(s.startUnixMs);
  const step = Number(s.stepMs);
  return s.avg.map((v, i) => ({ t: start + i * step + step / 2, v, max: s.max[i] ?? v }));
}

/** Whether any point has a reading other than zero (temperature on a machine without a sensor reads 0). */
export const hasReadings = (ps: Point[]) => ps.some((p) => Number.isFinite(p.v) && p.v !== 0);

/** The answer, with the range and times it covers (kept on screen while the next range loads). */
export type History = { supported: true; series: Map<string, MetricSeries>; range: RangeId; from: number; to: number } | { supported: false };

/** One GetMetricsHistory call, Unimplemented turned into "no history here". */
export async function readHistory(transport: Transport, series: string[], range: RangeId, now = Date.now()): Promise<History> {
  try {
    const res = await createClient(NodeService, transport).getMetricsHistory({ series, ...rangeQuery(range, now) }, { timeoutMs: 8000 });
    return { supported: true, series: new Map(res.series.map((s) => [s.name, s])), range, from: now - rangeOf(range).ms, to: now };
  } catch (e) {
    if (e instanceof ConnectError && e.code === Code.Unimplemented) return { supported: false };
    throw e;
  }
}

/**
 * The series asked for from one Node over the picked range, refreshed as
 * the range needs and keeping the last answer while the next loads.
 */
export function useMetricsHistory(nodeId: string | undefined, target: Target | undefined, series: string[], range: RangeId) {
  return useQuery({
    queryKey: ["metrics-history", nodeId, target?.addr, series.join(","), range],
    enabled: !!target,
    refetchInterval: refetchMs(range),
    retry: false,
    placeholderData: (prev) => prev,
    queryFn: () => readHistory(transportFor(target!), series, range),
  });
}

/**
 * Time on a chart: the time of day for the last hour, with the weekday
 * on the axis for the last day, and the date beyond (the date and time in
 * the tooltip).
 */
export function formatTime(t: number, range: RangeId, tooltip = false) {
  const d = new Date(t);
  const time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  if (range === "1h") return time;
  // A day ago and now are the same time of day: the axis says which day.
  if (range === "1d") return tooltip ? time : `${d.toLocaleDateString([], { weekday: "short" })} ${time}`;
  const date = d.toLocaleDateString([], range === "1y" ? { day: "numeric", month: "short", year: "numeric" } : { day: "numeric", month: "short" });
  return tooltip ? `${date}, ${time}` : date;
}

/**
 * An SVG path through the points, broken where y is NaN (no reading: the
 * Node was off) rather than drawn down to zero. A reading alone between
 * gaps is a zero-length segment, which the round cap shows as a dot.
 */
export function linePath(ps: { x: number; y: number }[]): string {
  let d = "";
  let open = false;
  ps.forEach((p, i) => {
    if (!Number.isFinite(p.y)) {
      open = false;
      return;
    }
    const at = `${p.x.toFixed(1)},${p.y.toFixed(1)}`;
    if (open) d += `L${at}`;
    else {
      d += `M${at}`;
      open = true;
      if (!Number.isFinite(ps[i + 1]?.y)) d += `L${at}`;
    }
  });
  return d;
}

/** The area between two lines (average and highest), one closed shape per unbroken run. */
export function bandPath(ps: { x: number; lo: number; hi: number }[]): string {
  const runs: (typeof ps)[] = [[]];
  for (const p of ps) {
    if (Number.isFinite(p.lo) && Number.isFinite(p.hi)) runs[runs.length - 1].push(p);
    else if (runs[runs.length - 1].length) runs.push([]);
  }
  const f = (n: number) => n.toFixed(1);
  return runs
    .filter((r) => r.length > 1)
    .map((r) => `M${r.map((p) => `${f(p.x)},${f(p.hi)}`).join("L")}L${[...r].reverse().map((p) => `${f(p.x)},${f(p.lo)}`).join("L")}Z`)
    .join("");
}

/** A round top for an axis: 1, 2 or 5 times a power of ten. */
export function niceMax(v: number) {
  if (!(v > 0)) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  return ([1, 2, 5, 10].find((m) => m * p >= v) ?? 10) * p;
}

export const NO_HISTORY = "This Node's Maître Server keeps no history yet; update it to see graphs.";
