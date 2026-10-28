// Space owners (decisions 173, 182): one id per Space, user and group of its
// shared folders, from 61000 up. The Console hands them out: to a new Space
// when it makes one, and to older Spaces the first time it sees them.
import { createClient } from "@connectrpc/connect";
import { create, toBinary } from "@bufbuild/protobuf";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { SpaceSchema, type Space } from "@/gen/maitre/v1/storage_pb";
import type { Record as Rec } from "@/gen/maitre/v1/fleet_pb";
import { transportFor, type Target } from "@/transport";
import { currentRecord, signedRecord } from "./sign";

export const FIRST_OWNER = 61000;

export function nextOwnerId(spaces: Space[]): number {
  return Math.max(FIRST_OWNER - 1, ...spaces.map((s) => s.ownerId)) + 1;
}

/** Spaces without an owner, oldest first, with the ids they get. */
export function ownersToAssign(spaces: Space[]): Space[] {
  const out: Space[] = [];
  let next = nextOwnerId(spaces);
  for (const s of [...spaces].filter((x) => !x.ownerId).sort((a, b) => Number(a.createdUnixMs - b.createdUnixMs) || a.spaceId.localeCompare(b.spaceId))) out.push(create(SpaceSchema, { ...s, ownerId: next++ }));
  return out;
}

export async function assignOwners(anchor: Target, spaces: Space[], records: Rec[] = [], tf: typeof transportFor = transportFor): Promise<number> {
  const todo = ownersToAssign(spaces);
  const client = createClient(NodeService, tf(anchor));
  for (const s of todo) await client.putRecord({ record: await signedRecord({ kind: "space", id: s.spaceId, body: toBinary(SpaceSchema, s) }, { prev: currentRecord(records, "space", s.spaceId) }) });
  return todo.length;
}
