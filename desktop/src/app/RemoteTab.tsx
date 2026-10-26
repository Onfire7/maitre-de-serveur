// The Remote tab (Milestone 5, decision 148): the Fleet on Tailscale. One
// OAuth client for the whole Fleet; every Node joins the tailnet by itself.
import { useState } from "react";
import { useQueries, useQueryClient } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { toast } from "sonner";
import { Network, Globe, ExternalLink, KeyRound } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { TailscaleConfigSchema } from "@/gen/maitre/v1/tailscale_pb";
import { transportFor } from "@/transport";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { useFleetServices } from "@/fleet/useFleet";
import { targetFor } from "@/fleet/actions";
import type { ConsoleState } from "@/state";
import { ServiceIcon, StatusWord } from "./status";
import { useRoute } from "./routes";

export function RemoteTab({ snap, state }: { snap: FleetSnapshot | undefined; state: ConsoleState }) {
  const { go } = useRoute();
  const [connect, setConnect] = useState(false);
  const rec = snap?.records.find((r) => r.kind === "tailscale" && !r.deleted);
  const config = rec ? fromBinary(TailscaleConfigSchema, rec.body) : undefined;
  const nodes = snap?.nodes ?? [];
  const infos = useQueries({
    queries: nodes.map((n) => {
      const t = targetFor(snap, state.nodes, n.nodeId);
      return { queryKey: ["node-info", n.nodeId, t?.addr], enabled: !!t && !!config, refetchInterval: 5000, retry: false, queryFn: async () => (await createClient(NodeService, transportFor(t!)).getNodeInfo({})).node };
    }),
  });
  const remote = useFleetServices(snap).filter((s) => s.spec.exposure && s.spec.exposure !== "private");
  const words: Record<string, string> = { remote: "for you, anywhere", public: "public, Tailscale address", direct: "public, own address" };
  return (
    <div className="max-w-2xl space-y-8">
      <section className="space-y-3">
        <div className="flex items-start justify-between gap-4">
          <div>
            <h2 className="text-base font-semibold">Reach the Fleet from anywhere</h2>
            <p className="text-sm text-muted-foreground">Each Node joins your Tailscale network, so this app and Remote Services work away from home.</p>
          </div>
          <Button size="sm" variant={config ? "outline" : "default"} onClick={() => setConnect(true)}>
            <KeyRound /> {config ? "Replace the credentials…" : "Connect to Tailscale…"}
          </Button>
        </div>
        {config ? (
          <div className="rounded-xl border bg-card">
            <div className="flex items-center gap-2 border-b px-4 py-2.5 text-sm">
              <Network className="size-4 text-muted-foreground" />
              <span className="font-medium">Connected</span>
              <span className="text-xs text-muted-foreground">
                {config.tag} · client {config.oauthClientId.slice(0, 12)}… · since {new Date(Number(config.addedUnixMs)).toLocaleDateString()}
              </span>
            </div>
            <ul className="divide-y">
              {nodes.map((n, i) => {
                const ts = infos[i]?.data?.tailscale;
                const err = infos[i]?.error;
                return (
                  <li key={n.nodeId} className="flex items-center gap-3 px-4 py-2.5 text-sm">
                    <span className={cn("size-2 shrink-0 rounded-full", ts?.state === "Running" ? "bg-status-online" : err ? "bg-status-stopped" : "bg-status-starting")} />
                    <span className="w-28 shrink-0 font-medium">{n.name}</span>
                    <span className="min-w-0 flex-1 truncate text-muted-foreground">
                      {err ? "unreachable" : !infos[i]?.data ? "…" : !ts ? "joining…" : ts.state === "Running" ? <span className="font-mono text-xs text-foreground">{ts.dnsName.replace(/\.$/, "")}</span> : ts.loginUrl ? "needs a login" : `${ts.state || "starting"}${ts.error ? ` · ${ts.error}` : ""}`}
                    </span>
                    {ts?.loginUrl && (
                      <Button size="sm" variant="outline" asChild>
                        <a href={ts.loginUrl} target="_blank" rel="noreferrer">
                          <ExternalLink /> Log in
                        </a>
                      </Button>
                    )}
                    {ts?.addresses[0] && <span className="font-mono text-xs text-muted-foreground">{ts.addresses[0]}</span>}
                  </li>
                );
              })}
            </ul>
          </div>
        ) : (
          <p className="rounded-xl border border-dashed p-4 text-sm text-muted-foreground">Not connected. Services are reachable at home only until the Fleet is on a Tailscale network.</p>
        )}
      </section>

      <section className="space-y-3">
        <h2 className="text-base font-semibold">Services reachable away from home</h2>
        {remote.length === 0 ? (
          <p className="text-sm text-muted-foreground">None yet. A Service's menu has Exposure…, where you choose who can reach it from outside.</p>
        ) : (
          <ul className="divide-y rounded-xl border bg-card">
            {remote.map((s) => (
              <li key={s.spec.name} className="flex items-center gap-3 px-4 py-2.5 text-sm">
                <ServiceIcon catalogId={s.spec.catalogId} word={s.word} size={24} />
                <button className="font-medium hover:underline" onClick={() => go({ name: "service", service: s.spec.name })}>
                  {s.spec.name}
                </button>
                <Badge variant="outline">
                  <Globe className="size-3" /> {words[s.spec.exposure] ?? s.spec.exposure}
                </Badge>
                <span className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground">{s.spec.exposure === "direct" ? `https://${s.spec.publicHost}` : s.status?.remoteUrl || ""}</span>
                <StatusWord word={s.word} className="text-xs" />
              </li>
            ))}
          </ul>
        )}
      </section>

      <ConnectDialog open={connect} onClose={() => setConnect(false)} snap={snap} tag={config?.tag} />
    </div>
  );
}

