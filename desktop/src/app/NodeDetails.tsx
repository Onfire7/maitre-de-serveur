// A Node's details inside its card on the Nodes tab (decision 148): what it
// is, what it runs, its Maître Server image with update, roll back and the
// wait while it restarts. Every call here goes straight to the Node (160).
import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { invoke } from "@tauri-apps/api/core";
import { toast } from "sonner";
import { Copy, ArrowUpCircle, Undo2, Globe, Router, Network } from "lucide-react";
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
import type { Found } from "@/state";
import { ServiceIcon, StatusWord } from "./status";
import { useRoute } from "./routes";

export type Maintenance = { kind: "update"; target: string; reboot: boolean } | { kind: "rollback"; reboot: boolean } | { kind: "reboot" };

export function NodeDetails({ node, target, snap, unreachable }: { node: Node; target: Target | undefined; snap: FleetSnapshot; unreachable: boolean }) {
  const { go } = useRoute();
  const qc = useQueryClient();
  const client = useMemo(() => (target ? createClient(NodeService, transportFor(target)) : undefined), [target?.addr, target?.fingerprint]);
  const [maint, setMaint] = useState<Maintenance | null>(null);
  const info = useQuery({ queryKey: ["node-info", node.nodeId, target?.addr], enabled: !!client && !unreachable && !maint, refetchInterval: 5000, retry: false, queryFn: async () => (await client!.getNodeInfo({})).node });
  const upd = useQuery({ queryKey: ["update-status", node.nodeId, target?.addr], enabled: !!client && !unreachable && !maint, refetchInterval: 3000, retry: false, queryFn: async () => (await client!.getUpdateStatus({})).status });
  const services = useFleetServices(snap).filter((s) => s.spec.nodeId === node.nodeId);
  const i = info.data;
  const st = upd.data;
  const [updateOpen, setUpdateOpen] = useState(false);
  const [rollbackOpen, setRollbackOpen] = useState(false);

  async function run(fn: () => Promise<unknown>, m: Maintenance) {
    try {
      await fn();
      setMaint(m);
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    }
  }

  if (!target) return <p className="text-sm text-muted-foreground">This Node has no known address.</p>;
  if (maint && client)
    return (
      <MaintenanceCard
        client={client}
        name={node.name}
        m={maint}
        onDone={() => {
          setMaint(null);
          qc.invalidateQueries({ queryKey: ["node-info", node.nodeId] });
          qc.invalidateQueries({ queryKey: ["update-status", node.nodeId] });
          qc.invalidateQueries({ queryKey: ["fleet"] });
        }}
      />
    );

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
          <dd>{unreachable ? "unreachable" : i ? `${i.version}${i.devMode ? " (dev mode)" : ""}` : "…"}</dd>
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

      <section className="space-y-2 md:col-span-2">
        <div className="flex items-center gap-2">
          <h3 className="text-sm font-semibold">Maître Server</h3>
          {st?.state === UpdateStatus_State.UPDATING && <Badge className="bg-status-starting text-white">updating {Math.round(st.progress * 100)}%</Badge>}
          {st?.state === UpdateStatus_State.FAILED && <Badge variant="destructive">last update failed</Badge>}
          <span className="flex-1" />
          {st?.supported && !unreachable && (
            <>
              <Button size="sm" variant="outline" disabled={st.state === UpdateStatus_State.UPDATING} onClick={() => setUpdateOpen(true)}>
                <ArrowUpCircle /> Update…
              </Button>
              <Button size="sm" variant="outline" disabled={!st.rollback || st.state === UpdateStatus_State.UPDATING} onClick={() => setRollbackOpen(true)}>
                <Undo2 /> Roll back…
              </Button>
            </>
          )}
        </div>
        {upd.error && !unreachable && <p className="text-xs text-muted-foreground">This Node runs a Maître Server from before updates existed. Reinstall it from current media or over the network to manage its image here.</p>}
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
        {st?.staged && st.state !== UpdateStatus_State.UPDATING && <p className="text-xs text-muted-foreground">An update is staged. Restart the Node to run it.</p>}
        {st?.rollout && <p className="text-xs text-muted-foreground">Automatic updates: {st.rollout}.</p>}
        {st?.lastRollback && <p className="text-xs text-status-attention">The last update was rolled back: {st.lastRollback}. The Node runs the image from before it.</p>}
      </section>

      {client && st && <UpdateDialog open={updateOpen} onClose={() => setUpdateOpen(false)} status={st} onRun={(image, reboot, insecure) => run(() => client.updateImage({ image, reboot, insecureRegistry: insecure }), { kind: "update", target: image, reboot })} />}
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
              Hold to roll back and restart
            </HoldButton>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
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
function UpdateDialog({ open, onClose, status, onRun }: { open: boolean; onClose: () => void; status: UpdateStatus; onRun: (image: string, reboot: boolean, insecure: boolean) => Promise<void> }) {
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
            Update
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * Replaces the details while an update, rollback or restart runs. Polls the
 * Node, treats connection failures as "restarting", and hands back once the
 * Node answers again (or the update failed without a restart).
 */
function MaintenanceCard({ client, name, m, onDone }: { client: ReturnType<typeof createClient<typeof NodeService>>; name: string; m: Maintenance; onDone: () => void }) {
  const [status, setStatus] = useState<UpdateStatus | null>(null);
  const [phase, setPhase] = useState<"working" | "down" | "back" | "failed">("working");
  const [failure, setFailure] = useState("");
  const [seconds, setSeconds] = useState(0);
  const expectReboot = m.kind === "reboot" || m.reboot;
  useEffect(() => {
    let stop = false;
    let wentDown = false;
    const started = Date.now();
    const tick = async () => {
      if (stop) return;
      setSeconds(Math.round((Date.now() - started) / 1000));
      try {
        const st = (await client.getUpdateStatus({})).status;
        if (stop || !st) return;
        setStatus(st);
        if (wentDown) {
          setPhase("back");
          stop = true;
          setTimeout(onDone, 1500);
          return;
        }
        if (st.state === UpdateStatus_State.FAILED) {
          setPhase("failed");
          setFailure(st.message);
          stop = true;
          return;
        }
        if (st.state === UpdateStatus_State.REBOOTING) setPhase("down");
        else if (st.state === UpdateStatus_State.IDLE && !expectReboot) {
          setPhase("back");
          stop = true;
          setTimeout(onDone, 1500);
          return;
        }
      } catch {
        wentDown = true;
        setPhase("down");
      }
      if (!stop) setTimeout(tick, 1500);
    };
    tick();
    return () => {
      stop = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client]);
  const what = m.kind === "update" ? `Updating ${name} to ${m.target}` : m.kind === "rollback" ? `Rolling ${name} back` : `Restarting ${name}`;
  return (
    <div className="space-y-3">
      <h3 className="text-sm font-semibold">{what}</h3>
      {phase === "working" && (
        <>
          <div className="h-1.5 overflow-hidden rounded bg-muted">
            <div className="h-full rounded bg-status-starting transition-[width]" style={{ width: `${Math.round((status?.progress ?? 0) * 100)}%` }} />
          </div>
          <p className="text-sm text-muted-foreground">{status?.message || "starting"}</p>
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
          <p className="text-sm text-destructive">It failed: {failure}</p>
          <Button size="sm" variant="outline" onClick={onDone}>
            Back to the Node
          </Button>
        </>
      )}
    </div>
  );
}
