// The Fleet's Overview (decisions 148, 209, 210): a dashboard. How the Fleet
// is doing in a line, a row of headline numbers (Nodes, Services, storage,
// updates, reach from away), each Node's CPU over the picked range, every
// Service at a glance, what needs attention and how full each Space is.
// Everything opens the tab that manages it.
import { useMemo, useSyncExternalStore } from "react";
import { ArrowUpCircle, CheckCircle2, CircleAlert, Globe, HardDrive, Info, Server, Boxes } from "lucide-react";
import { cn } from "@/lib/utils";
import { formatBytes } from "@/lib/format";
import type { FleetService, FleetSnapshot } from "@/fleet/useFleet";
import { nodeDown, nodeWord } from "@/fleet/useFleet";
import { onSamples, samples, type Sample } from "@/fleet/history";
import { pointsOf, rangeOf, useMetricsHistory, useRange } from "@/fleet/metrics";
import { targetFor } from "@/fleet/actions";
import { HistoryChart } from "./HistoryChart";
import { fmtPercent } from "./Usage";
import { spacesOf } from "@/spaces";
import type { ConsoleState } from "@/state";
import type { DeviceInfo } from "@/state";
import { NodeMark, ServiceIcon, StatusWord } from "./status";
import { colorOf } from "./StorageTab";
import { useRoute, type FleetTab } from "./routes";
import type { Notice } from "./notices";

