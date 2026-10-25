import { useEffect, useState } from "react";
import { createClient } from "@connectrpc/connect";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { NodeService } from "./gen/maitre/v1/node_pb";
import { UpdatePolicySchema, type UpdatePolicy } from "./gen/maitre/v1/fleet_pb";
import { transportFor } from "./transport";
import type { ConsoleState } from "./state";

// Milestone 6: how Maître Server updates roll across the Fleet by themselves.
export function UpdatePolicyPanel({ state }: { state: ConsoleState }) {
  const [policy, setPolicy] = useState<UpdatePolicy>(create(UpdatePolicySchema, { automatic: false, windowStartHour: 3, windowEndHour: 5, stageHours: 24 }));
  const [via, setVia] = useState<{ addr: string; fingerprint: string } | null>(null);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    (async () => {
      for (const n of state.nodes)
        for (const addr of n.addresses) {
          try {
            const recs = (await createClient(NodeService, transportFor({ addr, fingerprint: n.node_id })).listRecords({ kind: "update_policy" })).records.filter((r) => !r.deleted);
            if (recs[0]) setPolicy(fromBinary(UpdatePolicySchema, recs[0].body));
            setVia({ addr, fingerprint: n.node_id });
            return;
          } catch {
            /* next */
          }
        }
    })();
  }, [state.nodes.map((n) => n.node_id).join(",")]);
  if (state.nodes.length === 0) return null;
  async function save(p: UpdatePolicy) {
    setPolicy(p);
    setSaved(false);
    if (!via) return;
    try {
      await createClient(NodeService, transportFor(via)).putRecord({ record: { kind: "update_policy", id: "fleet", body: toBinary(UpdatePolicySchema, p) } });
      setSaved(true);
      setError("");
    } catch (e) {
      setError(String(e));
    }
  }
  return (
    <section>
      <h2>Updates</h2>
      <label>
        <input type="checkbox" checked={policy.automatic} onChange={(e) => save(create(UpdatePolicySchema, { ...policy, automatic: e.target.checked }))} /> Update Maître Server on the Nodes automatically
      </label>
      {policy.automatic && (
        <div style={{ marginLeft: 24, color: "#555", fontSize: 13 }}>
          <label>
            Between{" "}
            <input type="number" min={0} max={23} value={policy.windowStartHour} onChange={(e) => save(create(UpdatePolicySchema, { ...policy, windowStartHour: Number(e.target.value) }))} style={{ width: 50 }} />
            :00 and{" "}
            <input type="number" min={0} max={23} value={policy.windowEndHour} onChange={(e) => save(create(UpdatePolicySchema, { ...policy, windowEndHour: Number(e.target.value) }))} style={{ width: 50 }} />
            :00.
          </label>{" "}
          One Node goes first; the others follow after it has run the new version for{" "}
          <input type="number" min={1} max={168} value={policy.stageHours} onChange={(e) => save(create(UpdatePolicySchema, { ...policy, stageHours: Number(e.target.value) }))} style={{ width: 50 }} /> hours without trouble.
          <br />
          <label>
            <input type="checkbox" checked={policy.paused} onChange={(e) => save(create(UpdatePolicySchema, { ...policy, paused: e.target.checked }))} /> Paused
          </label>
        </div>
      )}
      {saved && <span style={{ color: "#2a7", fontSize: 13 }}> saved</span>}
      {error && <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>{error}</pre>}
    </section>
  );
}
