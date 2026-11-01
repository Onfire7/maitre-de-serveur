// What removed Nodes left behind (decision 276): Service and Drive records
// that still name a Node no longer in the Fleet. Shown on the Nodes tab
// only when there is something, with a way to clear each.
import { useState } from "react";
import { HardDrive, Package, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { leftBehind, removeLeftovers, type Leftover } from "@/fleet/leftovers";
import type { ConsoleState } from "@/state";
import type { Target } from "@/transport";

/** Every address of every Node the Console knows, as writes try them in turn. */
export function anyNode(state: ConsoleState): Target[] {
  return state.nodes.flatMap((n) => n.addresses.map((a) => ({ addr: a, fingerprint: n.node_id })));
}

export function Leftovers({ snap, state, onDone }: { snap: FleetSnapshot | undefined; state: ConsoleState; onDone?: () => void }) {
  const items = leftBehind(snap?.records ?? []);
  const [busy, setBusy] = useState<string | null>(null);
  if (items.length === 0) return null;

  async function clear(l: Leftover) {
    setBusy(l.kind + l.id);
    try {
      const err = await removeLeftovers(anyNode(state), [l]);
      if (err) toast.error(`Could not remove ${l.name}: ${err.message}`);
      else toast(`${l.name} was removed. ${l.kind === "service" ? "Its data stays on that machine's Drives." : ""}`.trim());
      onDone?.();
    } finally {
      setBusy(null);
    }
  }

  return (
    <section className="space-y-3" aria-label="Left behind by removed Nodes">
      <h2 className="text-base font-semibold">Left behind by removed Nodes</h2>
      <p className="text-sm text-muted-foreground">These belonged to Nodes that are no longer in the Fleet. A Service here is not running anywhere; to keep one, set it up again on a Node and then remove this entry. Its data stays on the removed machine's Drives.</p>
      <ul className="space-y-2">
        {items.map((l) => (
          <li key={l.kind + l.id} className="flex items-center justify-between rounded-lg border p-3">
            <div className="flex items-center gap-3">
              {l.kind === "service" ? <Package className="size-4 text-muted-foreground" /> : <HardDrive className="size-4 text-muted-foreground" />}
              <div>
                <div className="font-medium">{l.name}</div>
                <div className="text-xs text-muted-foreground">
                  {l.kind === "service" ? "Service" : "Drive"} of removed Node {l.nodeId.slice(0, 8)}
                </div>
              </div>
            </div>
            <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" disabled={busy !== null} onClick={() => clear(l)}>
              <Trash2 /> Remove
            </Button>
          </li>
        ))}
      </ul>
    </section>
  );
}
