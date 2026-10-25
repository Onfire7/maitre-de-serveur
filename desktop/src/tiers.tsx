import { useEffect, useState } from "react";
import { createClient } from "@connectrpc/connect";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { NodeService } from "./gen/maitre/v1/node_pb";
import { TierSchema, type Tier } from "./gen/maitre/v1/storage_pb";
import { transportFor } from "./transport";
import type { ConsoleState } from "./state";

// Milestone 4: Tiers are Fleet-wide records. A Drive belongs to the
// highest-priority Tier whose tags it carries; "default" takes the rest.
export function useTiers(state: ConsoleState) {
  const [tiers, setTiers] = useState<Tier[]>([]);
  const [via, setVia] = useState<{ addr: string; fingerprint: string } | null>(null);
  const load = async () => {
    for (const n of state.nodes)
      for (const addr of n.addresses) {
        try {
          const recs = (await createClient(NodeService, transportFor({ addr, fingerprint: n.node_id })).listRecords({ kind: "tier" })).records;
          setTiers(recs.filter((r) => !r.deleted).map((r) => fromBinary(TierSchema, r.body)).sort((a, b) => b.priority - a.priority));
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
  const put = async (t: Tier, del = false) => {
    if (!via) throw new Error("no Node of the Fleet can be reached");
    await createClient(NodeService, transportFor(via)).putRecord({ record: { kind: "tier", id: t.tierId, body: toBinary(TierSchema, t), deleted: del } });
    await load();
  };
  return { tiers, put };
}

export function TiersPanel({ state }: { state: ConsoleState }) {
  const { tiers, put } = useTiers(state);
  const [name, setName] = useState("");
  const [tags, setTags] = useState("");
  const [error, setError] = useState("");
  if (state.nodes.length === 0) return null;
  return (
    <section>
      <h2>Tiers</h2>
      <p style={{ color: "#555", fontSize: 13 }}>A Tier is a kind of storage: Drives with these tags, on any Node. Services put each volume on a Tier.</p>
      {error && <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>{error}</pre>}
      <ul style={{ listStyle: "none", padding: 0 }}>
        {tiers.map((t) => (
          <li key={t.tierId} style={{ padding: "4px 0" }}>
            <b>{t.name}</b>{" "}
            <span style={{ color: "#555", fontSize: 13 }}>
              {t.tierId === "default" ? "every Drive no other Tier claims" : `Drives tagged ${t.requireTags.join(" + ") || "(any)"}`} · {t.placement}
            </span>{" "}
            {t.tierId !== "default" && (
              <button
                onClick={() => {
                  if (confirm(`Remove Tier ${t.name}? Its Drives fall back to Default; nothing is deleted.`)) put(t, true).catch((e) => setError(String(e)));
                }}
              >
                Remove
              </button>
            )}
          </li>
        ))}
      </ul>
      <div style={{ display: "flex", gap: 6, alignItems: "center" }}>
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Tier name, e.g. Media" style={{ width: 160 }} />
        <input value={tags} onChange={(e) => setTags(e.target.value)} placeholder="required tags, e.g. hdd media" style={{ width: 220 }} />
        <button
          disabled={!name.trim()}
          onClick={() => {
            const id = name.trim().toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
            put(create(TierSchema, { tierId: id, name: name.trim(), placement: "local", requireTags: tags.split(/[,\s]+/).filter(Boolean), priority: tiers.length, createdUnixMs: BigInt(Date.now()) }))
              .then(() => {
                setName("");
                setTags("");
                setError("");
              })
              .catch((e) => setError(String(e)));
          }}
        >
          Add Tier
        </button>
      </div>
    </section>
  );
}
