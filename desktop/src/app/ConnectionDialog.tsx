// The connection chooser (decision 284): which Node the Console reads the
// Fleet through and by which way (at home, over Tailscale, at the Fleet's
// domain, or an address typed here), what each way answered on the last
// round, and a pin that holds the choice until it fails.
import { useState } from "react";
import { Globe, Home, Link2, Network } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { cn } from "@/lib/utils";
import type { Attempt, FleetSnapshot } from "@/fleet/useFleet";
import type { Candidate } from "@/fleet/anchor";
import { domainAddr } from "@/fleet/domain";
import { routeKind, routeLabel, type RouteKind } from "@/fleet/route";
import type { KnownNode } from "@/state";
import type { Target } from "@/transport";

export interface ConnectionProps {
  snap: FleetSnapshot | undefined;
  /** No Node answered: the poll's last error. */
  error?: string;
  known: KnownNode[];
  candidates: Candidate[];
  attempts: Attempt[];
  pin: Target | undefined;
  onChoose: (nodeId: string, addr: string) => void;
  onUnpin: () => void;
  /** Keep a typed address on this device, under that Node. */
  onAddAddress: (nodeId: string, addr: string) => Promise<void>;
}

const ICON: Record<RouteKind, typeof Home> = { home: Home, tailscale: Network, domain: Globe, other: Link2 };

/** "through Alder over Tailscale", for the header. */
export function connectionLine(snap: FleetSnapshot | undefined, known: KnownNode[]): string {
  if (!snap) return "not connected";
  const name = snap.nodes.find((n) => n.nodeId === snap.anchor.nodeId)?.name ?? known.find((n) => n.node_id === snap.anchor.nodeId)?.name ?? snap.anchor.nodeId.slice(0, 8);
  return `through ${name} ${routeLabel(routeKind(snap.anchor.addr, domainAddr()))}`;
}

export function ConnectionDialog({ open, onOpenChange, snap, error, known, candidates, attempts, pin, onChoose, onUnpin, onAddAddress }: ConnectionProps & { open: boolean; onOpenChange: (o: boolean) => void }) {
  const dom = domainAddr();
  const nameOf = (id: string) => snap?.nodes.find((n) => n.nodeId === id)?.name ?? known.find((n) => n.node_id === id)?.name ?? id.slice(0, 8);
  const letterOf = (id: string) => snap?.nodes.find((n) => n.nodeId === id)?.letter ?? "";
  const nodes = [...candidates].sort((a, b) => (letterOf(a.nodeId) || "~").localeCompare(letterOf(b.nodeId) || "~") || nameOf(a.nodeId).localeCompare(nameOf(b.nodeId)));
  const [typed, setTyped] = useState("");
  const [typedNode, setTypedNode] = useState(nodes[0]?.nodeId ?? "");
  const [adding, setAdding] = useState(false);
  const typedOk = /^(\[[0-9a-f:.%]+\]|[^\s:[\]]+):\d{1,5}$/i.test(typed.trim());

  async function useTyped() {
    const addr = typed.trim();
    if (!typedOk || !typedNode) return;
    setAdding(true);
    try {
      await onAddAddress(typedNode, addr);
      onChoose(typedNode, addr);
      setTyped("");
      onOpenChange(false);
    } finally {
      setAdding(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Connection</DialogTitle>
          <DialogDescription>
            {snap ? (
              <>
                Reading the Fleet {connectionLine(snap, known)}
                {pin ? ", as you chose" : ""}.
              </>
            ) : (
              <>No Node answers{error ? `: ${error}` : ""}. Each way below was tried in turn.</>
            )}
          </DialogDescription>
        </DialogHeader>
        <div className="max-h-[50vh] space-y-3 overflow-y-auto pr-1">
          {nodes.length === 0 && <p className="text-sm text-muted-foreground">This device knows no Node yet.</p>}
          {nodes.map((n) => (
            <div key={n.nodeId} className="rounded-lg border">
              <div className="flex items-center gap-2 border-b px-3 py-1.5 text-sm font-medium">
                {letterOf(n.nodeId) && <span className="rounded bg-muted px-1.5 text-xs font-medium text-muted-foreground">{letterOf(n.nodeId)}</span>}
                {nameOf(n.nodeId)}
              </div>
              <ul className="divide-y">
                {n.addresses.length === 0 && <li className="px-3 py-1.5 text-xs text-muted-foreground">No address known.</li>}
                {n.addresses.map((addr) => {
                  const kind = routeKind(addr, dom);
                  const Icon = ICON[kind];
                  const current = snap?.anchor.nodeId === n.nodeId && snap.anchor.addr === addr;
                  const isPin = pin?.fingerprint === n.nodeId && pin.addr === addr;
                  const tried = attempts.find((a) => a.nodeId === n.nodeId && a.addr === addr);
                  const word = current ? "connected" : !tried ? "not tried this round" : tried.error ? tried.error : "answered";
                  return (
                    <li key={addr} className={cn("flex items-center gap-3 px-3 py-1.5 text-sm", current && "bg-accent/50")}>
                      <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
                      <div className="min-w-0 flex-1">
                        <div className="flex items-baseline gap-2">
                          <span className="font-medium">{routeLabel(kind)}</span>
                          <span className="truncate font-mono text-xs text-muted-foreground">{addr}</span>
                        </div>
                        <div className={cn("truncate text-xs", tried?.error ? "text-status-attention-foreground" : "text-muted-foreground")} title={tried?.error || undefined}>
                          {word}
                          {isPin ? " · pinned" : ""}
                        </div>
                      </div>
                      <Button size="sm" variant={current ? "secondary" : "outline"} disabled={isPin} onClick={() => { onChoose(n.nodeId, addr); onOpenChange(false); }} aria-label={`Use ${nameOf(n.nodeId)} ${routeLabel(kind)}`}>
                        {isPin ? "Pinned" : current ? "Pin" : "Use"}
                      </Button>
                    </li>
                  );
                })}
              </ul>
            </div>
          ))}
        </div>
        <div className="space-y-2 border-t pt-3">
          <Label htmlFor="conn-addr" className="text-sm">
            Another address
          </Label>
          <p className="text-xs text-muted-foreground">A way to a Node that its record does not list: its Tailscale address from the admin console, say. The Node's key is checked whatever the address.</p>
          <div className="flex gap-2">
            <Input id="conn-addr" value={typed} onChange={(e) => setTyped(e.target.value)} placeholder="100.64.0.5:7443" className="font-mono text-sm" />
            <Select value={typedNode} onValueChange={setTypedNode}>
              <SelectTrigger className="w-40" aria-label="Which Node">
                <SelectValue placeholder="Node" />
              </SelectTrigger>
              <SelectContent>
                {nodes.map((n) => (
                  <SelectItem key={n.nodeId} value={n.nodeId}>
                    {nameOf(n.nodeId)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button size="sm" disabled={!typedOk || !typedNode || adding} onClick={useTyped}>
              Use
            </Button>
          </div>
        </div>
        <div className="flex items-center justify-between border-t pt-3 text-xs text-muted-foreground">
          <span>{pin ? "The pin holds until that way fails." : "The app chooses: the way that answered last, then each Node by letter."}</span>
          <Button size="sm" variant="ghost" disabled={!pin} onClick={() => { onUnpin(); onOpenChange(false); }}>
            Let the app choose
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
