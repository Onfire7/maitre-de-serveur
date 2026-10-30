// Your own domain on the Remote tab (decision 218): the domain, the DNS
// records to make, and which Node the router forwards to for the app (7443)
// and for Services published at the domain (80 and 443, Caddy).
import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { create, toBinary } from "@bufbuild/protobuf";
import { toast } from "sonner";
import { Copy, MonitorSmartphone, Globe } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { PublicSettingsSchema } from "@/gen/maitre/v1/service_pb";
import { EntryNodeSchema, FleetDomainSchema, type EntryNode, type FleetDomain } from "@/gen/maitre/v1/fleet_pb";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { transportFor } from "@/transport";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { nodeWord } from "@/fleet/useFleet";
import { currentRecord, putSigned } from "@/fleet/sign";
import { APP_PORT, DOMAIN_KIND, acmeEmail, fleetDomain, holderOf, validDomain, type Role } from "@/fleet/domain";
import { NodeMark } from "./status";

const AUTO = "auto";

export function DomainSection({ snap }: { snap: FleetSnapshot | undefined }) {
  const qc = useQueryClient();
  const stored = fleetDomain(snap?.records) ?? create(FleetDomainSchema, {});
  const [domain, setDomain] = useState(stored.domain);
  useEffect(() => setDomain(stored.domain), [stored.domain]);
  const d = domain.trim().toLowerCase().replace(/\.$/, "");
  const valid = validDomain(d);
  // The home's public address, as the anchor learned it.
  const info = useQuery({
    queryKey: ["node-info", snap?.anchor.nodeId, snap?.anchor.addr],
    enabled: !!snap,
    refetchInterval: 30_000,
    retry: false,
    queryFn: async () => (await createClient(NodeService, transportFor({ addr: snap!.anchor.addr, fingerprint: snap!.anchor.nodeId })).getNodeInfo({})).node,
  });
  const ip = info.data?.publicIp ?? "";

  async function save(next: FleetDomain) {
    if (!snap) return;
    try {
      await putSigned({ addr: snap.anchor.addr, fingerprint: snap.anchor.nodeId }, { kind: DOMAIN_KIND, id: "fleet", body: toBinary(FleetDomainSchema, next) }, currentRecord(snap.records, DOMAIN_KIND, "fleet"));
      qc.invalidateQueries({ queryKey: ["fleet"] });
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    }
  }
  const setEntry = (role: Role, e: Partial<EntryNode>) => {
    const cur = stored[role] ?? create(EntryNodeSchema, {});
    void save(create(FleetDomainSchema, { ...stored, [role]: create(EntryNodeSchema, { ...cur, ...e }) }));
  };
  const copy = (s: string) => navigator.clipboard?.writeText(s).then(() => toast("Copied"));

  return (
    <section className="space-y-3">
      <div>
        <h2 className="text-base font-semibold">Your own domain</h2>
        <p className="text-sm text-muted-foreground">A domain you own, pointed at your home, reaches the Fleet from anywhere without Tailscale: this app at the domain, and Services you publish at their own name under it.</p>
      </div>
      <div className="space-y-4 rounded-xl border bg-card p-4">
        <div className="flex flex-wrap items-end gap-2">
          <div className="min-w-64 flex-1 space-y-1.5">
            <Label htmlFor="fleet-domain">Domain</Label>
            <Input id="fleet-domain" value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="home.example.com" className="font-mono text-sm" />
          </div>
          {d !== stored.domain && (
            <>
              <Button variant="outline" onClick={() => setDomain(stored.domain)}>
                Cancel
              </Button>
              <Button disabled={!!d && !valid} onClick={() => save(create(FleetDomainSchema, { ...stored, domain: d }))}>
                {d ? "Save" : "Remove"}
              </Button>
            </>
          )}
        </div>
        {d && !valid && <p className="text-xs text-destructive">A name on the internet you own, such as home.example.com (not .local or .lan).</p>}
        {stored.domain && (
          <div className="space-y-1.5">
            <p className="text-sm">At your domain's DNS provider, make two records pointing at your home:</p>
            <div className="overflow-hidden rounded-lg border text-sm">
              {[stored.domain, `*.${stored.domain}`].map((name) => (
                <div key={name} className="flex items-center gap-3 border-b px-3 py-1.5 last:border-b-0">
                  <span className="min-w-0 flex-1 truncate font-mono text-xs">{name}</span>
                  <span className="w-8 text-xs text-muted-foreground">A</span>
                  <span className="w-32 font-mono text-xs">{ip || "…"}</span>
                  <button className="rounded p-1 text-muted-foreground hover:text-foreground" title={`Copy ${name}`} onClick={() => copy(name)}>
                    <Copy className="size-3.5" />
                  </button>
                </div>
              ))}
            </div>
            <p className="text-xs text-muted-foreground">
              {ip ? `${ip} is your home's address on the internet today.` : "Your home's address on the internet shows here once a Node has looked it up."} If your provider changes it, the Node holding the forward below says the records no longer point home.
            </p>
          </div>
        )}
        <CertEmail snap={snap} />
      </div>
      <EntryCard
        role="app"
        icon={<MonitorSmartphone className="size-4" />}
        title="This app away from home"
        detail={`Your router forwards port ${APP_PORT} to one Node, so Maître reaches the Fleet at ${stored.domain || "your domain"} when it is not at home.`}
        snap={snap}
        entry={stored.app}
        disabled={!stored.domain}
        onChange={(e) => setEntry("app", e)}
      />
      <EntryCard
        role="web"
        icon={<Globe className="size-4" />}
        title="Services at your domain"
        detail={`Your router forwards ports 80 and 443 to one Node. Its Caddy gets the certificates for every Service published at ${stored.domain ? `name.${stored.domain}` : "your domain"} and passes each request to the Node that runs it.`}
        snap={snap}
        entry={stored.web}
        disabled={!stored.domain}
        onChange={(e) => setEntry("web", e)}
      />
    </section>
  );
}

