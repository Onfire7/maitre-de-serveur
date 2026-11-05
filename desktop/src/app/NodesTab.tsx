// The Nodes tab (decisions 133, 148): every Node with its gauges from the
// anchor's view, Rename, Reboot and Remove, and the ways to add a Node.
import { useState } from "react";
import { Dices, RefreshCw, Trash2, Pencil, ChevronDown, ArrowUpCircle } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { cn } from "@/lib/utils";
import { HoldButton } from "@/components/hold-button";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { nodeDown, nodeWord, provisioningLine, STALE_MS } from "@/fleet/useFleet";
import { node as nodeClient, targetFor } from "@/fleet/actions";
import { currentRecord, putSigned } from "@/fleet/sign";
import { leftBy, removeLeftovers } from "@/fleet/leftovers";
import type { NodeMetrics } from "@/gen/maitre/v1/node_pb";
import type { Node } from "@/gen/maitre/v1/fleet_pb";
import type { ConsoleState, Found } from "@/state";
import { randomNodeName } from "@/names";
import { formatBytes, ago } from "@/lib/format";
import { NetbootPanel, restartInstaller } from "./NetbootPanel";
import { InstallerLogs } from "./InstallerLogs";
import { MediaPanel } from "./MediaPanel";
import { Leftovers } from "./Leftovers";
import { FleetTimeZone } from "./FleetTimeZone";
import { NodeDetails } from "./NodeDetails";
import { NodeMark, StatusWord } from "./status";
import { useRoute } from "./routes";

