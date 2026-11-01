// What a Node removed from the Fleet left behind (decision 276): its
// Service and Drive records stay the Fleet's history (275) but name a Node
// that is gone. A Trusted Device may delete them; nothing else can change
// them. Removing a Node tombstones them in the same breath; this finds the
// ones from before that, or from a removal that did not finish.
import { fromBinary } from "@bufbuild/protobuf";
import { type Record as Rec } from "@/gen/maitre/v1/fleet_pb";
import { ServiceSpecSchema } from "@/gen/maitre/v1/service_pb";
import { DriveRecordSchema } from "@/gen/maitre/v1/storage_pb";
import type { Target } from "@/transport";
import { putSigned } from "./sign";

export interface Leftover {
  kind: "service" | "drive";
  /** The record id: the Service's name, or the Drive's id. */
  id: string;
  /** What to call it. */
  name: string;
  /** The removed Node it belonged to. */
  nodeId: string;
  rec: Rec;
}

/** Whether `nodeId` names a Node the Fleet removed, or never heard of. */
export function nodeRemoved(records: Rec[], nodeId: string): boolean {
  const n = records.find((r) => r.kind === "node" && r.id === nodeId);
  return !n || n.deleted;
}

/** The live Service and Drive records owned by Nodes no longer in the Fleet. */
export function leftBehind(records: Rec[]): Leftover[] {
  const out: Leftover[] = [];
  for (const r of records) {
    if (r.deleted || (r.kind !== "service" && r.kind !== "drive")) continue;
    let nodeId = r.ownerNodeId;
    let name = r.id;
    try {
      if (r.kind === "service") {
        const s = fromBinary(ServiceSpecSchema, r.body);
        nodeId = s.nodeId || nodeId;
        name = s.name || name;
      } else {
        const d = fromBinary(DriveRecordSchema, r.body);
        nodeId = d.nodeId || nodeId;
        name = d.name || d.model || name;
      }
    } catch {
      /* an unreadable body still belongs to its owner */
    }
    if (nodeRemoved(records, nodeId)) out.push({ kind: r.kind, id: r.id, name, nodeId, rec: r });
  }
  return out.sort((a, b) => a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name));
}

/** The leftovers of one Node, as its removal must clear them. */
export function leftBy(records: Rec[], nodeId: string): Leftover[] {
  return leftBehind(records.map((r) => (r.kind === "node" && r.id === nodeId ? { ...r, deleted: true } : r))).filter((l) => l.nodeId === nodeId);
}

/**
 * Tombstones `items` through the first of `targets` that answers. Returns
 * the first failure, if any: the rest were still tried.
 */
export async function removeLeftovers(targets: Target[], items: Leftover[]): Promise<Error | undefined> {
  let first: Error | undefined;
  for (const l of items) {
    let done = false;
    let last: unknown;
    for (const t of targets) {
      try {
        await putSigned(t, { kind: l.kind, id: l.id, deleted: true }, l.rec);
        done = true;
        break;
      } catch (e) {
        last = e;
      }
    }
    if (!done && !first) first = new Error(`${l.name}: ${String((last as Error)?.message ?? last ?? "no Node answered")}`);
  }
  return first;
}
