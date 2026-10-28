// The demo Nodes' stored metrics (decision 210): made up on asking, the
// same for the same time so a refresh does not reshuffle a graph, at the
// levels a Node keeps (10 s for 6 h, 1 min for 7 days, 15 min for 90 days,
// 1 h for 2 years). Each Node has kept them for 120 days, and Basalt was
// off for a while before its last start, so the graphs show gaps.
import type { DemoFleet, DemoNode } from "./fleet";

const MIN = 60_000;
const DAY = 24 * 60 * MIN;
const LEVELS = [
  { step: 10_000, keep: 6 * 60 * MIN },
  { step: MIN, keep: 7 * DAY },
  { step: 15 * MIN, keep: 90 * DAY },
  { step: 60 * MIN, keep: 730 * DAY },
];
const KEPT = 120 * DAY;

/** 0..1 from a name and a number, the same every time. */
function noise(name: string, n: number) {
  let h = 2166136261 ^ n;
  for (let i = 0; i < name.length; i++) h = Math.imul(h ^ name.charCodeAt(i), 16777619);
  h = Math.imul(h ^ (h >>> 15), 2246822507);
  return ((h ^ (h >>> 13)) >>> 0) / 4294967296;
}

/** 0..1 over the day, highest in the evening. */
const daily = (t: number) => 0.5 + 0.5 * Math.sin(((t % DAY) / DAY) * 2 * Math.PI - Math.PI / 1.4);

export function demoHistory(f: DemoFleet, me: DemoNode, input: { series?: string[]; fromUnixMs?: bigint; toUnixMs?: bigint; points?: number }) {
  const now = f.now;
  const to = Number(input.toUnixMs ?? 0n) || now;
  const from = Math.min(to - MIN, Number(input.fromUnixMs ?? 0n));
  const points = input.points || 300;
  const level = LEVELS.find((l) => now - from <= l.keep) ?? LEVELS[LEVELS.length - 1];
  const step = Math.ceil((to - from) / points / level.step) * level.step;
  const start = Math.floor(from / step) * step;
  const count = Math.ceil((to - start) / step);

  const m = me.metrics;
  const own = f.services.filter((s) => s.spec.nodeId === me.nodeId).map((s) => s.spec.name);
  const drives = f.drives.filter((d) => d.nodeId === me.nodeId && d.adopted);
  const all = [
    "node/cpu",
    "node/memory",
    "node/load",
    "node/temperature",
    "node/net_rx",
    "node/net_tx",
    "node/pool",
    ...drives.map((d) => `drive/${d.driveId}`),
    ...own.flatMap((n) => ["cpu", "memory", "disk_read", "disk_write"].map((k) => `service/${n}/${k}`)),
  ];
  const asked = input.series?.length ? input.series : ["node/", "drive/"];
  const names = all.filter((n) => asked.some((a) => (a.endsWith("/") ? n.startsWith(a) : n === a)));

  // Off: before it kept anything, and Basalt for 40 minutes before it last started.
  const off = (t: number) => t < now - KEPT || (me.letter === "B" && t > me.bootedUnixMs - 40 * MIN && t < me.bootedUnixMs);
  const pct = (used: number, total: number) => (total > 0 ? (used / total) * 100 : 0);

  const value = (name: string, t: number, k: number): [number, number] => {
    const r = noise(name, k);
    const d = daily(t);
    const spike = r > 0.97 ? 1.3 + r : 1;
    const ageDays = (now - t) / DAY;
    const [, , svc, what] = name.match(/^(service)\/([^/]+)\/(.+)$/) ?? [];
    const seed = noise(svc ?? name, 1);
    let avg: number;
    let peak = 1.1 + r * 0.6;
    if (name === "node/cpu") avg = Math.min(100, (m.cpuPercent * 0.7 + 20 * d) * (0.85 + 0.3 * r) * spike);
    else if (name === "node/memory") {
      avg = pct(Number(m.memoryUsedBytes), Number(m.memoryTotalBytes)) + 6 * d - 3 + 2 * r;
      peak = 1.02 + r * 0.05;
    } else if (name === "node/load") avg = (m.load1m * 0.5 + 1.5 * d) * (0.8 + 0.4 * r) * spike;
    else if (name === "node/temperature") {
      avg = m.temperatureC - 6 + 10 * d + 2 * r;
      peak = 1.03 + r * 0.05;
    } else if (name === "node/net_rx") avg = (0.2e6 + 6e6 * d * d) * (0.7 + 0.6 * r) * spike;
    else if (name === "node/net_tx") avg = (0.1e6 + 2.5e6 * d * d) * (0.7 + 0.6 * r) * spike;
    else if (name === "node/pool" || name.startsWith("drive/")) {
      const dr = drives.find((x) => `drive/${x.driveId}` === name);
      const cur = dr ? pct(Number(dr.sizeBytes - dr.freeBytes), Number(dr.sizeBytes)) : pct(Number(m.poolUsedBytes), Number(m.poolTotalBytes));
      avg = Math.max(1, cur - ageDays * 0.08 + 0.2 * r);
      peak = 1;
    } else if (what === "cpu") avg = (0.3 + 6 * seed * d) * (0.5 + r) * spike;
    else if (what === "memory") {
      avg = (150 + 1800 * seed) * 1e6 * (0.85 + 0.15 * d + 0.05 * r);
      peak = 1.02 + r * 0.08;
    } else if (what === "disk_read") avg = (20e3 + 4e6 * seed * d) * (0.3 + 1.4 * r) * spike;
    else avg = (10e3 + 1.5e6 * seed * d) * (0.3 + 1.4 * r) * spike;
    avg = Math.max(0, avg);
    return [avg, name.endsWith("cpu") || name === "node/memory" || name === "node/pool" || name.startsWith("drive/") ? Math.min(100, avg * peak) : avg * peak];
  };

  return {
    series: names.map((name) => {
      const avg: number[] = [];
      const max: number[] = [];
      for (let i = 0; i < count; i++) {
        const t = start + i * step;
        if (off(t) || t > now) {
          avg.push(NaN);
          max.push(NaN);
          continue;
        }
        const [a, x] = value(name, t, Math.floor(t / step));
        avg.push(a);
        max.push(x);
      }
      return { name, startUnixMs: BigInt(start), stepMs: BigInt(step), avg, max };
    }),
  };
}
