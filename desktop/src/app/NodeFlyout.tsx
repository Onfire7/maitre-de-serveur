// The Node flyout: hovering a Node under the Fleet entry slides out a
// detailed status beside the sidebar, the same way a Service's menu does
// (decision 142): status, gauges, every Drive and how full it is, what it
// runs. Everything comes from the anchor's GetFleetStatus (ADR 0006), so it
// is there for every Node without calling each one.
import { motion } from "motion/react";
import { ArrowUpCircle, Cpu, HardDrive, MemoryStick, Thermometer, ArrowRight } from "lucide-react";
import { cn } from "@/lib/utils";
import { formatBytes, ago } from "@/lib/format";
import type { FleetService, FleetSnapshot } from "@/fleet/useFleet";
import { nodeWord, provisioningLine, STALE_MS } from "@/fleet/useFleet";
import { spacesOf } from "@/spaces";
import { NodeMark, ServiceIcon, StatusWord } from "./status";
import { colorOf } from "./StorageTab";
import { useNodeLogs } from "./LogsTab";
import { useRoute } from "./routes";

const pct = (used: bigint, total: bigint) => (total > 0n ? Number((used * 1000n) / total) / 10 : 0);
const fillColor = (p: number) => (p >= 90 ? "bg-destructive" : p >= 75 ? "bg-status-attention" : "bg-status-online");

