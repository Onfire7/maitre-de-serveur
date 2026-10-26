// The side sheets behind a Service's flyout (decision 143): Exposure
// (decisions 110-114), Port forwarding (115) and Storage (volumes and their
// Spaces). Each talks to the Node that runs the Service (160).
import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { toast } from "sonner";
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
import { PublicSettingsSchema } from "@/gen/maitre/v1/service_pb";
import { transportFor, type Target } from "@/transport";
import type { FleetService, FleetSnapshot } from "@/fleet/useFleet";
import { services as serviceClient } from "@/fleet/actions";
import { ServiceIcon, StatusWord } from "./status";
import { useRoute } from "./routes";

export type SheetKind = "exposure" | "ports" | "storage";

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

export function ExposureSheet({ service, target, snap, open, onClose }: { service: FleetService; target: Target | undefined; snap: FleetSnapshot | undefined; open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const { go } = useRoute();
  const info = useNodeInfo(target, open);
  const [choice, setChoice] = useState(service.spec.exposure || "private");
  const [host, setHost] = useState(service.spec.publicHost || "");
  const [ack, setAck] = useState(false);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) {
      setChoice(service.spec.exposure || "private");
      setHost(service.spec.publicHost || "");
      setAck(false);
    }
  }, [open, service.spec.exposure, service.spec.publicHost]);
  // Decision 114: one optional email for the whole Fleet, given to Let's Encrypt.
  const savedEmail = (() => {
    const r = snap?.records.find((r) => r.kind === "public_settings" && !r.deleted);
    return r ? fromBinary(PublicSettingsSchema, r.body).acmeEmail : "";
  })();
  const [email, setEmail] = useState(savedEmail);
  useEffect(() => setEmail(savedEmail), [savedEmail, open]);
  const emailOk = email.trim() === "" || /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim());
  async function saveEmail() {
    if (!snap) return;
    try {
      await createClient(NodeService, transportFor({ addr: snap.anchor.addr, fingerprint: snap.anchor.nodeId })).putRecord({ record: { kind: "public_settings", id: "fleet", body: toBinary(PublicSettingsSchema, create(PublicSettingsSchema, { acmeEmail: email.trim() })) } });
      toast.success("Certificate notices go to " + (email.trim() || "nobody"));
      qc.invalidateQueries({ queryKey: ["fleet"] });
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    }
  }
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
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="flex w-[480px] flex-col sm:max-w-[480px]">
        <Head service={service} title={`Where can ${name} be reached?`} description={service.status?.url ? `Now at ${service.status.url}${service.status.remoteUrl ? ` and ${service.status.remoteUrl}` : ""}` : "This Service has no web page."} />
        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-4 pb-4">
          <RadioGroup value={choice} onValueChange={setChoice} className="gap-2">
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
              <button className="underline" onClick={() => (onClose(), go({ name: "fleet", tab: "remote" }))}>
                Remote
              </button>{" "}
              tab.
            </p>
          )}
          {choice === "direct" && (
            <div className="space-y-3 rounded-lg border p-3 text-sm">
              <div className="space-y-1.5">
                <Label htmlFor="exp-host">Host name</Label>
                <Input id="exp-host" value={host} onChange={(e) => setHost(e.target.value.toLowerCase())} placeholder="photos.example.com" />
                {h && !hostOk && <p className="text-xs text-destructive">That is not a public host name.</p>}
              </div>
              <ol className="list-decimal space-y-1.5 pl-5 text-xs text-muted-foreground">
                <li>
                  At your domain's DNS, make an <b className="text-foreground">A record</b> for <code>{hostOk ? h : "the host name"}</code> pointing at <code>{info.data?.publicIp || "this Node's public address (still being looked up)"}</code>.
                </li>
                <li>
                  The Node asks your router to send ports <b className="text-foreground">80</b> and <b className="text-foreground">443</b> to it{portMapping.startsWith("router:") ? <>, found <b className="text-foreground">{portMapping.slice(8)}</b></> : portMapping ? <>: {portMapping}</> : ""}. If the router will not, forward those two ports to this Node at <code>{target?.addr}</code> yourself.
                  {service.status?.forwardState && <span className="block">Now: {service.status.forwardState}</span>}
                </li>
                <li>The Node then gets a certificate from Let's Encrypt on its own and keeps it renewed.</li>
              </ol>
              <p className="text-xs text-muted-foreground">If your home address changes, the record has to follow it; the Node says when the two no longer match.</p>
              <div className="space-y-1.5">
                <Label htmlFor="exp-email">Email for certificate notices</Label>
                <div className="flex gap-2">
                  <Input id="exp-email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@example.com" />
                  <Button variant="outline" disabled={!emailOk || email.trim() === savedEmail} onClick={saveEmail}>
                    Save
                  </Button>
                </div>
                {!emailOk && <p className="text-xs text-destructive">That is not an email address.</p>}
                <p className="text-xs text-muted-foreground">Optional, shared by all your addresses. Let's Encrypt writes there when a certificate is about to expire without renewing, and about policy changes. Nothing else.</p>
              </div>
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
        </div>
        <SheetFooter>
          <Button
            disabled={busy || !target || !changed || (isPublic && !ack) || (choice === "direct" && !hostOk)}
            onClick={async () => {
              setBusy(true);
              try {
                await serviceClient(target!).setExposure({ name, exposure: choice, publicHost: choice === "direct" ? h : "" });
                toast.success(`${name}: ${options.find((o) => o.id === choice)?.title.toLowerCase()}`);
                qc.invalidateQueries({ queryKey: ["fleet"] });
                onClose();
              } catch (e) {
                toast.error(String((e as Error).message ?? e));
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
                toast.error(String((e as Error).message ?? e));
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

export function StorageSheet({ service, snap, open, onClose }: { service: FleetService; snap: FleetSnapshot | undefined; open: boolean; onClose: () => void }) {
  const { go } = useRoute();
  const spaces = snap?.spaces ?? [];
  const spaceName = (id: string) => spaces.find((s) => s.spaceId === id)?.name ?? (id ? `${id} (removed Space)` : "no Space");
  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="flex w-[440px] flex-col sm:max-w-[440px]">
        <Head service={service} title={`${service.spec.name}'s storage`} description={service.spec.volumes.length ? `${service.spec.volumes.length} ${service.spec.volumes.length === 1 ? "volume" : "volumes"} on ${service.node?.name ?? "its Node"}` : "This Service keeps no data of its own."} />
        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-4 pb-4 text-sm">
          <ul className="divide-y rounded-lg border">
            {service.spec.volumes.map((v) => (
              <li key={v.name} className="flex items-center gap-3 px-3 py-2">
                <div className="min-w-0 flex-1">
                  <div className="font-medium">{v.name}</div>
                  <div className="truncate font-mono text-[11px] text-muted-foreground">{v.path}</div>
                </div>
                <Badge variant="secondary">{spaceName(service.spec.volumeSpaces[v.name])}</Badge>
                {v.size && <span className="text-xs text-muted-foreground">{v.size}</span>}
              </li>
            ))}
          </ul>
          <p className="text-xs text-muted-foreground">A volume lives in one Space, chosen at install. Moving it to another Space comes with the file manager; until then the Files tab shows what is in it.</p>
          <p className="text-xs">
            Status: <StatusWord word={service.word} />
          </p>
        </div>
        <SheetFooter>
          <Button variant="outline" onClick={() => (onClose(), go({ name: "fleet", tab: "files", files: { nodeId: service.spec.nodeId, spaceId: Object.values(service.spec.volumeSpaces)[0], path: service.spec.name } }))}>
            Open in Files
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}