export function OverviewTab({ snap, device, notices, state, services, now, onPromote }: { snap: FleetSnapshot | undefined; device: DeviceInfo; notices: Notice[]; state: ConsoleState; services: FleetService[]; now: number; onPromote: () => void }) {
  const { go } = useRoute();
  if (!snap) return <p className="text-sm text-muted-foreground">{state.nodes.length === 0 ? "No Node answers yet." : "Reaching the Fleet…"}</p>;

  const nodes = [...snap.nodes].sort((a, b) => a.name.localeCompare(b.name));
  const words = new Map(nodes.map((n) => [n.nodeId, nodeWord(snap.status.get(n.nodeId), now)]));
  const healthy = nodes.filter((n) => words.get(n.nodeId) === "Healthy").length;
  const running = services.filter((s) => s.word === "Online" || s.word === "Starting").length;
  const stopped = services.filter((s) => s.word === "Stopped").length;
  const troubled = services.filter((s) => s.word === "Attention" || s.nodeUnreachable).length;
  const usage = (d: { nodeId: string; driveId: string }) => snap.status.get(d.nodeId)?.metrics?.drives.find((u) => u.driveId === d.driveId);
  const reported = snap.drives.map(usage).filter((u) => !!u);
  const total = reported.reduce((a, u) => a + Number(u.totalBytes), 0);
  const used = reported.reduce((a, u) => a + Number(u.usedBytes), 0);
  const updates = nodes.filter((n) => snap.status.get(n.nodeId)?.updateAvailable).length;
  const updating = nodes.filter((n) => words.get(n.nodeId) === "Updating").length;
  const tailscale = snap.records.some((r) => r.kind === "tailscale" && !r.deleted);
  const away = services.filter((s) => s.spec.exposure && s.spec.exposure !== "private").length;
  const attention = notices.filter((n) => n.severity === "attention").length;
  const anchor = nodes.find((n) => n.nodeId === snap.anchor.nodeId)?.name;
  const starting = snap.nodes.length === 0 || snap.spaces.length === 0 || snap.specs.length === 0;

  return (
    <div className="max-w-6xl space-y-6">
      <div className="flex items-start gap-3">
        {attention > 0 || troubled > 0 ? <CircleAlert className="mt-0.5 size-6 shrink-0 text-status-attention" /> : <CheckCircle2 className="mt-0.5 size-6 shrink-0 text-status-online" />}
        <div>
          <div className="text-lg font-semibold">{attention > 0 ? `${attention} ${attention === 1 ? "thing needs" : "things need"} your attention` : troubled > 0 ? "Running, with Services to look at" : "Everything is running"}</div>
          <div className="text-sm text-muted-foreground">
            {nodes.length} {nodes.length === 1 ? "Node" : "Nodes"}, {services.length} {services.length === 1 ? "Service" : "Services"}, {snap.spaces.length} {snap.spaces.length === 1 ? "Storage Space" : "Storage Spaces"}
            {anchor ? `, reached through ${anchor}` : ""}. This device is <span className="text-foreground">{device.name}</span>.
          </div>
        </div>
      </div>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
        <Tile icon={<Server />} label="Nodes healthy" value={`${healthy} of ${nodes.length}`} sub={nodes.length - healthy > 0 ? summarize(nodes.map((n) => words.get(n.nodeId)!).filter((w) => w !== "Healthy")) : "All answering"} tone={healthy < nodes.length ? "attention" : undefined} onClick={() => go({ name: "fleet", tab: "nodes" })} />
        <Tile icon={<Boxes />} label="Services running" value={`${running} of ${services.length}`} sub={[stopped && `${stopped} stopped`, troubled && `${troubled} ${troubled === 1 ? "needs" : "need"} a look`].filter(Boolean).join(" · ") || (services.length ? "All running" : "None installed yet")} tone={troubled ? "attention" : undefined} onClick={() => go({ name: "fleet", tab: "services" })} />
        <Tile icon={<HardDrive />} label="Storage free" value={total ? formatBytes(total - used) : "—"} sub={total ? `of ${formatBytes(total)} on ${reported.length} ${reported.length === 1 ? "Drive" : "Drives"}` : "No Drive has reported yet"} meter={total ? used / total : undefined} onClick={() => go({ name: "fleet", tab: "storage" })} />
        <Tile icon={<ArrowUpCircle />} label="Maître Server" value={updating ? "Updating" : updates ? `${updates} to update` : "Up to date"} sub={snap.policy?.automatic ? `Updates automatically${snap.policy.paused ? ", paused" : ""}` : "Updates when you choose"} tone={updates ? "attention" : undefined} onClick={() => go({ name: "fleet", tab: "updates" })} />
        <Tile icon={<Globe />} label="Away from home" value={tailscale ? "Connected" : "Home only"} sub={tailscale ? `${away} ${away === 1 ? "Service" : "Services"} reachable` : "Tailscale is not set up"} onClick={() => go({ name: "fleet", tab: "remote" })} />
      </div>

      <div className="grid gap-6 lg:grid-cols-3">
        <div className="space-y-6 lg:col-span-2">
          {starting && <GettingStarted snap={snap} />}
          <section>
            <SectionHead title="Nodes" onMore={() => go({ name: "fleet", tab: "nodes" })} />
            <div className="grid gap-3 sm:grid-cols-2">
              {nodes.map((n) => (
                <NodeCard key={n.nodeId} snap={snap} nodeId={n.nodeId} name={n.name} word={words.get(n.nodeId)!} services={services.filter((s) => s.spec.nodeId === n.nodeId)} onOpen={() => go({ name: "fleet", tab: "nodes", nodeId: n.nodeId })} />
              ))}
            </div>
          </section>
          <section>
            <SectionHead title="Services" onMore={() => go({ name: "fleet", tab: "services" })} />
            {services.length === 0 ? (
              <p className="text-sm text-muted-foreground">None yet. Add one from the Catalog.</p>
            ) : (
              <ul className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
                {[...services]
                  .sort((a, b) => order(a) - order(b) || a.spec.name.localeCompare(b.spec.name))
                  .map((s) => (
                    <li key={s.spec.name}>
                      <button onClick={() => go({ name: "fleet", tab: "services", open: { service: s.spec.name } })} className="flex w-full items-center gap-2.5 rounded-lg border bg-card px-3 py-2 text-left hover:bg-accent/50">
                        <ServiceIcon catalogId={s.spec.catalogId} word={s.word} size={28} />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate text-sm font-medium">{s.spec.name}</span>
                          <span className="block truncate text-xs text-muted-foreground">{s.node?.name ?? "Node unknown"}</span>
                        </span>
                        <StatusWord word={s.nodeUnreachable ? "Attention" : s.word} className="text-xs" />
                      </button>
                    </li>
                  ))}
              </ul>
            )}
          </section>
        </div>

        <div className="space-y-6">
          <section>
            <SectionHead title="Needs your attention" />
            {notices.length === 0 ? (
              <p className="flex items-center gap-2 rounded-lg border p-3 text-sm text-muted-foreground">
                <CheckCircle2 className="size-4 text-status-online" /> Nothing. Everything is as it should be.
              </p>
            ) : (
              <ul className="space-y-2">
                {notices.map((n) => (
                  <li key={n.id}>
                    <button onClick={() => (n.id === "scope" ? onPromote() : go({ name: "fleet", tab: n.tab, nodeId: n.nodeId }))} className={cn("flex w-full items-start gap-3 rounded-lg border p-3 text-left hover:bg-accent/50", n.severity === "attention" && "border-status-attention/60 bg-status-attention/10")}>
                      {n.severity === "attention" ? <CircleAlert className="mt-0.5 size-4 shrink-0 text-status-attention" /> : <Info className="mt-0.5 size-4 shrink-0 text-status-starting" />}
                      <span>
                        <span className="block text-sm font-medium">{n.title}</span>
                        <span className="block text-xs text-muted-foreground">{n.detail}</span>
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </section>
          <section>
            <SectionHead title="Storage Spaces" onMore={() => go({ name: "fleet", tab: "storage" })} />
            <SpaceMeters snap={snap} />
          </section>
        </div>
      </div>
    </div>
  );
}

/** Services needing a look first, then running, then stopped. */
const order = (s: FleetService) => (s.word === "Attention" || s.nodeUnreachable ? 0 : s.word === "Starting" ? 1 : s.word === "Online" ? 2 : 3);

function summarize(words: string[]) {
  const counts = new Map<string, number>();
  for (const w of words) counts.set(w, (counts.get(w) ?? 0) + 1);
  return [...counts].map(([w, n]) => `${n} ${w.toLowerCase()}`).join(" · ");
}

function SectionHead({ title, onMore }: { title: string; onMore?: () => void }) {
  return (
    <div className="mb-2 flex items-baseline justify-between">
      <h2 className="text-base font-semibold">{title}</h2>
      {onMore && (
        <button className="text-xs text-muted-foreground hover:text-foreground hover:underline" onClick={onMore}>
          Open {title}
        </button>
      )}
    </div>
  );
}

/** A headline number (the stat tile contract: label, value, a line under it, optional meter). */
function Tile({ icon, label, value, sub, meter, tone, onClick }: { icon: React.ReactNode; label: string; value: string; sub: string; meter?: number; tone?: "attention"; onClick: () => void }) {
  return (
    <button onClick={onClick} className={cn("rounded-xl border bg-card p-3 text-left hover:bg-accent/40", tone === "attention" && "border-status-attention/60")}>
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground [&_svg]:size-3.5">
        {icon}
        {label}
      </div>
      <div className="mt-1 text-xl font-semibold">{value}</div>
      {meter !== undefined && <Meter value={meter} className="my-1.5" label={`${Math.round(meter * 100)}% used`} />}
      <div className="truncate text-xs text-muted-foreground">{sub}</div>
    </button>
  );
}

/**
 * How full something is: the fill carries severity (blue, then amber past
 * 85%, red past 95%) over a lighter step of the blue (dataviz meter).
 */
function Meter({ value, className, label }: { value: number; className?: string; label: string }) {
  const v = Math.min(1, Math.max(0, value));
  return (
    <div className={cn("h-1.5 overflow-hidden rounded-full bg-chart-track", className)} role="meter" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(v * 100)} aria-label={label} title={label}>
      <div className={cn("h-full rounded-full transition-[width] duration-700", v > 0.95 ? "bg-destructive" : v > 0.85 ? "bg-status-attention" : "bg-chart-1")} style={{ width: `${v * 100}%` }} />
    </div>
  );
}

function NodeCard({ snap, nodeId, name, word, services, onOpen }: { snap: FleetSnapshot; nodeId: string; name: string; word: ReturnType<typeof nodeWord>; services: FleetService[]; onOpen: () => void }) {
  const st = snap.status.get(nodeId);
  const m = st?.metrics;
  const history = useSyncExternalStore(onSamples, () => samples(nodeId));
  // CPU over the picked range from the Node itself (210); a Node from
  // before, or one not answered yet, shows what this Console has seen (209).
  const range = useRange();
  const q = useMetricsHistory(nodeId, nodeDown(word) ? undefined : targetFor(snap, [], nodeId), ["node/cpu"], range);
  const h = q.data?.supported ? q.data : undefined;
  const seen = useMemo(() => history.map((p) => ({ t: p.t, v: p.cpu, max: p.cpu })), [history]);
  const memory = m && m.memoryTotalBytes > 0n ? Number((m.memoryUsedBytes * 1000n) / m.memoryTotalBytes) / 1000 : undefined;
  const up = services.filter((s) => s.word === "Online" || s.word === "Starting").length;
  return (
    <div className="rounded-xl border bg-card p-3">
      <button onClick={onOpen} className="flex w-full items-center gap-2 text-left">
        <NodeMark word={word} size={24} />
        <span className="font-semibold hover:underline">{name}</span>
        <span className={cn("text-xs", word === "Healthy" ? "text-status-online" : nodeDown(word) ? "text-muted-foreground" : "text-status-attention")}>{word}</span>
        <span className="ml-auto text-xs text-muted-foreground">{st?.version ? `v${st.version.replace(/^v/, "")}` : ""}</span>
      </button>
      {!m || nodeDown(word) ? (
        <p className="mt-3 text-sm text-muted-foreground">{nodeDown(word) ? "Not answering; its numbers show when it is back." : "No numbers yet."}</p>
      ) : (
        <>
          <div className="mt-3 flex items-end justify-between text-xs text-muted-foreground">
            <span>CPU, last {h ? rangeOf(h.range).span : spanOf(history)}</span>
            <span className="text-sm font-semibold text-foreground">{m.cpuPercent.toFixed(0)}%</span>
          </div>
          {h ? (
            <HistoryChart compact title={`${name} CPU`} percent series={[{ label: "CPU", points: pointsOf(h.series.get("node/cpu")), color: "chart-1" }]} format={fmtPercent} from={h.from} to={h.to} range={h.range} dim={q.isPlaceholderData} empty="No readings in this range" />
          ) : (
            <HistoryChart compact title={`${name} CPU`} percent series={[{ label: "CPU", points: seen, color: "chart-1" }]} format={fmtPercent} from={seen[0]?.t ?? 0} to={seen[seen.length - 1]?.t ?? 1} range="1h" empty="Gathering samples…" />
          )}
          <div className="mt-2 grid grid-cols-2 gap-x-4 gap-y-1.5 text-xs">
            <div>
              <div className="flex justify-between text-muted-foreground">
                Memory <span className="text-foreground">{formatBytes(m.memoryUsedBytes)} of {formatBytes(m.memoryTotalBytes)}</span>
              </div>
              {memory !== undefined && <Meter value={memory} className="mt-1" label={`${Math.round(memory * 100)}% of memory used`} />}
            </div>
            <div>
              <div className="flex justify-between text-muted-foreground">
                Storage <span className="text-foreground">{m.poolTotalBytes ? `${formatBytes(m.poolTotalBytes - m.poolUsedBytes)} free` : "—"}</span>
              </div>
              {m.poolTotalBytes > 0n && <Meter value={Number(m.poolUsedBytes) / Number(m.poolTotalBytes)} className="mt-1" label={`${Math.round((Number(m.poolUsedBytes) / Number(m.poolTotalBytes)) * 100)}% of its Pools used`} />}
            </div>
          </div>
          <div className="mt-2 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
            <span>
              {up} of {services.length} {services.length === 1 ? "Service" : "Services"} up
            </span>
            <span>
              ↓ {formatBytes(m.netRxBytesPerSec)}/s ↑ {formatBytes(m.netTxBytesPerSec)}/s
            </span>
            {m.temperatureC > 0 && <span className={cn(m.temperatureC >= 80 && "text-status-attention")}>{m.temperatureC.toFixed(0)} °C</span>}
            <span>load {m.load1m.toFixed(2)}</span>
          </div>
        </>
      )}
    </div>
  );
}

function spanOf(points: Sample[]) {
  if (points.length < 2) return "moments";
  const min = Math.round((points[points.length - 1].t - points[0].t) / 60000);
  return min < 1 ? "minute" : `${min} min`;
}

/** Each Space: how full its Drives are, from their gossiped usage. */
function SpaceMeters({ snap }: { snap: FleetSnapshot }) {
  const spaces = [...snap.spaces].sort((a, b) => b.priority - a.priority);
  if (spaces.length === 0) return <p className="text-sm text-muted-foreground">No Space yet.</p>;
  return (
    <ul className="space-y-3 rounded-xl border bg-card p-3">
      {[...spaces]
        .sort((a, b) => a.name.localeCompare(b.name))
        .map((sp) => {
          const ds = snap.drives.filter((d) => spacesOf(d, spaces).includes(sp.spaceId));
          const us = ds.map((d) => snap.status.get(d.nodeId)?.metrics?.drives.find((u) => u.driveId === d.driveId)).filter((u) => !!u);
          const total = us.reduce((a, u) => a + Number(u.totalBytes), 0);
          const used = us.reduce((a, u) => a + Number(u.usedBytes), 0);
          return (
            <li key={sp.spaceId} className="text-sm">
              <div className="flex items-center gap-2">
                <span className="size-2.5 shrink-0 rounded-full" style={{ background: colorOf(sp, spaces) }} />
                <span className="font-medium">{sp.name}</span>
                <span className="ml-auto text-xs text-muted-foreground">{total ? `${formatBytes(total - used)} free of ${formatBytes(total)}` : ds.length ? "not reported yet" : "no Drives"}</span>
              </div>
              {total > 0 && <Meter value={used / total} className="mt-1.5" label={`${sp.name}: ${Math.round((used / total) * 100)}% used`} />}
            </li>
          );
        })}
    </ul>
  );
}

function GettingStarted({ snap }: { snap: FleetSnapshot }) {
  const { go } = useRoute();
  return (
    <section>
      <SectionHead title="Getting started" />
      <ol className="space-y-1.5">
        {(
          [
            [snap.nodes.length > 0, "Set up a Node", "A machine booted from the Installer, on the Nodes tab.", "nodes"],
            [snap.spaces.length > 0, "Create a Storage Space", "Where Service data goes: pick the Drives, on the Storage tab.", "storage"],
            [snap.specs.length > 0, "Install a Service", "From the Catalog, with Add a Service in the sidebar.", undefined],
          ] as [boolean, string, string, FleetTab | undefined][]
        ).map(([done, title, detail, tab]) => (
          <li key={title}>
            <button disabled={done} onClick={() => (tab ? go({ name: "fleet", tab }) : go({ name: "catalog" }))} className={cn("flex w-full items-start gap-3 rounded-lg border p-3 text-left", done ? "opacity-60" : "hover:bg-accent/50")}>
              <span className={cn("mt-0.5 inline-flex size-4 shrink-0 items-center justify-center rounded-full border text-[10px]", done && "border-status-online bg-status-online text-black")}>{done ? "✓" : ""}</span>
              <span>
                <span className={cn("block text-sm font-medium", done && "line-through")}>{title}</span>
                <span className="block text-xs text-muted-foreground">{detail}</span>
              </span>
            </button>
          </li>
        ))}
      </ol>
    </section>
  );
}
