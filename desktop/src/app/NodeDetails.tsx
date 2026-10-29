// A Node's details inside its card on the Nodes tab (decision 148): what it
// is, what it runs, its Maître Server image with update, roll back and the
// wait while it restarts, and its usage over time (210). Every call here
// goes straight to the Node (160).
import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { invoke } from "@tauri-apps/api/core";
import { toast } from "sonner";
import { Copy, ScrollText, ArrowUpCircle, Undo2, Globe, Router, Network, RotateCw, ListOrdered } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { HoldButton } from "@/components/hold-button";
import { AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { cn } from "@/lib/utils";
import { formatUptime } from "@/lib/format";
import { NodeService, UpdateStatus_State, type ImageDeployment, type UpdateStatus } from "@/gen/maitre/v1/node_pb";
import type { Node } from "@/gen/maitre/v1/fleet_pb";
import { transportFor, type Target } from "@/transport";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { useFleetServices } from "@/fleet/useFleet";
import { busy, describe, enqueue, onQueue, queued, remove, type Job, type Maintenance } from "@/fleet/updateQueue";
import type { Found } from "@/state";
import { ServiceIcon, StatusWord } from "./status";
import { useRoute } from "./routes";
import { useNodeLogs } from "./LogsTab";
import { NodeUsage } from "./Usage";

export function NodeDetails({ node, target, snap, unreachable, askUpdate, onAsked }: { node: Node; target: Target | undefined; snap: FleetSnapshot; unreachable: boolean; askUpdate?: boolean; onAsked?: () => void }) {
  const { go } = useRoute();
  const client = useMemo(() => (target ? createClient(NodeService, transportFor(target)) : undefined), [target?.addr, target?.fingerprint]);
  // The core calls (ADR 0007) go straight to the Node whatever the anchor
  // thinks of it: if this Console can reach it, it can see and update it.
  const info = useQuery({ queryKey: ["node-info", node.nodeId, target?.addr], enabled: !!client, refetchInterval: 5000, retry: false, queryFn: async () => (await client!.getNodeInfo({})).node });
  const services = useFleetServices(snap).filter((s) => s.spec.nodeId === node.nodeId);
  const i = info.data;

  if (!target) return <p className="text-sm text-muted-foreground">This Node has no known address.</p>;

  return (
    <div className="grid gap-6 md:grid-cols-2">
      <section className="space-y-2">
        <h3 className="text-sm font-semibold">About</h3>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
          <dt className="text-muted-foreground">Node ID</dt>
          <dd className="flex items-center gap-1 font-mono text-xs">
            {node.nodeId.slice(0, 16)}…
            <button className="rounded p-0.5 text-muted-foreground hover:text-foreground" title="Copy the full id" onClick={() => navigator.clipboard?.writeText(node.nodeId).then(() => toast("Node id copied"))}>
              <Copy className="size-3" />
            </button>
          </dd>
          <dt className="text-muted-foreground">Version</dt>
          <dd>{i ? `${i.version}${i.devMode ? " (dev mode)" : ""}` : unreachable || info.error ? "unreachable" : "…"}</dd>
          <dt className="text-muted-foreground">Up for</dt>
          <dd>{i ? formatUptime(Number(i.uptimeSeconds)) : "…"}</dd>
          <dt className="text-muted-foreground">Addresses</dt>
          <dd className="font-mono text-xs">{node.addresses.join(", ")}</dd>
          {i?.publicIp && (
            <>
              <dt className="flex items-center gap-1 text-muted-foreground">
                <Globe className="size-3.5" /> Internet
              </dt>
              <dd className="font-mono text-xs">{i.publicIp}</dd>
            </>
          )}
          {i?.portMapping && (
            <>
              <dt className="flex items-center gap-1 text-muted-foreground">
                <Router className="size-3.5" /> Router
              </dt>
              <dd>{i.portMapping.replace(/^router: /, "")}</dd>
            </>
          )}
          <dt className="flex items-center gap-1 text-muted-foreground">
            <Network className="size-3.5" /> Tailscale
          </dt>
          <dd>
            {!i ? "…" : !i.tailscale ? <span className="text-muted-foreground">not connected</span> : i.tailscale.state === "Running" ? <>connected as <span className="font-mono text-xs">{i.tailscale.dnsName.replace(/\.$/, "")}</span></> : i.tailscale.loginUrl ? <a className="underline" href={i.tailscale.loginUrl} target="_blank" rel="noreferrer">needs a login</a> : <span className="text-muted-foreground">{i.tailscale.state || "starting"}{i.tailscale.error && ` · ${i.tailscale.error}`}</span>}
          </dd>
        </dl>
        {i && i.peers.length > 0 && (
          <div className="pt-2">
            <h4 className="text-xs font-semibold text-muted-foreground">Syncs with</h4>
            <ul className="mt-1 space-y-0.5 text-sm">
              {i.peers.map((p) => (
                <li key={p.nodeId} className="flex items-center gap-2">
                  <span className={cn("size-2 rounded-full", p.online ? "bg-status-online" : "bg-status-stopped")} />
                  {p.name}
                  <span className="text-xs text-muted-foreground">
                    {p.online ? "in sync" : "offline"}
                    {p.queued > 0 && ` · ${p.queued} ${p.queued === 1 ? "change" : "changes"} waiting`}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
      </section>

      <section className="space-y-2">
        <h3 className="text-sm font-semibold">Runs</h3>
        {services.length === 0 ? (
          <p className="text-sm text-muted-foreground">Nothing yet. Pick a Service from the Catalog and choose this Node.</p>
        ) : (
          <ul className="space-y-1">
            {services.map((s) => (
              <li key={s.spec.name}>
                <button onClick={() => go({ name: "service", service: s.spec.name })} className="flex w-full items-center gap-2 rounded-md px-1.5 py-1 text-left text-sm hover:bg-accent">
                  <ServiceIcon catalogId={s.spec.catalogId} word={s.word} size={24} />
                  <span className="flex-1 truncate">{s.spec.name}</span>
                  <StatusWord word={s.word} className="text-xs" />
                </button>
              </li>
            ))}
          </ul>
        )}
        <p className="text-xs text-muted-foreground">
          Its Drives are on the{" "}
          <button className="underline" onClick={() => go({ name: "fleet", tab: "storage" })}>
            Storage
          </button>{" "}
          tab.
        </p>
      </section>

      <section className="space-y-3 md:col-span-2">
        <h3 className="text-sm font-semibold">Usage</h3>
        <NodeUsage node={node} target={unreachable ? undefined : target} snap={snap} />
      </section>

      <div className="md:col-span-2">
        <ServerImage node={node} target={target} snap={snap} askUpdate={askUpdate} onAsked={onAsked} />
      </div>
    </div>
  );
}

/** The Console's update queue (decision 215), re-read whenever it changes. */
export function useUpdateQueue() {
  return useSyncExternalStore(onQueue, queued);
}

/**
 * A Node's Maître Server image: what it runs, has staged and can go back
 * to, with Logs, Update and Roll back, and the wait while it restarts. On
 * the Node's card and, for every Node, on the Updates tab (decision 197).
 * Update, Roll back and Restart now join the Console's queue (215).
 */
export function ServerImage({ node, target, snap, askUpdate, onAsked, title = "Maître Server" }: { node: Node; target: Target | undefined; snap: FleetSnapshot; askUpdate?: boolean; onAsked?: () => void; title?: React.ReactNode }) {
  const showLogs = useNodeLogs();
  const qc = useQueryClient();
  const client = useMemo(() => (target ? createClient(NodeService, transportFor(target)) : undefined), [target?.addr, target?.fingerprint]);
  const queue = useUpdateQueue();
  const job = queue.find((j) => j.nodeId === node.nodeId);
  const maint = job && job.phase !== "waiting" ? job : null;
  const info = useQuery({ queryKey: ["node-info", node.nodeId, target?.addr], enabled: !!client && !maint, refetchInterval: 5000, retry: false, queryFn: async () => (await client!.getNodeInfo({})).node });
  const upd = useQuery({ queryKey: ["update-status", node.nodeId, target?.addr], enabled: !!client && !maint, refetchInterval: 3000, retry: false, queryFn: async () => (await client!.getUpdateStatus({})).status });
  const i = info.data;
  const st = upd.data;
  const problems = snap.status.get(node.nodeId)?.problems ?? [];
  const [updateOpen, setUpdateOpen] = useState(false);
  const [rollbackOpen, setRollbackOpen] = useState(false);
  // The card's Update button: open the dialog once the Node's status is in.
  useEffect(() => {
    if (!askUpdate || !st) return;
    if (st.supported) setUpdateOpen(true);
    onAsked?.();
  }, [askUpdate, !!st]);

  // Fresh numbers once this Node's job is over.
  const jobId = job?.id;
  const hadJob = useRef(jobId);
  useEffect(() => {
    const had = hadJob.current;
    hadJob.current = jobId;
    if (had === undefined || jobId !== undefined) return;
    qc.invalidateQueries({ queryKey: ["node-info", node.nodeId] });
    qc.invalidateQueries({ queryKey: ["update-status", node.nodeId] });
    qc.invalidateQueries({ queryKey: ["fleet"] });
  }, [jobId]);

  function run(fn: () => Promise<unknown>, m: Maintenance) {
    if (!client) return;
    const wait = busy(snap);
    enqueue(node.nodeId, node.name, m, { start: fn, status: async () => (await client.getUpdateStatus({})).status });
    if (wait) toast(`${node.name}: ${describe(m)} queued`, { description: "It starts once no Node is updating." });
  }

  if (maint) return <MaintenanceCard job={maint} />;

  return (
    <>
      <section className="space-y-2">
        <div className="flex items-center gap-2">
          <h3 className="text-sm font-semibold">{title}</h3>
          {st?.state === UpdateStatus_State.UPDATING && <Badge className="bg-status-starting text-white">updating {Math.round(st.progress * 100)}%</Badge>}
          {st?.state === UpdateStatus_State.FAILED && <Badge variant="destructive">last update failed</Badge>}
          <span className="flex-1" />
          <Button size="sm" variant="outline" onClick={() => showLogs(node.nodeId)}>
            <ScrollText /> Logs
          </Button>
          {st?.supported && (
            <>
              <Button size="sm" variant="outline" disabled={!!job || st.state === UpdateStatus_State.UPDATING} onClick={() => setUpdateOpen(true)}>
                <ArrowUpCircle /> Update…
              </Button>
              <Button size="sm" variant="outline" disabled={!!job || !st.rollback || st.state === UpdateStatus_State.UPDATING} onClick={() => setRollbackOpen(true)}>
                <Undo2 /> Roll back…
              </Button>
            </>
          )}
        </div>
        {job && <QueuedLine job={job} queue={queue} />}
        {problems.length > 0 && (
          <ul className="space-y-0.5 rounded-md bg-status-attention/10 px-2 py-1.5 text-xs">
            {problems.map((p) => (
              <li key={p}>
                {node.name} {p}
              </li>
            ))}
          </ul>
        )}
        {upd.error && ConnectError.from(upd.error).code === Code.Unimplemented && <p className="text-xs text-muted-foreground">This Node runs a Maître Server from before updates existed. Reinstall it from current media or over the network to manage its image here.</p>}
        {st && !st.supported && <p className="text-xs text-muted-foreground">Not an image-based Node (dev mode), so it cannot update itself.</p>}
        {st?.supported && (
          <div className="grid gap-2 sm:grid-cols-3">
            <Deployment label="Running" d={st.booted} version={i?.version} />
            {st.staged && <Deployment label="Staged for next restart" d={st.staged} accent />}
            {st.rollback && <Deployment label={st.rollbackQueued ? "Rollback, boots next" : "Previous"} d={st.rollback} />}
          </div>
        )}
        {st?.state === UpdateStatus_State.UPDATING && (
          <div className="space-y-1">
            <div className="h-1.5 overflow-hidden rounded bg-muted">
              <div className="h-full rounded bg-status-starting transition-[width]" style={{ width: `${Math.round(st.progress * 100)}%` }} />
            </div>
            <p className="text-xs text-muted-foreground">
              Updating to {st.target}: {st.message}
            </p>
          </div>
        )}
        {st?.state === UpdateStatus_State.FAILED && <p className="text-xs text-destructive">Update to {st.target} failed: {st.message}</p>}
        {st?.staged && st.state !== UpdateStatus_State.UPDATING && !job && (
          <p className="flex items-center gap-2 text-xs text-muted-foreground">
            An update is staged. Restart the Node to run it.
            <Button size="sm" variant="outline" className="h-7" onClick={() => client && run(() => client.reboot({}), { kind: "reboot" })}>
              <RotateCw /> Restart now
            </Button>
          </p>
        )}
        {st?.available && st.state !== UpdateStatus_State.UPDATING && <p className="text-xs text-muted-foreground">The registry has a newer build of {st.booted?.image || "this Node's image"}.</p>}
        {st?.rollout && <p className="text-xs text-muted-foreground">Automatic updates: {st.rollout}.</p>}
        {st?.lastRollback && <p className="text-xs text-status-attention">The last update was rolled back: {st.lastRollback}. The Node runs the image from before it.</p>}
      </section>

      {client && st && <UpdateDialog open={updateOpen} onClose={() => setUpdateOpen(false)} status={st} queue={busy(snap)} onRun={async (image, reboot, insecure) => run(() => client.updateImage({ image, reboot, insecureRegistry: insecure }), { kind: "update", target: image, reboot })} />}
      <AlertDialog open={rollbackOpen} onOpenChange={setRollbackOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Roll {node.name} back?</AlertDialogTitle>
            <AlertDialogDescription>
              It restarts into {st?.rollback?.version ? `version ${st.rollback.version}` : "its previous image"}
              {st?.rollback?.timestamp && `, built ${new Date(st.rollback.timestamp).toLocaleDateString()}`}. Its Services stop for a minute or two.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <HoldButton
              onConfirm={() => {
                setRollbackOpen(false);
                if (client) run(() => client.rollback({ reboot: true }), { kind: "rollback", reboot: true });
              }}
            >
              {busy(snap) ? "Hold to queue the roll back" : "Hold to roll back and restart"}
            </HoldButton>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function Deployment({ label, d, version, accent }: { label: string; d?: ImageDeployment; version?: string; accent?: boolean }) {
  if (!d) return null;
  const v = version || d.version;
  return (
    <div className={cn("rounded-lg border p-2.5 text-xs", accent && "border-status-starting/60 bg-status-starting/10")}>
      <div className="text-muted-foreground">{label}</div>
      <div className="font-medium">{v ? `version ${v}` : "unknown version"}</div>
      <div className="truncate font-mono text-[11px] text-muted-foreground" title={`${d.image}\n${d.digest}`}>
        {d.image}
      </div>
      {d.timestamp && <div className="text-muted-foreground">built {new Date(d.timestamp).toLocaleString()}</div>}
    </div>
  );
}

interface DepotImage {
  ref: string;
  host: string;
  tag: string;
  version: string;
  created: string;
}

/** Decisions 82, 91, 96: pick an image (Depots on the LAN offer theirs), restart now or later. */
function UpdateDialog({ open, onClose, status, queue, onRun }: { open: boolean; onClose: () => void; status: UpdateStatus; queue: boolean; onRun: (image: string, reboot: boolean, insecure: boolean) => Promise<void> }) {
  const [image, setImage] = useState(status.booted?.image ?? "");
  const [reboot, setReboot] = useState(true);
  const [busy, setBusy] = useState(false);
  const depot = useQuery({
    queryKey: ["depot-images"],
    enabled: open,
    refetchInterval: 10_000,
    queryFn: async () => {
      const out: DepotImage[] = [];
      const found = (await invoke<Found[]>("discover").catch(() => [] as Found[])).filter((f) => f.kind === "depot");
      for (const d of found) {
        const addr = d.addresses[0];
        if (!addr) continue;
        try {
          const j = (await (await fetch(`http://${addr}/api/images`, { signal: AbortSignal.timeout(3000) })).json()) as { registry: string; images: Record<string, { tag: string; version?: string; created?: string }[]> };
          for (const [name, tags] of Object.entries(j.images)) for (const tg of tags) out.push({ ref: `${j.registry}/${name}:${tg.tag}`, host: d.name, tag: tg.tag, version: tg.version ?? "", created: tg.created ?? "" });
        } catch {
          /* not answering */
        }
      }
      return out;
    },
  });
  const host = image.split("/")[0] ?? "";
  const insecure = /\.local(:\d+)?$/.test(host) || /^\d+\.\d+\.\d+\.\d+(:\d+)?$/.test(host) || /^localhost(:\d+)?$/.test(host);
  useEffect(() => {
    if (open) setImage(status.booted?.image ?? "");
  }, [open]);
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Update Maître Server</DialogTitle>
          <DialogDescription>The Node downloads the image and stages it; restarting switches to it. The image it runs now stays one click away.</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="upd-image">Image</Label>
            <Input id="upd-image" value={image} onChange={(e) => setImage(e.target.value)} placeholder="registry/maitre-server:tag" className="font-mono text-xs" />
            {status.available && image === status.booted?.image && <p className="text-xs text-muted-foreground">The registry has a newer build of this image.</p>}
            {insecure && <p className="text-xs text-muted-foreground">A registry on the LAN: the Node will allow plain HTTP for it.</p>}
          </div>
          {(depot.data?.length ?? 0) > 0 && (
            <div className="space-y-1.5">
              <Label>From your Depot</Label>
              <div className="flex flex-wrap gap-1.5">
                {depot.data!.map((d) => (
                  <button key={d.ref} type="button" onClick={() => setImage(d.ref)} className={cn("rounded-full border px-2.5 py-1 text-xs", image === d.ref ? "border-primary bg-primary text-primary-foreground" : "hover:bg-accent")} title={`${d.ref} on ${d.host}`}>
                    {d.tag}
                    {d.version && ` · ${d.version}`}
                  </button>
                ))}
              </div>
            </div>
          )}
          <label className="flex items-center justify-between gap-3 text-sm">
            <span>
              Restart as soon as it is staged
              <span className="block text-xs text-muted-foreground">Off: it waits for the next restart.</span>
            </span>
            <Switch checked={reboot} onCheckedChange={setReboot} />
          </label>
          {queue && <p className="text-xs text-muted-foreground">Another Node is updating. This one joins the queue and starts once none is.</p>}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={busy || !image.trim()}
            onClick={async () => {
              setBusy(true);
              await onRun(image.trim(), reboot, insecure);
              setBusy(false);
              onClose();
            }}
          >
            {queue ? "Queue update" : "Update"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Where this Node's job stands in the Console's queue, with Cancel. */
function QueuedLine({ job, queue }: { job: Job; queue: Job[] }) {
  if (job.phase !== "waiting") return null;
  const ahead = queue.filter((j) => j.id < job.id);
  const running = queue.find((j) => j.phase === "working" || j.phase === "down");
  const failed = queue.find((j) => j.phase === "failed");
  return (
    <p className="flex flex-wrap items-center gap-2 rounded-md bg-muted px-2 py-1.5 text-xs">
      <ListOrdered className="size-3.5 text-muted-foreground" />
      <span className="flex-1">
        Queued: {describe(job.m)}
        {ahead.length > 0 && `, ${ahead.length} ahead`}.{" "}
        <span className="text-muted-foreground">{failed ? `Waits until ${failed.name}'s failed ${describe(failed.m).split(" ")[0]} is dismissed.` : running ? `Starts once ${running.name} is done.` : "Starts once no Node is updating."}</span>
      </span>
      <Button size="sm" variant="outline" className="h-6 px-2 text-xs" onClick={() => remove(job.id)}>
        Cancel
      </Button>
    </p>
  );
}

/**
 * Replaces the details while this Node's update, rollback or restart runs:
 * the Console's queue (215) does the polling and hands back once the Node
 * answers again; a failure stays until dismissed, holding the queue.
 */
function MaintenanceCard({ job }: { job: Job }) {
  const { name, m, phase } = job;
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);
  const seconds = Math.max(0, Math.round((now - job.started) / 1000));
  const waiting = useUpdateQueue().filter((j) => j.phase === "waiting").length;
  const what = m.kind === "update" ? `Updating ${name} to ${m.target}` : m.kind === "rollback" ? `Rolling ${name} back` : `Restarting ${name}`;
  return (
    <div className="space-y-3">
      <h3 className="text-sm font-semibold">{what}</h3>
      {phase === "working" && (
        <>
          <div className="h-1.5 overflow-hidden rounded bg-muted">
            <div className="h-full rounded bg-status-starting transition-[width]" style={{ width: `${Math.round(job.progress * 100)}%` }} />
          </div>
          <p className="text-sm text-muted-foreground">{job.message || "starting"}</p>
        </>
      )}
      {phase === "down" && (
        <>
          <div className="h-1.5 overflow-hidden rounded bg-muted">
            <div className="h-full w-1/3 animate-pulse rounded bg-status-starting" />
          </div>
          <p className="text-sm">The Node is restarting. Waiting for it to answer again ({seconds}s).</p>
          <p className="text-xs text-muted-foreground">A Node normally takes under a minute. Its monitor shows the Maître banner when it is back.</p>
        </>
      )}
      {phase === "back" && <p className="text-sm">{name} is back.</p>}
      {phase === "failed" && (
        <>
          <p className="text-sm text-destructive">It failed: {job.message}</p>
          {waiting > 0 && <p className="text-xs text-muted-foreground">The {waiting === 1 ? "queued update waits" : `${waiting} queued updates wait`} until you dismiss this.</p>}
          <Button size="sm" variant="outline" onClick={() => remove(job.id)}>
            Back to the Node
          </Button>
        </>
      )}
      {phase !== "failed" && waiting > 0 && <p className="text-xs text-muted-foreground">{waiting} more queued after this.</p>}
    </div>
  );
}
