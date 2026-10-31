import { useEffect, useState } from "react";
import { createClient } from "@connectrpc/connect";
import { fromBinary, toBinary } from "@bufbuild/protobuf";
import { NodeService } from "./gen/maitre/v1/node_pb";
import { SpaceSchema, DriveRecordSchema, type Space, type DriveRecord } from "./gen/maitre/v1/storage_pb";
import { NodeSchema, type Node as FleetNode, type Record as Rec } from "./gen/maitre/v1/fleet_pb";
import { currentRecord, putSigned } from "./fleet/sign";
import { transportFor } from "./transport";
import type { ConsoleState } from "./state";

// Storage Spaces (decisions 121-128) are Fleet-wide records. A Drive is a
// member of every Space whose rule matches it, unless an exclusive Space claims
// it. This panel is the minimal, working form of it; the UI sprint owns the
// real design (docs/ui-needs.md).
export function useSpaces(state: ConsoleState) {
  const [spaces, setSpaces] = useState<Space[]>([]);
  const [drives, setDrives] = useState<DriveRecord[]>([]);
  const [nodes, setNodes] = useState<FleetNode[]>([]);
  const [via, setVia] = useState<{ addr: string; fingerprint: string } | null>(null);
  const [records, setRecords] = useState<Rec[]>([]);
  const load = async () => {
    for (const n of state.nodes)
      for (const addr of n.addresses) {
        try {
          const c = createClient(NodeService, transportFor({ addr, fingerprint: n.node_id }));
          const recs = (await c.listRecords({ kind: "space" })).records;
          setRecords(recs);
          setSpaces(recs.filter((r) => !r.deleted).map((r) => fromBinary(SpaceSchema, r.body)).sort((a, b) => b.priority - a.priority));
          const dr = (await c.listRecords({ kind: "drive" })).records;
          setDrives(dr.filter((r) => !r.deleted).map((r) => fromBinary(DriveRecordSchema, r.body)));
          const nr = (await c.listRecords({ kind: "node" })).records;
          setNodes(nr.filter((r) => !r.deleted).map((r) => fromBinary(NodeSchema, r.body)).sort((a, b) => Number(a.provisionedUnixMs - b.provisionedUnixMs)));
          setVia({ addr, fingerprint: n.node_id });
          return;
        } catch {
          /* next */
        }
      }
  };
  useEffect(() => {
    load();
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  }, [state.nodes.map((n) => n.node_id).join(",")]);
  const put = async (b: Space, del = false) => {
    if (!via) throw new Error("no Node of the Fleet can be reached");
    await putSigned(via, { kind: "space", id: b.spaceId, body: toBinary(SpaceSchema, b), deleted: del }, currentRecord(records, "space", b.spaceId));
    await load();
  };
  return { spaces, drives, nodes, put };
}

/**
 * Mirrors storage.SpacesOf on the Go side: spaces sorted highest priority
 * first. A Parity Drive is in no Space; a Mirrored Drive only in Mirrored
 * Spaces, and a plain Drive never in one (ADR 0009, decisions 225, 255).
 */
export function spacesOf(d: DriveRecord, spaces: Space[]): string[] {
  if (d.role === "parity") return [];
  const have = new Set([...d.autoTags, ...d.tags]);
  const out: string[] = [];
  for (const b of spaces) {
    if (b.mirrored !== (d.role === "mirrored")) continue;
    if (!b.requireTags.every((x) => have.has(x))) continue;
    if (b.nodeIds.length > 0 && !b.nodeIds.includes(d.nodeId)) continue;
    if (b.exclusive) return [b.spaceId];
    out.push(b.spaceId);
  }
  return out.sort();
}

/** The Space new Service volumes land in, if any is marked. */
export function defaultSpace(spaces: Space[]): Space | undefined {
  return spaces.filter((b) => b.defaultForNew).sort((a, b) => Number(b.createdUnixMs - a.createdUnixMs))[0];
}
