// Changing a Service's Access (decisions 110-114, renamed in 194) and
// Storage (volumes and their Spaces) in place, in its open row on the
// Services tab; Port forwarding (115) and Root powers keep their side
// sheets. Each talks to the Node that runs the Service (160).
import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { toast } from "sonner";
import { FolderSearch } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Checkbox } from "@/components/ui/checkbox";
import { Badge } from "@/components/ui/badge";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { cn } from "@/lib/utils";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { FilesService } from "@/gen/maitre/v1/files_pb";
import { transportFor, type Target } from "@/transport";
import type { FleetService, FleetSnapshot } from "@/fleet/useFleet";
import { services as serviceClient } from "@/fleet/actions";
import { declined, grantsOf, optionNote, revokeGrant } from "@/fleet/grants";
import { ServiceIcon } from "./status";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { spacesOf } from "@/spaces";
import { useRoute } from "./routes";
import { FoldersEditor, type Folders } from "@/files/FoldersEditor";
import { folderSource, ownSpacesOn } from "@/files/sources";
import { acmeEmail, fleetDomain, fleetDomainName } from "@/fleet/domain";

export type SheetKind = "ports" | "grants";

function Head({ service, title, description }: { service: FleetService; title: string; description: string }) {
  return (
    <SheetHeader className="pr-10">
      <div className="flex items-center gap-3">
        <ServiceIcon catalogId={service.spec.catalogId} word={service.word} size={40} />
        <div>
          <SheetTitle>{title}</SheetTitle>
          <SheetDescription>{description}</SheetDescription>
        </div>
      </div>
    </SheetHeader>
  );
}

function useNodeInfo(target: Target | undefined, enabled: boolean) {
  return useQuery({ queryKey: ["node-info", target?.fingerprint, target?.addr], enabled: !!target && enabled, refetchInterval: 5000, retry: false, queryFn: async () => (await createClient(NodeService, transportFor(target!)).getNodeInfo({})).node });
}

