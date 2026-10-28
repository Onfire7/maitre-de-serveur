// Recent metrics per Node (decision 209), kept by the Console while it runs:
// Nodes report only their latest sample, so the Overview's trend lines are
// built here from each new one, about ten minutes' worth.
import type { FleetSnapshot } from "./useFleet";

export interface Sample {
  t: number;
  cpu: number;
  memory: number;
}

const KEEP = 200;
const history = new Map<string, Sample[]>();
const listeners = new Set<() => void>();

/** Adds each Node's sample when it is new. */
export function recordMetrics(snap: FleetSnapshot | undefined) {
  if (!snap) return;
  let changed = false;
  for (const [id, st] of snap.status) {
    const m = st.metrics;
    const t = Number(st.metricsUnixMs);
    if (!m || !t) continue;
    const list = history.get(id) ?? [];
    const memory = m.memoryTotalBytes > 0n ? Number((m.memoryUsedBytes * 1000n) / m.memoryTotalBytes) / 10 : 0;
    const last = list[list.length - 1];
    if (last && last.t > t) continue;
    if (last && last.t === t && last.cpu === m.cpuPercent && last.memory === memory) continue;
    // A new array each time: the Overview reads it with useSyncExternalStore.
    // The same report time with new numbers replaces the last sample.
    const sample = { t, cpu: m.cpuPercent, memory };
    history.set(id, (last && last.t === t ? [...list.slice(0, -1), sample] : [...list, sample]).slice(-KEEP));
    changed = true;
  }
  if (changed) listeners.forEach((f) => f());
}

const none: Sample[] = [];

export function samples(nodeId: string): Sample[] {
  return history.get(nodeId) ?? none;
}

export function onSamples(f: () => void) {
  listeners.add(f);
  return () => void listeners.delete(f);
}
