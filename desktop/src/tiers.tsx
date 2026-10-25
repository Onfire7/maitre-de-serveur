import { useEffect, useState } from "react";
import { createClient } from "@connectrpc/connect";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { NodeService } from "./gen/maitre/v1/node_pb";
import { TierSchema, DriveRecordSchema, type Tier, type DriveRecord } from "./gen/maitre/v1/storage_pb";
import { transportFor } from "./transport";
import type { ConsoleState } from "./state";

// Milestone 4: Tiers are Fleet-wide records. A Drive belongs to the
// highest-priority Tier whose tags it carries; "default" takes the rest.
export function useTiers(state: ConsoleState) {
  const [tiers, setTiers] = useState<Tier[]>([]);
  const [drives, setDrives] = useState<DriveRecord[]>([]);
  const [via, setVia] = useState<{ addr: string; fingerprint: string } | null>(null);
  const load = async () => {
    for (const n of state.nodes)
      for (const addr of n.addresses) {
        try {
          const c = createClient(NodeService, transportFor({ addr, fingerprint: n.node_id }));
          const recs = (await c.listRecords({ kind: "tier" })).records;
          setTiers(recs.filter((r) => !r.deleted).map((r) => fromBinary(TierSchema, r.body)).sort((a, b) => b.priority - a.priority));
          const dr = (await c.listRecords({ kind: "drive" })).records;
          setDrives(dr.filter((r) => !r.deleted).map((r) => fromBinary(DriveRecordSchema, r.body)));
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
  return { tiers, drives, put };
}

/** Mirrors storage.TierOf on the Go side. */
export function tierOf(d: DriveRecord, tiers: Tier[]): string {
  const have = new Set([...d.autoTags, ...d.tags]);
  let best = "default";
  let bestPrio = -Infinity;
  for (const t of tiers) {
    if (t.tierId === "default") continue;
    if (!t.requireTags.every((x) => have.has(x))) continue;
    if (t.nodeIds.length > 0 && !t.nodeIds.includes(d.nodeId)) continue;
    if (t.priority > bestPrio) {
      best = t.tierId;
      bestPrio = t.priority;
    }
  }
  return best;
}

export function TiersPanel({ state }: { state: ConsoleState }) {
  const { tiers, drives, put } = useTiers(state);
  const [name, setName] = useState("");
  const [tags, setTags] = useState("");
  const [spanning, setSpanning] = useState(false);
  const [error, setError] = useState("");
  const nodeName = (id: string) => state.nodes.find((n) => n.node_id === id)?.name ?? id.slice(0, 8);
  const members = (t: Tier) => drives.filter((d) => tierOf(d, tiers) === t.tierId);
  // Live preview of the rule being typed (decision 104).
  const previewTags = tags.split(/[,\s]+/).filter(Boolean);
  const preview = name.trim() ? drives.filter((d) => previewTags.every((x) => [...d.autoTags, ...d.tags].includes(x))) : [];
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
            <div style={{ fontSize: 13, color: "#555", marginLeft: 16 }}>
              {members(t).length === 0 ? "no Drives yet" : members(t).map((d) => `${d.name} on ${nodeName(d.nodeId)}`).join(", ")}
            </div>
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
        <label title="Spanning: one Pool across every Node that has a Drive of this Tier, over the network">
          <input type="checkbox" checked={spanning} onChange={(e) => setSpanning(e.target.checked)} /> spans Nodes
        </label>
        <button
          disabled={!name.trim()}
          onClick={() => {
            const id = name.trim().toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
            put(create(TierSchema, { tierId: id, name: name.trim(), placement: spanning ? "spanning" : "local", requireTags: tags.split(/[,\s]+/).filter(Boolean), priority: tiers.length, createdUnixMs: BigInt(Date.now()) }))
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
      {name.trim() && (
        <div style={{ fontSize: 13, color: "#555", marginTop: 4 }}>
          Would take: {preview.length === 0 ? "no Drive matches these tags" : preview.map((d) => `${d.name} on ${nodeName(d.nodeId)}`).join(", ")}
        </div>
      )}
    </section>
  );
}