/** Who can reach a Service: the choice as it is, changed in place (194, 195). */
export function AccessEditor({ service, target, snap }: { service: FleetService; target: Target | undefined; snap: FleetSnapshot | undefined }) {
  const qc = useQueryClient();
  const { go } = useRoute();
  const info = useNodeInfo(target, true);
  // Decision 220: under the Fleet's domain by default, so there is nothing to type.
  const domain = fleetDomain(snap?.records)?.domain || fleetDomainName();
  const defaultHost = domain ? `${service.spec.name}.${domain}` : "";
  const [choice, setChoice] = useState(service.spec.exposure || "private");
  const [host, setHost] = useState(service.spec.publicHost || defaultHost);
  const [ack, setAck] = useState(false);
  const [busy, setBusy] = useState(false);
  const reset = () => {
    setChoice(service.spec.exposure || "private");
    setHost(service.spec.publicHost || defaultHost);
    setAck(false);
  };
  // Changed elsewhere (or applied here): show what it is now.
  useEffect(reset, [service.spec.name, service.spec.exposure, service.spec.publicHost, defaultHost]);
  const email = acmeEmail(snap?.records);
  const name = service.spec.name;
  const remoteReady = info.data?.tailscale?.state === "Running";
  const canPublic = !service.spec.infrastructure;
  const isPublic = choice === "public" || choice === "direct";
  const h = host.trim().toLowerCase();
  const hostOk = /^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/.test(h) && !/\.(local|localhost|lan|internal|home\.arpa)$/.test(h);
  const changed = choice !== (service.spec.exposure || "private") || (choice === "direct" && h !== service.spec.publicHost);
  const portMapping = info.data?.portMapping ?? "";
  const options: { id: string; title: string; detail: string; disabled?: boolean; why?: string }[] = [
    { id: "private", title: "At home only", detail: "Devices on your network, and this app." },
    { id: "remote", title: "From anywhere, for you", detail: "Your devices on Tailscale, wherever they are. Nobody else.", disabled: !remoteReady, why: "connect the Fleet to Tailscale first" },
    { id: "public", title: "Public, at a Tailscale address", detail: "Anyone on the internet with the link; nothing to set up.", disabled: !remoteReady || !canPublic, why: !canPublic ? "network infrastructure stays at home" : "connect the Fleet to Tailscale first" },
    { id: "direct", title: "Public, at your own address", detail: "A name you own, such as photos.example.com, with a certificate the Node gets by itself. No Tailscale needed.", disabled: !canPublic, why: "network infrastructure stays at home" },
  ];
  return (
        <div className="max-w-2xl space-y-4">
          <RadioGroup value={choice} onValueChange={setChoice} className="grid gap-2 sm:grid-cols-2" aria-label={`Who can reach ${name}`}>
            {options.map((o) => (
              <label key={o.id} className={cn("flex cursor-pointer items-start gap-3 rounded-lg border p-3", choice === o.id && "border-primary", o.disabled && "cursor-not-allowed opacity-60")}>
                <RadioGroupItem value={o.id} disabled={o.disabled} className="mt-0.5" />
                <span className="text-sm">
                  <span className="font-medium">{o.title}</span>
                  <span className="block text-xs text-muted-foreground">
                    {o.detail}
                    {o.disabled && o.why && <> ({o.why})</>}
                  </span>
                </span>
              </label>
            ))}
          </RadioGroup>
          {!remoteReady && (
            <p className="text-xs text-muted-foreground">
              Tailscale is set up on the{" "}
              <button className="underline" onClick={() => go({ name: "fleet", tab: "remote" })}>
                Remote
              </button>{" "}
              tab.
            </p>
          )}
          {choice === "direct" && (
            <div className="space-y-3 rounded-lg border p-3 text-sm">
              <div className="space-y-1.5">
                <Label htmlFor="exp-host">Host name</Label>
                <Input id="exp-host" value={host} onChange={(e) => setHost(e.target.value.toLowerCase())} placeholder={defaultHost || "photos.example.com"} />
                {h && !hostOk && <p className="text-xs text-destructive">That is not a public host name.</p>}
                {domain && h !== defaultHost && hostOk && (
                  <button className="text-xs text-muted-foreground underline" onClick={() => setHost(defaultHost)}>
                    Use {defaultHost}
                  </button>
                )}
              </div>
              <ol className="list-decimal space-y-1.5 pl-5 text-xs text-muted-foreground">
                {domain && h.endsWith(`.${domain}`) ? (
                  <li>
                    The <code>*.{domain}</code> record you made for your domain already points <code>{h}</code> home.
                  </li>
                ) : (
                  <li>
                    At your domain's DNS, make an <b className="text-foreground">A record</b> for <code>{hostOk ? h : "the host name"}</code> pointing at <code>{info.data?.publicIp || "this Node's public address (still being looked up)"}</code>.
                  </li>
                )}
                <li>
                  The Node asks your router to send ports <b className="text-foreground">80</b> and <b className="text-foreground">443</b> to it{portMapping.startsWith("router:") ? <>, found <b className="text-foreground">{portMapping.slice(8)}</b></> : portMapping ? <>: {portMapping}</> : ""}. If the router will not, forward those two ports to this Node at <code>{target?.addr}</code> yourself.
                  {service.status?.forwardState && <span className="block">Now: {service.status.forwardState}</span>}
                </li>
                <li>The Node then gets a certificate from Let's Encrypt on its own and keeps it renewed.</li>
              </ol>
              <p className="text-xs text-muted-foreground">If your home address changes, the record has to follow it; the Node says when the two no longer match.</p>
              <p className="text-xs text-muted-foreground">
                {email ? (
                  <>
                    Let's Encrypt writes to <b className="text-foreground">{email}</b> about certificates.
                  </>
                ) : (
                  "No email is set for certificate notices."
                )}{" "}
                <button className="underline" onClick={() => go({ name: "fleet", tab: "remote" })}>
                  {email ? "Change it" : "Set one"}
                </button>{" "}
                with your domain on the Remote tab.
              </p>
            </div>
          )}
          {isPublic && (
            <div className="space-y-2 rounded-lg border border-destructive/60 bg-destructive/5 p-3 text-sm">
              <p>
                <b>Public means public.</b> Anyone who finds the address can open {name}'s page. If it has no login of its own, or a weak one, strangers can use it, read what is in it, or change it. Bugs in {name} become reachable by everyone, not only by you.
              </p>
              <label className="flex items-center gap-2">
                <Checkbox checked={ack} onCheckedChange={(v) => setAck(v === true)} /> I understand; {name} has a login I trust.
              </label>
            </div>
          )}
          {changed && (
          <div className="flex items-center gap-2">
          <Button
            size="sm"
            disabled={busy || !target || service.nodeUnreachable || !changed || (isPublic && !ack) || (choice === "direct" && !hostOk)}
            onClick={async () => {
              setBusy(true);
              try {
                await serviceClient(target!).setExposure({ name, exposure: choice, publicHost: choice === "direct" ? h : "" });
                toast.success(`${name}: ${options.find((o) => o.id === choice)?.title.toLowerCase()}`);
                qc.invalidateQueries({ queryKey: ["fleet"] });
              } catch (e) {
                if (!declined(e)) toast.error(String((e as Error).message ?? e));
              } finally {
                setBusy(false);
              }
            }}
          >
            Apply
          </Button>
          <Button size="sm" variant="ghost" onClick={reset}>
            Cancel
          </Button>
          </div>
          )}
        </div>
  );
}