function ConnectDialog({ open, onClose, snap, tag: current }: { open: boolean; onClose: () => void; snap: FleetSnapshot | undefined; tag?: string }) {
  const qc = useQueryClient();
  const [id, setId] = useState("");
  const [secret, setSecret] = useState("");
  const [tag, setTag] = useState(current ?? "tag:maitre");
  const [busy, setBusy] = useState(false);
  async function save() {
    if (!snap) return toast.error("no Node of the Fleet can be reached");
    setBusy(true);
    try {
      const body = toBinary(TailscaleConfigSchema, create(TailscaleConfigSchema, { oauthClientId: id.trim(), oauthClientSecret: secret.trim(), tag: tag.trim(), addedUnixMs: BigInt(Date.now()) }));
      await createClient(NodeService, transportFor({ addr: snap.anchor.addr, fingerprint: snap.anchor.nodeId })).putRecord({ record: { kind: "tailscale", id: "fleet", body } });
      toast.success("Saved. Each Node joins within a minute.");
      qc.invalidateQueries({ queryKey: ["fleet"] });
      setId("");
      setSecret("");
      onClose();
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Connect the Fleet to Tailscale</DialogTitle>
          <DialogDescription>One OAuth client for the whole Fleet. Every Node makes its own key from it and joins by itself.</DialogDescription>
        </DialogHeader>
        <ol className="list-decimal space-y-1.5 pl-5 text-sm text-muted-foreground">
          <li>
            In the Tailscale admin console, under Access controls, add a tag owner: <code className="rounded bg-muted px-1 text-xs">{`"tagOwners": { "${tag || "tag:maitre"}": ["autogroup:admin"] }`}</code>
          </li>
          <li>
            Under Settings, OAuth clients, create a client with the <b className="text-foreground">Auth Keys</b> write scope for that tag. Copy its ID and secret here.
          </li>
        </ol>
        <div className="space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="ts-tag">Tag</Label>
            <Input id="ts-tag" value={tag} onChange={(e) => setTag(e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="ts-id">Client ID</Label>
            <Input id="ts-id" value={id} onChange={(e) => setId(e.target.value)} className="font-mono text-xs" />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="ts-secret">Client secret</Label>
            <Input id="ts-secret" type="password" value={secret} onChange={(e) => setSecret(e.target.value)} className="font-mono text-xs" />
            <p className="text-xs text-muted-foreground">Stored in the Fleet, never shown again.</p>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={busy || !id.trim() || !secret.trim() || !tag.trim()} onClick={save}>
            Save to the Fleet
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
