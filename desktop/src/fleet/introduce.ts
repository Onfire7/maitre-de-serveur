// Milestone 3 repair, moved out of the old Home screen: Nodes provisioned
// before the sync layer existed (or while their sponsor was down) do not know
// each other. Whenever two known Nodes answer and one lacks the other's
// record, hand it over; from then on they sync by themselves. Addresses in
// the records become fallbacks for every known Node (Milestone 5).
import { useEffect } from "react";
import { createClient } from "@connectrpc/connect";
import { fromBinary } from "@bufbuild/protobuf";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { NodeSchema, type Record as Rec } from "@/gen/maitre/v1/fleet_pb";
import { transportFor } from "@/transport";
import type { KnownNode } from "@/state";
import { signedRecord } from "./sign";

export function useIntroduce(nodes: KnownNode[], onAddresses: (nodes: KnownNode[]) => void) {
  useEffect(() => {
    let stop = false;
    const introduce = async () => {
      const alive: { node: KnownNode; addr: string; records: Map<string, Rec> }[] = [];
      for (const n of nodes) {
        for (const a of n.addresses) {
          try {
            const recs = (await createClient(NodeService, transportFor({ addr: a, fingerprint: n.node_id })).listRecords({ kind: "node" })).records;
            alive.push({ node: n, addr: a, records: new Map(recs.filter((r) => !r.deleted).map((r) => [r.id, r])) });
            break;
          } catch {
            /* offline or unreachable here */
          }
        }
      }
      if (stop) return;
      let changed = false;
      const next = nodes.map((n) => {
        for (const a of alive) {
          const own = a.records.get(n.node_id);
          if (!own) continue;
          const rec = fromBinary(NodeSchema, own.body);
          const extra = rec.addresses.filter((x) => !n.addresses.includes(x));
          if (extra.length > 0) {
            changed = true;
            return { ...n, addresses: [...n.addresses, ...extra] };
          }
        }
        return n;
      });
      if (changed && !stop) onAddresses(next);
      for (const a of alive)
        for (const b of alive) {
          if (a === b || a.records.has(b.node.node_id)) continue;
          const own = b.records.get(b.node.node_id);
          if (!own) continue;
          try {
            // Decision 189: a signed record is handed over as it is, so the
            // signature that admitted the Node travels with it.
            const record = own.signature.length > 0 ? own : await signedRecord({ kind: "node", id: b.node.node_id, body: own.body });
            await createClient(NodeService, transportFor({ addr: a.addr, fingerprint: a.node.node_id })).putRecord({ record });
          } catch {
            /* next round */
          }
        }
    };
    introduce();
    const t = setInterval(introduce, 30000);
    return () => {
      stop = true;
      clearInterval(t);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodes.map((n) => n.node_id + n.addresses.join()).join(",")]);
}