export function PortForwardSheet({ service, target, open, onClose }: { service: FleetService; target: Target | undefined; open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const info = useNodeInfo(target, open);
  const [on, setOn] = useState(!!service.spec.forwardPorts);
  const [ack, setAck] = useState(!!service.spec.forwardPorts);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) {
      setOn(!!service.spec.forwardPorts);
      setAck(!!service.spec.forwardPorts);
    }
  }, [open, service.spec.forwardPorts]);
  const name = service.spec.name;
  const ports = service.spec.hostPorts.join(", ");
  const portMapping = info.data?.portMapping ?? "";
  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="flex w-[440px] flex-col sm:max-w-[440px]">
        <Head service={service} title={`Can people on the internet reach ${name}?`} description={`${name} listens on ${ports}.`} />
        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-4 pb-4 text-sm">
          <p className="text-muted-foreground">
            On your network it is reachable at <code>{target?.addr.replace(/:\d+$/, "")}</code> already. Turning this on asks your router to send those ports from the internet to this Node{portMapping.startsWith("router:") ? <>, found <b className="text-foreground">{portMapping.slice(8)}</b></> : portMapping ? <>: {portMapping}</> : ""}.
          </p>
          <label className="flex items-center justify-between gap-3 rounded-lg border p-3">
            <span>
              <span className="font-medium">Reachable from the internet</span>
              <span className="block text-xs text-muted-foreground">At your public address{info.data?.publicIp ? `, ${info.data.publicIp}` : ""}.</span>
            </span>
            <Switch checked={on} onCheckedChange={setOn} />
          </label>
          {on && (
            <div className="space-y-2 rounded-lg border border-destructive/60 bg-destructive/5 p-3">
              <p>
                <b>Open to everyone.</b> Anyone who finds your address can connect to {name}. Use its own allow-list or password if it has one, keep it updated, and turn this off when you stop playing.
              </p>
              <label className="flex items-center gap-2">
                <Checkbox checked={ack} onCheckedChange={(v) => setAck(v === true)} /> I understand.
              </label>
            </div>
          )}
          {service.status?.forwardState && (
            <p className="text-xs text-muted-foreground">
              Now: <Badge variant="secondary">{service.status.forwardState}</Badge>
            </p>
          )}
        </div>
        <SheetFooter>
          <Button
            disabled={busy || !target || on === !!service.spec.forwardPorts || (on && !ack)}
            onClick={async () => {
              setBusy(true);
              try {
                await serviceClient(target!).setPortForwarding({ name, enabled: on });
                toast.success(on ? `${name}: the Node is asking the router` : `${name}: no longer reachable from the internet`);
                qc.invalidateQueries({ queryKey: ["fleet"] });
                onClose();
              } catch (e) {
                if (!declined(e)) toast.error(String((e as Error).message ?? e));
              } finally {
                setBusy(false);
              }
            }}
          >
            Apply
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}