function EntryCard({ role, icon, title, detail, snap, entry, disabled, onChange }: { role: Role; icon: React.ReactNode; title: string; detail: string; snap: FleetSnapshot | undefined; entry: EntryNode | undefined; disabled: boolean; onChange: (e: Partial<EntryNode>) => void }) {
  const on = !!entry?.enabled;
  const holder = holderOf(snap, role);
  const nodes = [...(snap?.nodes ?? [])].sort((a, b) => a.name.localeCompare(b.name));
  const name = (id: string) => nodes.find((n) => n.nodeId === id)?.name ?? id.slice(0, 8);
  const pinned = entry?.nodeId ?? "";
  return (
    <div className="space-y-3 rounded-xl border bg-card p-4">
      <label className="flex items-start justify-between gap-4">
        <span>
          <span className="flex items-center gap-2 text-sm font-medium">
            {icon} {title}
          </span>
          <span className="block text-xs text-muted-foreground">{detail}</span>
        </span>
        <Switch checked={on} disabled={disabled} onCheckedChange={(v) => onChange({ enabled: v })} />
      </label>
      {on && (
        <>
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <Label>Forward to</Label>
            <Select value={pinned || AUTO} onValueChange={(v) => onChange({ nodeId: v === AUTO ? "" : v })}>
              <SelectTrigger size="sm" className="w-56" aria-label={`${title}: forward to`}>
                <SelectValue>{pinned ? name(pinned) : "The least busy Node"}</SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={AUTO}>The least busy Node</SelectItem>
                {nodes.map((n) => (
                  <SelectItem key={n.nodeId} value={n.nodeId}>
                    {n.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <span className="text-xs text-muted-foreground">{pinned ? "while it is reachable; else the least busy" : "kept until it goes away or stays much busier"}</span>
          </div>
          {holder ? (
            <p className="flex items-start gap-2 rounded-md bg-muted px-2 py-1.5 text-xs">
              <NodeMark word={nodeWord(snap?.status.get(holder.nodeId), Date.now())} size={18} />
              <span>
                <span className="font-medium">{name(holder.nodeId)}</span> holds it{holder.state ? ` · ${holder.state}` : ""}.
              </span>
            </p>
          ) : (
            <p className="text-xs text-muted-foreground">No Node has taken it yet. It takes a few seconds, and Nodes need a Maître Server from this version on.</p>
          )}
        </>
      )}
    </div>
  );
}

/** One optional email for the whole Fleet, given to Let's Encrypt (decisions 114, 220). */
function CertEmail({ snap }: { snap: FleetSnapshot | undefined }) {
  const qc = useQueryClient();
  const saved = acmeEmail(snap?.records);
  const [email, setEmail] = useState(saved);
  useEffect(() => setEmail(saved), [saved]);
  const e = email.trim();
  const ok = e === "" || /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(e);
  async function save() {
    if (!snap) return;
    try {
      await putSigned({ addr: snap.anchor.addr, fingerprint: snap.anchor.nodeId }, { kind: "public_settings", id: "fleet", body: toBinary(PublicSettingsSchema, create(PublicSettingsSchema, { acmeEmail: e })) }, currentRecord(snap.records, "public_settings", "fleet"));
      toast.success(e ? `Certificate notices go to ${e}` : "No email for certificate notices");
      qc.invalidateQueries({ queryKey: ["fleet"] });
    } catch (err) {
      toast.error(String((err as Error).message ?? err));
    }
  }
  return (
    <div className="space-y-1.5 border-t pt-4">
      <div className="flex flex-wrap items-end gap-2">
        <div className="min-w-64 flex-1 space-y-1.5">
          <Label htmlFor="acme-email">Email for certificate notices</Label>
          <Input id="acme-email" type="email" value={email} onChange={(ev) => setEmail(ev.target.value)} placeholder="you@example.com" className="text-sm" />
        </div>
        {e !== saved && (
          <>
            <Button variant="outline" onClick={() => setEmail(saved)}>
              Cancel
            </Button>
            <Button disabled={!ok} onClick={save}>
              {e ? "Save" : "Remove"}
            </Button>
          </>
        )}
      </div>
      {!ok && <p className="text-xs text-destructive">That is not an email address.</p>}
      <p className="text-xs text-muted-foreground">Optional, for every Service published at its own address. Let's Encrypt writes there when a certificate is about to expire without renewing, and about policy changes. Nothing else.</p>
    </div>
  );
}
