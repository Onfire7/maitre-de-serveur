// How long a Node's updates take (decision 214): each Node keeps its last
// updates, start and end, in its own record.
import { UpdateRun_Outcome, type Node } from "@/gen/maitre/v1/fleet_pb";

/** How long its updates that reached the new image took, oldest first, in ms. */
export function updateDurations(node: Node | undefined): number[] {
  return (node?.updates ?? []).filter((r) => r.outcome === UpdateRun_Outcome.UPDATED && r.endedUnixMs > r.startedUnixMs).map((r) => Number(r.endedUnixMs - r.startedUnixMs));
}

/** The average of its last five updates that reached the new image; undefined before the first. */
export function averageUpdateMs(node: Node | undefined): number | undefined {
  const d = updateDurations(node).slice(-5);
  return d.length ? d.reduce((a, b) => a + b, 0) / d.length : undefined;
}

/** "45 s", "4 min", "1 h 5 min". */
export function formatDuration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `${s} s`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min`;
  return `${Math.floor(m / 60)} h${m % 60 ? ` ${m % 60} min` : ""}`;
}

/** "for 3 min · usually about 5 min", for a Node updating since `since`. */
export function updatingLine(node: Node | undefined, since: number | undefined, now: number): string {
  const parts: string[] = [];
  if (since) parts.push(`for ${formatDuration(now - since)}`);
  const avg = averageUpdateMs(node);
  if (avg !== undefined) parts.push(`usually about ${formatDuration(avg)}`);
  return parts.join(" · ");
}