/** Where a Service's data lives: the folders as they are, changed in place (194, 195). */
export function StorageEditor({ service, target, snap }: { service: FleetService; target: Target | undefined; snap: FleetSnapshot | undefined }) {
  const { go } = useRoute();
  const qc = useQueryClient();
  const spaces = snap?.spaces ?? [];
  const spaceName = (id: string) => spaces.find((s) => s.spaceId === id)?.name ?? (id ? `${id} (removed Space)` : "no Space");
  // Spaces with a Pool on the Service's Node: the ones a volume can move to.
  const here = spaces.filter((sp) => sp.spanNodes || (snap?.drives ?? []).some((d) => d.nodeId === service.spec.nodeId && spacesOf(d, spaces).includes(sp.spaceId)));
  // Its own folders move only to Spaces with a Drive on its Node (208).
  const ownHere = ownSpacesOn(snap, service.spec.nodeId);
  const [picked, setPicked] = useState<Record<string, string>>({});
  const [moving, setMoving] = useState<string | null>(null);
  // Each volume's move job, followed until it ends (decision 207): a move
  // that fails leaves the volume where it was, and says why here.
  const [jobs, setJobs] = useState<Record<string, { jobId: string; to: string }>>({});
  const [failed, setFailed] = useState<Record<string, string>>({});
  const jobList = useQuery({
    queryKey: ["volume-move-jobs", target?.addr],
    enabled: !!target && Object.keys(jobs).length > 0,
    refetchInterval: 1500,
    queryFn: async () => (await createClient(FilesService, transportFor(target!)).listJobs({})).jobs,
  });
  useEffect(() => {
    for (const [volume, { jobId, to }] of Object.entries(jobs)) {
      const j = jobList.data?.find((x) => x.jobId === jobId);
      if (!j || j.state === "queued" || j.state === "running") continue;
      setJobs(({ [volume]: _, ...rest }) => rest);
      qc.invalidateQueries({ queryKey: ["fleet"] });
      if (j.state === "done") toast.success(`${volume} is in ${spaceName(to)} now`);
      else {
        const why = j.message || j.state;
        setFailed((f) => ({ ...f, [volume]: why }));
        toast.error(`${volume} did not move to ${spaceName(to)}`, { description: why });
      }
    }
  }, [jobList.data]);
  // Decisions 170, 171, 174: folders are edited here and saved together.
  const saved: Folders = useMemo(() => ({ volumeSpaces: { ...service.spec.volumeSpaces }, volumeFolders: { ...service.spec.volumeFolders }, mounts: service.spec.mounts.map((m) => ({ spaceId: m.spaceId, folder: m.folder, path: m.path, readOnly: m.readOnly })) }), [service.spec]);
  const [folders, setFolders] = useState<Folders>(saved);
  const [saving, setSaving] = useState(false);
  const savedKey = JSON.stringify(saved);
  const dirty = JSON.stringify(folders) !== savedKey;
  // Changed elsewhere (or saved here): show what it is now.
  useEffect(() => setFolders(saved), [service.spec.name, savedKey]);
  const files = (at?: { service: string; folder?: string }) => go({ name: "fleet", tab: "files", files: { nodeId: service.spec.nodeId, service: service.spec.name, ...at } });
  const running = service.status?.state === 2 || service.status?.state === 3;
  async function saveFolders() {
    if (!target) return;
    setSaving(true);
    try {
      await serviceClient(target).setFolders({ name: service.spec.name, volumeFolders: folders.volumeFolders, volumeSpaces: Object.fromEntries(Object.keys(folders.volumeFolders).map((v) => [v, folders.volumeSpaces[v]])), mounts: folders.mounts });
      toast.success(`${service.spec.name}'s folders saved`, { description: "It restarts with them if it was running." });
      qc.invalidateQueries({ queryKey: ["fleet"] });
    } catch (e) {
      if (declined(e)) return;
      toast.error(e instanceof ConnectError && e.code === Code.Unimplemented ? `${service.node?.name ?? "Its Node"} runs an older Maître Server; update it to change folders.` : String((e as Error).message ?? e));
    } finally {
      setSaving(false);
    }
  }
  async function move(volume: string) {
    const to = picked[volume];
    if (!to || !target) return;
    setMoving(volume);
    try {
      const { jobId } = await serviceClient(target).moveVolume({ name: service.spec.name, volume, toSpace: to });
      setJobs((m) => ({ ...m, [volume]: { jobId, to } }));
      setFailed(({ [volume]: _, ...rest }) => rest);
      toast(`${service.spec.name} stops while ${volume} moves to ${spaceName(to)}`);
      qc.invalidateQueries({ queryKey: ["jobs"] });
      qc.invalidateQueries({ queryKey: ["fleet"] });
      setPicked((p) => ({ ...p, [volume]: "" }));
    } catch (e) {
      if (!declined(e)) toast.error(String((e as Error).message ?? e));
    } finally {
      setMoving(null);
    }
  }
  return (
        <div className="max-w-2xl space-y-4">
          <FoldersEditor volumes={service.spec.volumes} spaces={here} value={folders} onChange={setFolders} source={(sp) => folderSource(snap, service.spec.nodeId, sp)}
            serviceName={service.spec.name}
            lockOwnSpace
            onOpen={(at) => files({ service: service.spec.name, folder: "volume" in at ? at.volume : `mount:${at.mount}` })}
            ownExtra={(v) => {
              // An own folder changes Space by moving its data (MoveVolume).
              if (ownHere.length < 2 || v in folders.volumeFolders) return null;
              const cur = service.spec.volumeSpaces[v];
              const to = picked[v] ?? "";
              const job = jobs[v] && jobList.data?.find((x) => x.jobId === jobs[v].jobId);
              if (jobs[v])
                return (
                  <div className="space-y-1 text-xs">
                    <div>
                      Moving to {spaceName(jobs[v].to)}
                      {job && job.bytesTotal > 0n ? `: ${Math.round((Number(job.bytesDone) / Number(job.bytesTotal)) * 100)}%` : "…"}
                    </div>
                    <div className="h-1.5 overflow-hidden rounded bg-muted">
                      <div className="h-full rounded bg-primary transition-[width]" style={{ width: `${job && job.bytesTotal > 0n ? Math.round((Number(job.bytesDone) / Number(job.bytesTotal)) * 100) : 5}%` }} />
                    </div>
                  </div>
                );
              return (
                <div className="space-y-1">
                {failed[v] && <p className="rounded-md border border-destructive/60 bg-destructive/5 px-2 py-1 text-xs">The last move failed and it stayed in {spaceName(cur)}: {failed[v]}</p>}
                <div className="flex items-center gap-2">
                  <Select value={to || "stay"} onValueChange={(val) => setPicked((p) => ({ ...p, [v]: val === "stay" ? "" : val }))} disabled={service.nodeUnreachable || moving !== null}>
                    <SelectTrigger size="sm" className="flex-1">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="stay">Keep in {spaceName(cur)}</SelectItem>
                      {ownHere
                        .filter((sp) => sp.spaceId !== cur)
                        .map((sp) => (
                          <SelectItem key={sp.spaceId} value={sp.spaceId}>
                            Move the data to {sp.name}
                          </SelectItem>
                        ))}
                    </SelectContent>
                  </Select>
                  <Button size="sm" disabled={!to || !target || moving !== null} onClick={() => move(v)}>
                    {moving === v ? "Moving…" : "Move"}
                  </Button>
                </div>
                </div>
              );
            }}
          />
          <p className="text-xs text-muted-foreground">The Service's folder lives in one Space on {service.node?.name ?? "its Node"}. Moving it to another Space stops the Service, copies and checks the folder, then starts it again; the Files tab shows progress. Shared folders are never moved or deleted with the Service.</p>
          <div className="flex items-center gap-2">
            {dirty && (
              <>
                <Button size="sm" disabled={saving || !target || service.nodeUnreachable} onClick={saveFolders}>
                  {saving ? "Saving…" : running ? "Save and restart" : "Save"}
                </Button>
                <Button size="sm" variant="ghost" onClick={() => setFolders(saved)}>
                  Cancel
                </Button>
              </>
            )}
            <Button size="sm" variant="outline" className="ml-auto" onClick={() => files()}>
              <FolderSearch /> Open in Files
            </Button>
          </div>
        </div>
  );
}

