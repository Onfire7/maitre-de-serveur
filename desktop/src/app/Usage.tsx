// Usage graphs (decision 210): a Node's on its card on the Nodes tab and a
// Service's in its details, over the range picked in the page header. Each
// asks the Node itself; one from before keeps no history, so a Node falls
// back on the CPU and memory this Console has seen (209) and a Service
// says to update its Node.
import { useSyncExternalStore } from "react";
import { formatBytes } from "@/lib/format";
import type { Node } from "@/gen/maitre/v1/fleet_pb";
import type { Target } from "@/transport";
import type { FleetService, FleetSnapshot } from "@/fleet/useFleet";
import { targetFor } from "@/fleet/actions";
import { onSamples, samples } from "@/fleet/history";
import { hasReadings, NO_HISTORY, pointsOf, useMetricsHistory, useRange, type Point } from "@/fleet/metrics";
import { HistoryChart } from "./HistoryChart";

const known = (f: (v: number) => string) => (v: number) => (Number.isFinite(v) ? f(v) : "—");
export const fmtPercent = known((v) => `${v < 10 ? v.toFixed(1) : v.toFixed(0)}%`);
export const fmtRate = known((v) => `${formatBytes(Math.max(0, v))}/s`);
const fmtBytes = known((v) => formatBytes(Math.max(0, v)));
const fmtLoad = known((v) => (v >= 10 ? v.toFixed(0) : v.toFixed(2)));
const fmtTemp = known((v) => `${v.toFixed(0)} °C`);

const GRID = "grid gap-x-6 gap-y-5 sm:grid-cols-2 xl:grid-cols-3";

export function NodeUsage({ node, target, snap }: { node: Node; target: Target | undefined; snap: FleetSnapshot }) {
  const range = useRange();
  const q = useMetricsHistory(node.nodeId, target, [], range);
  const seen = useSyncExternalStore(onSamples, () => samples(node.nodeId));
  const dim = q.isPlaceholderData;

  if (q.data && !q.data.supported) {
    // What this Console has seen since it started, over its own span.
    const pts = (k: "cpu" | "memory"): Point[] => seen.map((s) => ({ t: s.t, v: s[k], max: s[k] }));
    const f = seen[0]?.t ?? Date.now();
    const t = seen[seen.length - 1]?.t ?? f;
    return (
      <div className="space-y-3">
        <p className="text-xs text-muted-foreground">{NO_HISTORY} Meanwhile, what this Console has seen since it started:</p>
        <div className={GRID}>
          <HistoryChart title="CPU" percent series={[{ label: "CPU", points: pts("cpu"), color: "chart-1" }]} format={fmtPercent} from={f} to={t} range="1h" empty="Gathering samples…" />
          <HistoryChart title="Memory" percent series={[{ label: "Memory", points: pts("memory"), color: "chart-1" }]} format={fmtPercent} from={f} to={t} range="1h" empty="Gathering samples…" />
        </div>
      </div>
    );
  }
  if (!target) return <p className="text-xs text-muted-foreground">Not answering; its history shows when it is back.</p>;
  if (!q.data) return <p className="text-xs text-muted-foreground">{q.error ? `Its history could not be read: ${(q.error as Error).message}` : "Reading its history…"}</p>;

  const s = q.data.series;
  const one = (name: string) => pointsOf(s.get(name));
  const temp = one("node/temperature");
  const drives = [...s.keys()]
    .filter((k) => k.startsWith("drive/"))
    .map((k) => ({ key: k, name: snap.drives.find((d) => d.nodeId === node.nodeId && d.driveId === k.slice(6))?.name || k.slice(6) }))
    .sort((a, b) => a.name.localeCompare(b.name));
  const { from, to } = q.data;
  const common = { from, to, range: q.data.range, dim };
  return (
    <div className={GRID}>
      <HistoryChart title="CPU" percent series={[{ label: "CPU", points: one("node/cpu"), color: "chart-1" }]} format={fmtPercent} {...common} />
      <HistoryChart title="Memory" percent series={[{ label: "Memory", points: one("node/memory"), color: "chart-1" }]} format={fmtPercent} {...common} />
      <HistoryChart
        title="Network"
        series={[
          { label: "In", points: one("node/net_rx"), color: "chart-1" },
          { label: "Out", points: one("node/net_tx"), color: "chart-2" },
        ]}
        format={fmtRate}
        {...common}
      />
      <HistoryChart title="Load" series={[{ label: "Load", points: one("node/load"), color: "chart-1" }]} format={fmtLoad} {...common} />
      {hasReadings(temp) && <HistoryChart title="Temperature" series={[{ label: "Temperature", points: temp, color: "chart-1" }]} format={fmtTemp} {...common} />}
      {s.has("node/pool") && <HistoryChart title="Pool used" percent series={[{ label: "Pool", points: one("node/pool"), color: "chart-1" }]} format={fmtPercent} {...common} />}
      {drives.map((d) => (
        <HistoryChart key={d.key} title={`${d.name} used`} percent series={[{ label: d.name, points: one(d.key), color: "chart-1" }]} format={fmtPercent} {...common} />
      ))}
    </div>
  );
}

export function ServiceUsage({ service: sv, snap }: { service: FleetService; snap: FleetSnapshot | undefined }) {
  const range = useRange();
  const name = sv.spec.name;
  const q = useMetricsHistory(sv.spec.nodeId, targetFor(snap, [], sv.spec.nodeId), [`service/${name}/`], range);
  if (q.data && !q.data.supported) return <p className="text-muted-foreground">{NO_HISTORY.replace("This Node's", `${sv.node?.name ?? "Its Node"}'s`)}</p>;
  if (!q.data) return <p className="text-muted-foreground">{q.error ? `Its history could not be read: ${(q.error as Error).message}` : "Reading its history…"}</p>;
  const h = q.data;
  const one = (k: string) => pointsOf(h.series.get(`service/${name}/${k}`));
  const common = { from: h.from, to: h.to, range: h.range, dim: q.isPlaceholderData };
  return (
    <div className="grid gap-x-6 gap-y-5 sm:grid-cols-2">
      <HistoryChart title="CPU, of all the Node's cores" percent series={[{ label: "CPU", points: one("cpu"), color: "chart-1" }]} format={fmtPercent} {...common} />
      <HistoryChart title="Memory" series={[{ label: "Memory", points: one("memory"), color: "chart-1" }]} format={fmtBytes} {...common} />
      <HistoryChart
        title="Disk"
        series={[
          { label: "Read", points: one("disk_read"), color: "chart-1" },
          { label: "Write", points: one("disk_write"), color: "chart-2" },
        ]}
        format={fmtRate}
        {...common}
      />
    </div>
  );
}