export function NodeFlyout({ nodeId, snap, services, bottom, onEnter, onLeave, onPicked }: { nodeId: string; snap: FleetSnapshot | undefined; services: FleetService[]; bottom: number; onEnter: () => void; onLeave: () => void; onPicked: () => void }) {
  const { go } = useRoute();
  const showLogs = useNodeLogs();
  const node = snap?.nodes.find((n) => n.nodeId === nodeId);
  if (!snap || !node) return null;
  const now = Date.now();
  const st = snap.status.get(nodeId);
  const word = nodeWord(st, now);
  const m = st?.metrics;
  const stale = !m || now - Number(st?.metricsUnixMs ?? 0) > STALE_MS;
  const spaces = [...snap.spaces].sort((a, b) => b.priority - a.priority);
  const records = snap.drives.filter((d) => d.nodeId === nodeId);
  // Every Drive the Node reported, with its record for the name and Spaces;
  // a recorded Drive the Node did not report is shown as not mounted.
  const usage = new Map((m?.drives ?? []).map((d) => [d.driveId, d]));
  const drives = [...records]
    .sort((a, b) => a.name.localeCompare(b.name))
    .map((r) => ({ record: r, usage: usage.get(r.driveId) }));
  // A Node from before Drive fill was gossiped reports no Drives at all.
  const noFill = !!m && (m.drives ?? []).length === 0 && drives.length > 0;
  const cols = drives.length > 16 ? 3 : drives.length > 8 ? 2 : 1;
  const mine = services.filter((s) => s.spec.nodeId === nodeId);
  const totalUsed = drives.reduce((n, d) => n + (d.usage?.usedBytes ?? 0n), 0n);
  const totalSize = drives.reduce((n, d) => n + (d.usage?.totalBytes ?? 0n), 0n);
  const open = (r: Parameters<typeof go>[0]) => {
    onPicked();
    go(r);
  };
  return (
    <motion.div
      role="dialog"
      aria-label={`${node.name} status`}
      onMouseEnter={onEnter}
      onMouseLeave={onLeave}
      initial={{ x: -24, opacity: 0 }}
      animate={{ x: 0, opacity: 1, bottom }}
      exit={{ x: -24, opacity: 0 }}
      transition={{ duration: 0.15, ease: "easeOut", bottom: { type: "spring", stiffness: 500, damping: 40 } }}
      className="absolute left-full z-10 rounded-r-xl border border-l-0 bg-popover text-popover-foreground shadow-lg"
      style={{ bottom, width: cols === 1 ? 288 : cols === 2 ? 480 : 660 }}
    >
      <div className="flex items-center gap-2.5 border-b px-3 py-2.5">
        <NodeMark word={word} size={32} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            <span className="truncate font-semibold">{node.name}</span>
            <span className="rounded bg-muted px-1 text-[10px] font-medium text-muted-foreground">{node.letter}</span>
          </div>
          <div className="text-xs">
            <StatusWord word={word} />
            {word === "Unreachable" && st && st.lastSeenUnixMs > 0n && <span className="text-muted-foreground"> · last heard {ago(Number(st.lastSeenUnixMs), now)}</span>}
            {word === "Provisioning" && <span className="text-muted-foreground"> · {provisioningLine(st)}</span>}
            {word === "Updating" && st?.updateMessage && <span className="text-muted-foreground"> · {st.updateMessage}</span>}
          </div>
        </div>
      </div>

      {st?.problems.map((p) => (
        <div key={p} className="border-b bg-status-attention/10 px-3 py-1.5 text-xs">
          {node.name} {p}
        </div>
      ))}
      {st?.lastRollback && <div className="border-b bg-status-attention/10 px-3 py-1.5 text-xs">Rolled back its last update: {st.lastRollback}</div>}
      {st?.updateState === 3 && <div className="border-b bg-destructive/10 px-3 py-1.5 text-xs">Last update failed{st.updateMessage ? `: ${st.updateMessage}` : ""}</div>}
      {st?.updateStaged && st.updateState !== 2 && (
        <div className="flex items-center gap-1.5 border-b px-3 py-1.5 text-xs text-muted-foreground">
          <ArrowUpCircle className="size-3.5" /> An update is staged; it runs after the next restart.
        </div>
      )}
      {st?.updateAvailable && !st.updateStaged && st.updateState !== 2 && (
        <div className="flex items-center gap-1.5 border-b px-3 py-1.5 text-xs text-muted-foreground">
          <ArrowUpCircle className="size-3.5" /> A newer Maître Server is available.
        </div>
      )}

      {m ? (
        <div className={cn("grid grid-cols-3 gap-2 border-b px-3 py-2.5", stale && "opacity-50")}>
          <Mini icon={<Cpu />} label="CPU" value={`${m.cpuPercent.toFixed(0)}%`} p={m.cpuPercent} />
          <Mini icon={<MemoryStick />} label="Memory" value={`${pct(m.memoryUsedBytes, m.memoryTotalBytes).toFixed(0)}%`} p={pct(m.memoryUsedBytes, m.memoryTotalBytes)} />
          <Mini icon={<Thermometer />} label="Temp" value={m.temperatureC > 0 ? `${m.temperatureC.toFixed(0)} °C` : "–"} p={m.temperatureC} />
        </div>
      ) : (
        <div className="border-b px-3 py-2.5 text-xs text-muted-foreground">No reading from {node.name} yet.</div>
      )}

      <div className={cn("border-b px-3 py-2.5", stale && "opacity-50")}>
        <div className="mb-1.5 flex items-baseline justify-between text-xs">
          <span className="font-semibold">Drives</span>
          {totalSize > 0n && (
            <span className="text-muted-foreground">
              {formatBytes(totalUsed)} of {formatBytes(totalSize)} used
            </span>
          )}
        </div>
        {noFill && <p className="mb-1.5 text-[11px] text-muted-foreground">How full each Drive is shows once {node.name} runs a newer Maître Server.</p>}
        {drives.length === 0 ? (
          <p className="text-xs text-muted-foreground">No Drives yet.</p>
        ) : (
          <ul className="grid gap-x-4 gap-y-1.5" style={{ gridTemplateColumns: `repeat(${cols}, minmax(0, 1fr))` }}>
            {drives.map(({ record: r, usage: u }) => {
              const p = u ? pct(u.usedBytes, u.totalBytes) : 0;
              const ids = spacesOf(r, spaces);
              return (
                <li key={r.driveId} className="min-w-0 text-xs">
                  <div className="flex items-center gap-1.5">
                    <HardDrive className="size-3 shrink-0 text-muted-foreground" />
                    <span className="truncate font-medium">{r.name}</span>
                    <span className="flex gap-0.5">
                      {ids.map((id) => (
                        <span key={id} className="size-1.5 rounded-full" style={{ background: colorOf(spaces.find((s) => s.spaceId === id), spaces) }} title={spaces.find((s) => s.spaceId === id)?.name} />
                      ))}
                    </span>
                    <span className={cn("ml-auto shrink-0 tabular-nums", !u ? "text-muted-foreground" : p >= 90 ? "text-destructive" : p >= 75 ? "text-status-attention" : "text-muted-foreground")}>{u ? `${p.toFixed(0)}%` : noFill ? "" : "not mounted"}</span>
                  </div>
                  <div className="mt-0.5 h-1 rounded bg-muted">{u && <div className={cn("h-1 rounded transition-[width] duration-700", fillColor(p))} style={{ width: `${Math.min(100, p)}%` }} />}</div>
                  {u && cols === 1 && <div className="mt-0.5 text-[10px] text-muted-foreground">{formatBytes(u.totalBytes - u.usedBytes)} free of {formatBytes(u.totalBytes)}</div>}
                </li>
              );
            })}
          </ul>
        )}
      </div>

      <div className="px-3 py-2.5">
        <div className="mb-1.5 text-xs font-semibold">Runs</div>
        {mine.length === 0 ? (
          <p className="text-xs text-muted-foreground">Nothing yet.</p>
        ) : (
          <ul className="flex flex-wrap gap-1.5">
            {mine.map((s) => (
              <li key={s.spec.name}>
                <button onClick={() => open({ name: "service", service: s.spec.name })} className="flex items-center gap-1.5 rounded-md border px-1.5 py-0.5 text-xs hover:bg-accent" title={`${s.spec.name} · ${s.word}`}>
                  <ServiceIcon catalogId={s.spec.catalogId} word={s.word} size={18} />
                  {s.spec.name}
                </button>
              </li>
            ))}
          </ul>
        )}
        <div className="mt-2.5 flex gap-1">
          <button onClick={() => open({ name: "fleet", tab: "nodes", nodeId })} className="flex items-center gap-1 rounded-md px-1.5 py-1 text-xs hover:bg-accent">
            Details <ArrowRight className="size-3" />
          </button>
          <button onClick={() => open({ name: "fleet", tab: "storage" })} className="flex items-center gap-1 rounded-md px-1.5 py-1 text-xs hover:bg-accent">
            Storage <ArrowRight className="size-3" />
          </button>
          <button onClick={() => (onPicked(), showLogs(nodeId))} className="flex items-center gap-1 rounded-md px-1.5 py-1 text-xs hover:bg-accent">
            Logs <ArrowRight className="size-3" />
          </button>
        </div>
      </div>
    </motion.div>
  );
}

function Mini({ icon, label, value, p }: { icon: React.ReactNode; label: string; value: string; p: number }) {
  return (
    <div className="min-w-0 text-xs">
      <div className="flex items-center gap-1 text-muted-foreground [&>svg]:size-3">
        {icon}
        {label}
      </div>
      <div className="font-semibold tabular-nums">{value}</div>
      <div className="mt-0.5 h-1 rounded bg-muted">
        <div className={cn("h-1 rounded transition-[width] duration-700", fillColor(p))} style={{ width: `${Math.min(100, Math.max(0, p))}%` }} />
      </div>
    </div>
  );
}