/**
 * The protected options a Service was granted (decision 189), each with
 * Revoke: a signed tombstone. The Node then refuses to start the Service
 * with that option until it is granted again.
 */
export function GrantsSheet({ service, target, snap, open, onClose }: { service: FleetService; target: Target | undefined; snap: FleetSnapshot | undefined; open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState("");
  const grants = grantsOf(snap?.records, service.spec.name);
  const via = target ?? (snap ? { addr: snap.anchor.addr, fingerprint: snap.anchor.nodeId } : undefined);
  const nodeName = (id: string) => snap?.nodes.find((n) => n.nodeId === id)?.name ?? id.slice(0, 8);
  async function revoke(g: (typeof grants)[number]) {
    if (!via) return toast.error("no Node of the Fleet can be reached");
    setBusy(g.record.id);
    try {
      await revokeGrant(via, g.record);
      toast.success(`${service.spec.name} no longer has ${g.grant.option}`, { description: "It keeps running until it restarts; then its Node refuses it without that option." });
      qc.invalidateQueries({ queryKey: ["fleet"] });
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    } finally {
      setBusy("");
    }
  }
  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="flex w-[480px] flex-col sm:max-w-[480px]">
        <Head service={service} title={`What ${service.spec.name} may do to its Node`} description="Options that reach past the container's sandbox, each allowed by a Trusted Device." />
        <div className="min-h-0 flex-1 space-y-3 overflow-y-auto px-4 pb-4">
          {grants.length === 0 && <p className="text-sm text-muted-foreground">Nothing: {service.spec.name} runs with Maître's limits on containers.</p>}
          <ul className="space-y-2">
            {grants.map((g) => (
              <li key={g.record.id} className="rounded-lg border p-3 text-sm">
                <div className="flex items-start gap-3">
                  <div className="min-w-0 flex-1">
                    {g.grant.component && <span className="mb-1 block text-xs font-medium">{g.grant.component}</span>}
                    <code className="block break-all font-mono text-xs">{g.grant.option}</code>
                    <span className="mt-1 block text-muted-foreground">{optionNote(g.grant.option)}</span>
                  </div>
                  <Button size="sm" variant="outline" disabled={busy === g.record.id} onClick={() => revoke(g)}>
                    Revoke
                  </Button>
                </div>
                <div className="mt-2 text-xs text-muted-foreground">
                  On {nodeName(g.grant.nodeId)} for {g.grant.imageRepository} · allowed by {g.grant.deviceName || "a Trusted Device"} on {new Date(Number(g.grant.grantedUnixMs)).toLocaleDateString()}
                </div>
              </li>
            ))}
          </ul>
        </div>
      </SheetContent>
    </Sheet>
  );
}
