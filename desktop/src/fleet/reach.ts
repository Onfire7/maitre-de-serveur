// How this device reaches each Node (ADR 0012): straight, at one of its
// addresses, or through the anchor's Relay; and whether it reaches a Node
// the anchor has lost, since Unreachable means neither reaches it.
import { create } from "@bufbuild/protobuf";
import { FleetNodeStatusSchema } from "@/gen/maitre/v1/node_pb";
import type { Target } from "@/transport";
import type { AnchorPick } from "./anchor";
import { domainAddr } from "./domain";
import { routeKind, routeLabel } from "./route";
import { nodeWord, UNREACHABLE_MS, type FleetSnapshot, type NodeStatus } from "./useFleet";

/** How the Desktop's dial last reached a Node (the `node_way` command). */
export type Way = "direct" | "relayed";

/**
 * A Node this device has not reached yet, or that the anchor has lost, is
 * asked once per this long; an answer counts for twice as long.
 */
export const PROBE_MS = 15_000;

export interface ReachDeps {
  /** How the Desktop's dial last reached this Node, or null: never, or that way failed. */
  wayOf: (nodeId: string) => Promise<Way | null>;
  /** One cheap call to the Node, which the Desktop makes straight or through the Relay. */
  probe: (t: Target) => Promise<void>;
}

/**
 * Watch how this device reaches each Node. Each round takes the anchor
 * that answered, the Fleet status it gave, and the address the Console
 * would use for every other Node; it returns the status with each Node's
 * way and whether this device reached it lately. Calls to Nodes run in the
 * background: their answers show from the next round on.
 */
export function createReach({ wayOf, probe }: ReachDeps) {
  const probes = new Map<string, { at: number; ok: boolean }>();
  const asking = new Set<string>();

  function ask(t: Target, now: number) {
    asking.add(t.fingerprint);
    probe(t).then(
      () => probes.set(t.fingerprint, { at: now, ok: true }),
      () => probes.set(t.fingerprint, { at: now, ok: false }),
    ).finally(() => asking.delete(t.fingerprint));
  }

  return async function observe(anchor: AnchorPick, status: Map<string, NodeStatus>, targets: Target[], now: number): Promise<Map<string, NodeStatus>> {
    const out = new Map(status);
    const blank = (id: string): NodeStatus => out.get(id) ?? create(FleetNodeStatusSchema, { nodeId: id });
    out.set(anchor.nodeId, { ...blank(anchor.nodeId), way: "direct", wayAddr: anchor.addr, consoleReached: true });
    await Promise.all(
      targets
        .filter((t) => t.fingerprint !== anchor.nodeId)
        .map(async (t) => {
          const id = t.fingerprint;
          const st = blank(id);
          const way = await wayOf(id).catch(() => null);
          const heard = st.reachable && now - Number(st.lastSeenUnixMs) <= UNREACHABLE_MS;
          const last = probes.get(id);
          if ((!way || !heard) && !asking.has(id) && (!last || now - last.at >= PROBE_MS)) ask(t, now);
          const consoleReached = !!last?.ok && now - last.at < 2 * PROBE_MS;
          // A way the anchor has lost and this device no longer gets an
          // answer through is not shown: it is a pooled connection gone stale.
          const shown = way && (heard || consoleReached) ? way : undefined;
          out.set(id, { ...st, way: shown, wayAddr: shown === "direct" ? t.addr : undefined, consoleReached });
        }),
    );
    return out;
  };
}

/**
 * How this device reaches a Node, as the Connection chooser and the Node's
 * details say it: "at home", "over Tailscale", "at your domain", "by
 * address", "through Alder" (the anchor's Relay), or "unreachable";
 * undefined while it has not been reached yet.
 */
export function wayLine(snap: FleetSnapshot | undefined, nodeId: string, now: number): string | undefined {
  if (!snap) return undefined;
  const dom = domainAddr();
  if (nodeId === snap.anchor.nodeId) return routeLabel(routeKind(snap.anchor.addr, dom));
  const st = snap.status.get(nodeId);
  if (nodeWord(st, now) === "Unreachable") return "unreachable";
  if (st?.way === "relayed") return `through ${snap.nodes.find((n) => n.nodeId === snap.anchor.nodeId)?.name ?? snap.anchor.nodeId.slice(0, 8)}`;
  if (st?.way === "direct" && st.wayAddr) return routeLabel(routeKind(st.wayAddr, dom));
  return undefined;
}
