// Milestone 3, decisions 99 to 101: three ways into an existing Fleet.
//  - Join: announce this Console, a trusted Console approves it.
//  - Recovery Phrase: the phrase's key asks a Node to add this Console.
//  - Code from a Node: a Node rebooted through recovery hands out a code.
// All three end the same way: adopt the Fleet from a Node that now trusts us.
import { createClient } from "@connectrpc/connect";
import { fromBinary } from "@bufbuild/protobuf";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { FleetSchema, NodeSchema, TrustedDeviceSchema } from "@/gen/maitre/v1/fleet_pb";
import { transportFor, type Target } from "@/transport";
import type { ConsoleState, Found, KnownNode } from "@/state";

/** Read the Fleet and its Nodes from a Node that trusts this device. */
export async function adoptFleet(t: Target, found: Found[]): Promise<ConsoleState> {
  const client = createClient(NodeService, transportFor(t));
  const recs = (await client.listRecords({})).records.filter((r) => !r.deleted);
  const fleetRec = recs.find((r) => r.kind === "fleet");
  if (!fleetRec) throw new Error("that Node holds no Fleet");
  const fleet = fromBinary(FleetSchema, fleetRec.body);
  const recovery = recs.filter((r) => r.kind === "trusted_device").map((r) => fromBinary(TrustedDeviceSchema, r.body)).find((d) => d.recoveryOnly);
  const nodes: KnownNode[] = recs
    .filter((r) => r.kind === "node")
    .map((r) => fromBinary(NodeSchema, r.body))
    .map((n) => {
      const live = found.find((f) => f.kind === "node" && f.id === n.nodeId);
      const addrs = [...(live?.addresses ?? []), ...n.addresses];
      if (n.nodeId === t.fingerprint && !addrs.includes(t.addr)) addrs.unshift(t.addr);
      return { node_id: n.nodeId, name: n.name, addresses: addrs.length ? addrs : [t.addr] };
    });
  return {
    fleet: { fleet_id: fleet.fleetId, name: fleet.name, created_unix_ms: Number(fleet.createdUnixMs), recovery_public_key_pem: recovery?.publicKeyPem ?? "" },
    nodes,
  };
}

/** Try every Node on the network until one answers this device. */
export async function firstTrustingNode(found: Found[]): Promise<Target | null> {
  for (const f of found.filter((x) => x.kind === "node"))
    for (const addr of f.addresses) {
      try {
        await createClient(NodeService, transportFor({ addr, fingerprint: f.id })).getNodeInfo({});
        return { addr, fingerprint: f.id };
      } catch {
        /* not yet */
      }
    }
  return null;
}