export function NodesTab({
  snap,
  state,
  found,
  selected,
  onState,
  onRefresh,
}: {
  snap: FleetSnapshot | undefined;
  state: ConsoleState;
  found: Found[];
  selected?: string;
  onState: (s: ConsoleState) => Promise<void>;
  onRefresh?: () => void;
}) {
  const { go } = useRoute();
  const nodes = snap?.nodes ?? [];
  const now = Date.now();
  const [byAddr, setByAddr] = useState("");
  const [more, setMore] = useState(false);
  // `?node=<letter>` opens that Node's details on load (screenshot fixture).
  const fixtureLetter = useState(() => new URLSearchParams(window.location.search).get("node"))[0];
  const installers = found.filter((f) => f.kind === "installer");
  // The Installer whose log is open (decision 271).
  const [logsOf, setLogsOf] = useState<{ addr: string; label: string } | null>(null);
  const knownIds = new Set(state.nodes.map((n) => n.node_id));
  return (
    <div className="space-y-8">
      <section className="space-y-3">
        {nodes.length === 0 && <p className="text-sm text-muted-foreground">No Nodes yet. Set one up below.</p>}
        {nodes.map((n) => (
          <NodeCard key={n.nodeId} node={n} snap={snap!} state={state} now={now} open={selected ? selected === n.nodeId : n.letter === fixtureLetter} onOpen={(o) => go({ name: "fleet", tab: "nodes", nodeId: o ? n.nodeId : undefined })} onState={onState} />
        ))}
      </section>

      <FleetTimeZone snap={snap} />

      <Leftovers snap={snap} state={state} onDone={onRefresh} />

      <section className="space-y-3">
        <h2 className="text-base font-semibold">Add a Node</h2>
        {logsOf && <InstallerLogs addr={logsOf.addr} label={logsOf.label} onClose={() => setLogsOf(null)} />}
        {installers.length === 0 ? (
          <p className="text-sm text-muted-foreground">Boot a machine from the Maître installer and it appears here. Looking on this network…</p>
        ) : (
          <ul className="space-y-2">
            {installers.map((f) => (
              <li key={f.id || f.addresses[0]} className="flex items-center justify-between rounded-lg border p-3">
                <div>
                  <div className="font-medium">{f.machine || "A machine"} is ready to install</div>
                  <div className="text-xs text-muted-foreground">
                    {f.addresses[0]} · installer {f.id.slice(0, 8)}
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  <Button size="sm" variant="outline" onClick={() => setLogsOf({ addr: f.addresses[0], label: f.machine || f.addresses[0] })}>
                    Installer log
                  </Button>
                  <Button size="sm" variant="outline" onClick={() => restartInstaller(f.addresses[0], f.machine || "The machine")}>
                    Restart
                  </Button>
                  <Button size="sm" onClick={() => go({ name: "add-node", addr: f.addresses[0], installerId: f.id })}>
                    Set up
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}
        <div className="flex items-center gap-2 text-sm">
          <span className="text-muted-foreground">Not listed? Set it up by address:</span>
          <Input value={byAddr} onChange={(e) => setByAddr(e.target.value)} placeholder="192.168.1.20:7443" className="h-8 w-48" />
          <Button size="sm" variant="outline" disabled={!byAddr.includes(":")} onClick={() => go({ name: "add-node", addr: byAddr.trim(), installerId: "" })}>
            Set up
          </Button>
        </div>
        {found
          .filter((f) => f.kind === "node" && !knownIds.has(f.id))
          .map((f) => (
            <p key={f.id} className="text-xs text-muted-foreground">
              Node <b>{f.name}</b> is on this network but belongs to {f.fleet_id === state.fleet?.fleet_id ? "this Fleet" : "another Fleet"}.
            </p>
          ))}
        <button onClick={() => setMore((m) => !m)} className="flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
          <ChevronDown className={cn("size-4 transition-transform", more && "rotate-180")} /> Boot from the network or prepare a USB stick
        </button>
        {more && (
          <div className="grid gap-4 md:grid-cols-2">
            <div className="rounded-xl border bg-card p-4">
              <NetbootPanel found={found} />
            </div>
            <div className="rounded-xl border bg-card p-4">
              <MediaPanel />
            </div>
          </div>
        )}
      </section>
    </div>
  );
}

function NodeCard({ node, snap, state, now, open, onOpen, onState }: { node: Node; snap: FleetSnapshot; state: ConsoleState; now: number; open: boolean; onOpen: (o: boolean) => void; onState: (s: ConsoleState) => Promise<void> }) {
  const st = snap.status.get(node.nodeId);
  const word = nodeWord(st, now);
  const stale = !st?.metrics || now - Number(st.metricsUnixMs) > STALE_MS;
  const t = targetFor(snap, state.nodes, node.nodeId);
  const [renaming, setRenaming] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<"reboot" | "remove" | null>(null);
  const [typed, setTyped] = useState("");
  // Update opens the details with the update dialog, so the wait while the
  // Node restarts shows there.
  const [askUpdate, setAskUpdate] = useState(false);
  const canUpdate = !!t && !!st?.updateAvailable && !st.updateStaged && word !== "Updating" && !nodeDown(word);
  const services = snap.specs.filter((s) => s.nodeId === node.nodeId).map((s) => s.name);
  const taken = [...snap.nodes.map((n) => n.name), ...snap.drives.map((d) => d.name)];

  async function rename() {
    const name = (renaming ?? "").trim();
    if (!name || !t) return;
    try {
      await nodeClient(t).renameNode({ name });
      await onState({ ...state, nodes: state.nodes.map((n) => (n.node_id === node.nodeId ? { ...n, name } : n)) });
      toast.success(`${node.name} is now ${name}`);
      setRenaming(null);
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    }
  }
  async function reboot() {
    if (!t) return;
    try {
      await nodeClient(t).reboot({});
      toast(`${node.name} is restarting`);
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    }
  }
  async function remove() {
    const others = state.nodes.filter((x) => x.node_id !== node.nodeId).flatMap((n) => n.addresses.map((a) => ({ addr: a, fingerprint: n.node_id })));
    let removed = others.length === 0;
    for (const t of others) {
      try {
        await putSigned(t, { kind: "node", id: node.nodeId, deleted: true }, currentRecord(snap.records, "node", node.nodeId));
        removed = true;
        break;
      } catch {
        /* next */
      }
    }
    // Decision 276: its Services and Drives go with it, so the Fleet does
    // not keep records naming a Node that is gone.
    const left = removed && others.length > 0 ? await removeLeftovers(others, leftBy(snap.records, node.nodeId)) : undefined;
    await onState({ ...state, nodes: state.nodes.filter((n) => n.node_id !== node.nodeId) });
    if (left) toast.warning(`${node.name} was removed from the Fleet, but not everything it left: ${left.message}. The rest is listed under "Left behind by removed Nodes".`);
    else toast(`${node.name} was removed from the Fleet`);
  }

  return (
    <div className={cn("rounded-xl border bg-card", open && "ring-1 ring-ring")}>
      <div className="flex items-center gap-3 p-4">
        <NodeMark word={word} size={40} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate text-base font-semibold">{node.name}</span>
            <button className="rounded p-0.5 text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-40" title="Rename" aria-label={`Rename ${node.name}`} disabled={!t || nodeDown(word)} onClick={() => setRenaming(node.name)}>
              <Pencil className="size-3.5" />
            </button>
            <span className="rounded bg-muted px-1.5 text-xs font-medium text-muted-foreground">{node.letter}</span>
            <StatusWord word={word} className="text-xs" />
          </div>
          <div className="truncate text-xs text-muted-foreground">
            {node.addresses[0]}
            {st && word === "Unreachable" && st.lastSeenUnixMs > 0n && ` · last seen ${ago(Number(st.lastSeenUnixMs), now)}`}
            {word === "Provisioning" && ` · ${provisioningLine(st)}`}
            {services.length > 0 ? ` · runs ${services.join(", ")}` : " · runs nothing yet"}
          </div>
        </div>
        <div className="flex gap-1">
          {canUpdate && (
            <Button
              size="sm"
              variant="ghost"
              title="A newer Maître Server is available"
              onClick={() => {
                setAskUpdate(true);
                onOpen(true);
              }}
            >
              <ArrowUpCircle /> Update
            </Button>
          )}
          <Button size="sm" variant="ghost" onClick={() => setConfirm("reboot")} disabled={!t || nodeDown(word)}>
            <RefreshCw /> Restart
          </Button>
          <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" onClick={() => setConfirm("remove")}>
            <Trash2 /> Remove
          </Button>
          <Button size="sm" variant="outline" onClick={() => onOpen(!open)}>
            {open ? "Less" : "Details"}
          </Button>
        </div>
      </div>
      {st?.metrics && (
        <div className={cn("grid grid-cols-2 gap-2 border-t px-4 py-3 md:grid-cols-4 lg:grid-cols-5", stale && "opacity-50")}>
          <Gauges m={st.metrics} />
        </div>
      )}
      {open && (
        <div className="border-t p-4">
          <NodeDetails node={node} target={t} snap={snap} unreachable={nodeDown(word)} askUpdate={askUpdate} onAsked={() => setAskUpdate(false)} />
        </div>
      )}

      <Dialog open={renaming !== null} onOpenChange={(o) => !o && setRenaming(null)}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Rename {node.name}</DialogTitle>
            <DialogDescription>Node {node.letter}: names start with {node.letter}. The host name follows now; the Tailscale name on the next restart.</DialogDescription>
          </DialogHeader>
          <form
            className="flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              rename();
            }}
          >
            <Input autoFocus value={renaming ?? ""} onChange={(e) => setRenaming(e.target.value)} maxLength={40} />
            <Button type="button" variant="outline" size="icon" title="Draw another name" onClick={() => setRenaming(randomNodeName(node.letter, taken))}>
              <Dices />
            </Button>
          </form>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRenaming(null)}>
              Cancel
            </Button>
            <Button onClick={rename} disabled={!(renaming ?? "").trim()}>
              Save
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <AlertDialog open={confirm !== null} onOpenChange={(o) => !o && (setConfirm(null), setTyped(""))}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{confirm === "reboot" ? `Restart ${node.name}?` : `Remove ${node.name} from the Fleet?`}</AlertDialogTitle>
            <AlertDialogDescription>
              {confirm === "reboot" ? (
                <>Its Services ({services.length ? services.join(", ") : "none"}) stop for a minute or two while it comes back.</>
              ) : state.nodes.length > 1 ? (
                <>
                  The other Nodes stop trusting it and forget it. Reinstall it to bring it back.{" "}
                  {services.length > 0 ? (
                    <>
                      <b>{services.join(", ")}</b> {services.length > 1 ? "run" : "runs"} on it and {services.length > 1 ? "are" : "is"} removed with it; to keep {services.length > 1 ? "them" : "it"}, move {services.length > 1 ? "them" : "it"} to another Node first. The data stays on that machine's Drives.
                    </>
                  ) : (
                    "No Services run on it. Its Drives leave the Fleet with it."
                  )}
                </>
              ) : (
                <>It is the only Node, so this only forgets it on this computer.</>
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          {confirm === "remove" && (
            <div className="space-y-1.5 text-sm">
              <div>
                Type <code className="rounded bg-muted px-1">{node.name}</code> to confirm
              </div>
              <Input value={typed} onChange={(e) => setTyped(e.target.value)} />
            </div>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            {confirm === "reboot" ? (
              <HoldButton
                onConfirm={() => {
                  setConfirm(null);
                  reboot();
                }}
              >
                Hold to restart
              </HoldButton>
            ) : (
              <AlertDialogAction disabled={typed.trim() !== node.name} onClick={() => remove()} variant="destructive" className="dark:bg-destructive">
                Remove
              </AlertDialogAction>
            )}
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

export function Gauges({ m }: { m: NodeMetrics }) {
  const pct = (used: bigint, total: bigint) => (total > 0n ? Number((used * 1000n) / total) / 10 : 0);
  const memPct = pct(m.memoryUsedBytes, m.memoryTotalBytes);
  const poolPct = pct(m.poolUsedBytes, m.poolTotalBytes);
  // The Node's own disk, which had no gauge at all until a full one took
  // Avalon down for twenty hours: this is the number that was missing.
  const sysPct = pct(m.systemDiskUsedBytes, m.systemDiskTotalBytes);
  const sysFree = m.systemDiskTotalBytes - m.systemDiskUsedBytes;
  const storePct = pct(m.imageStoreUsedBytes, m.imageStoreTotalBytes);
  // The same filesystem until decision 319 moves the store off root, and
  // then a gauge worth its own tile.
  const storeApart = m.imageStoreTotalBytes > 0n && m.imageStoreTotalBytes !== m.systemDiskTotalBytes;
  return (
    <>
      <Gauge label="CPU" value={`${m.cpuPercent.toFixed(0)}%`} pct={m.cpuPercent} sub={`load ${m.load1m.toFixed(2)}`} />
      <Gauge label="Memory" value={`${memPct.toFixed(0)}%`} pct={memPct} sub={`${formatBytes(m.memoryUsedBytes)} of ${formatBytes(m.memoryTotalBytes)}`} />
      <Gauge label="Storage" value={`${poolPct.toFixed(0)}%`} pct={poolPct} sub={m.poolFallback ? "no Pool: data lives on the system disk" : `${formatBytes(m.poolUsedBytes)} of ${formatBytes(m.poolTotalBytes)}`} warn={m.poolFallback} />
      {m.systemDiskTotalBytes > 0n && (
        <Gauge
          label="System disk"
          value={`${sysPct.toFixed(0)}%`}
          pct={sysPct}
          sub={sysFree < 3n * 1024n * 1024n * 1024n ? `only ${formatBytes(sysFree)} free: an update cannot be staged` : `${formatBytes(sysFree)} free of ${formatBytes(m.systemDiskTotalBytes)}`}
          warn={sysFree < 3n * 1024n * 1024n * 1024n}
        />
      )}
      {storeApart && <Gauge label="Image store" value={`${storePct.toFixed(0)}%`} pct={storePct} sub={m.imageStoreDir} />}
      <Gauge label="Network" value={`↓ ${formatBytes(m.netRxBytesPerSec)}/s`} sub={`↑ ${formatBytes(m.netTxBytesPerSec)}/s`} />
      {m.temperatureC > 0 && <Gauge label="Temperature" value={`${m.temperatureC.toFixed(0)} °C`} pct={m.temperatureC} />}
    </>
  );
}

function Gauge({ label, value, pct, sub, warn }: { label: string; value: string; pct?: number; sub?: string; warn?: boolean }) {
  return (
    <div className="rounded-lg bg-muted/50 px-3 py-2">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="text-lg font-semibold tabular-nums">{value}</div>
      {pct !== undefined && (
        <div className="my-1 h-1 rounded bg-border">
          <div className={cn("h-1 rounded transition-[width] duration-700", pct > 90 ? "bg-destructive" : "bg-status-online")} style={{ width: `${Math.min(100, Math.max(0, pct))}%` }} />
        </div>
      )}
      {sub && <div className={cn("truncate text-xs", warn ? "text-status-attention" : "text-muted-foreground")}>{sub}</div>}
    </div>
  );
}
