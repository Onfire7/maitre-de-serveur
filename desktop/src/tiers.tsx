import { useEffect, useState } from "react";
import { createClient } from "@connectrpc/connect";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { NodeService } from "./gen/maitre/v1/node_pb";
import { TierSchema, DriveRecordSchema, type Tier, type DriveRecord } from "./gen/maitre/v1/storage_pb";
import { NodeSchema, type Node as FleetNode } from "./gen/maitre/v1/fleet_pb";
import { transportFor } from "./transport";
import type { ConsoleState } from "./state";

// Milestone 4: Tiers are Fleet-wide records. A Drive belongs to the
// highest-priority Tier whose tags it carries; "default" takes the rest.
export function useTiers(state: ConsoleState) {
  const [tiers, setTiers] = useState<Tier[]>([]);
  const [drives, setDrives] = useState<DriveRecord[]>([]);
  const [nodes, setNodes] = useState<FleetNode[]>([]);
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
  const put = async (t: Tier, del = false) => {
    if (!via) throw new Error("no Node of the Fleet can be reached");
    await createClient(NodeService, transportFor(via)).putRecord({ record: { kind: "tier", id: t.tierId, body: toBinary(TierSchema, t), deleted: del } });
    await load();
  };
  return { tiers, drives, nodes, put };
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
  const { tiers, drives, nodes, put } = useTiers(state);
  const [name, setName] = useState("");
  const [tags, setTags] = useState("");
  const [spanning, setSpanning] = useState(false);
  const [error, setError] = useState("");
  const [hover, setHover] = useState<string | null>(null);
  const nodeName = (id: string) => state.nodes.find((n) => n.node_id === id)?.name ?? id.slice(0, 8);
  const members = (t: Tier) => drives.filter((d) => tierOf(d, tiers) === t.tierId);
  // Live preview of the rule being typed (decision 104).
  const previewTags = tags.split(/[,\s]+/).filter(Boolean);
  const preview = name.trim() ? drives.filter((d) => previewTags.every((x) => [...d.autoTags, ...d.tags].includes(x))) : [];
  // What the map lights up: the rule being typed wins, then the Tier under the pointer.
  const lit = name.trim() ? new Set(preview.map((d) => d.driveId)) : hover ? new Set(members(tiers.find((t) => t.tierId === hover)!).map((d) => d.driveId)) : null;
  if (state.nodes.length === 0) return null;
  return (
    <section>
      <h2>Tiers</h2>
      <p style={{ color: "#555", fontSize: 13 }}>A Tier is a kind of storage: Drives with these tags, on any Node. Services put each volume on a Tier.</p>
      {error && <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>{error}</pre>}
      <FleetMap nodes={nodes} known={state.nodes} drives={drives} tiers={tiers} lit={lit} />
      <ul style={{ listStyle: "none", padding: 0 }}>
        {tiers.map((t, i) => (
          <li key={t.tierId} style={{ padding: "4px 0", borderLeft: `4px solid ${tierColor(i)}`, paddingLeft: 8, background: hover === t.tierId ? "#f6f6f6" : undefined }} onMouseEnter={() => setHover(t.tierId)} onMouseLeave={() => setHover(null)}>
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

/** One colour per Tier, by position in the list (highest priority first). */
export function tierColor(i: number): string {
  const palette = ["#4c78a8", "#f58518", "#54a24b", "#b279a2", "#e45756", "#72b7b2", "#eeca3b", "#ff9da6", "#9d755d", "#bab0ac"];
  return palette[i % palette.length];
}

/**
 * Decision 104: the Fleet as rounded boxes. One per Node, carrying its letter,
 * holding one box per Drive coloured by the Tier that claims it. `lit` is the
 * set of Drives to highlight (a Tier under the pointer, or a rule being typed);
 * null lights everything.
 */
export function FleetMap({
  nodes,
  known,
  drives,
  tiers,
  lit,
}: {
  nodes: FleetNode[];
  known: { node_id: string; name: string }[];
  drives: DriveRecord[];
  tiers: Tier[];
  lit: Set<string> | null;
}) {
  // Every Node the Console knows, with the Fleet record's letter when it has one.
  const ids = Array.from(new Set([...nodes.map((n) => n.nodeId), ...known.map((k) => k.node_id)]));
  const rows = ids.map((id) => {
    const rec = nodes.find((n) => n.nodeId === id);
    return { id, name: rec?.name || known.find((k) => k.node_id === id)?.name || id.slice(0, 8), letter: rec?.letter || "?" };
  }).sort((a, b) => a.name.localeCompare(b.name));
  const tierIndex = (d: DriveRecord) => tiers.findIndex((t) => t.tierId === tierOf(d, tiers));
  return (
    <div style={{ display: "flex", flexWrap: "wrap", gap: 12, margin: "8px 0 12px 0" }}>
      {rows.map((n) => {
        const mine = drives.filter((d) => d.nodeId === n.id).sort((a, b) => a.name.localeCompare(b.name));
        return (
          <div key={n.id} style={{ border: "2px solid #888", borderRadius: 14, padding: "8px 10px 10px 10px", minWidth: 160, background: "#fafafa" }}>
            <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 8 }}>
              <span style={{ display: "inline-flex", alignItems: "center", justifyContent: "center", width: 26, height: 26, borderRadius: "50%", background: "#333", color: "#fff", fontWeight: 700, fontSize: 14 }} title="This Node's letter; its Drives are named after it">
                {n.letter}
              </span>
              <b>{n.name}</b>
            </div>
            <div style={{ display: "flex", flexWrap: "wrap", gap: 6 }}>
              {mine.length === 0 && <span style={{ color: "#999", fontSize: 12, padding: "6px 0" }}>no Drives</span>}
              {mine.map((d) => {
                const on = lit === null || lit.has(d.driveId);
                const ti = tierIndex(d);
                const color = ti >= 0 ? tierColor(ti) : "#bbb";
                return (
                  <div
                    key={d.driveId}
                    title={`${d.name} · ${d.model} · ${[...d.autoTags, ...d.tags].join(", ")}`}
                    style={{
                      border: `2px solid ${color}`,
                      background: on ? color + "22" : "#fff",
                      opacity: on ? 1 : 0.35,
                      borderRadius: 10,
                      padding: "6px 10px",
                      minWidth: 84,
                      fontSize: 12,
                      transition: "opacity .15s",
                    }}
                  >
                    <div style={{ fontWeight: 600 }}>{d.name}</div>
                    <div style={{ color: "#555" }}>{sizeOf(d.sizeBytes)}</div>
                    <div style={{ color: "#777", fontSize: 11 }}>{ti >= 0 ? tiers[ti].name : "—"}</div>
                  </div>
                );
              })}
            </div>
          </div>
        );
      })}
    </div>
  );
}

function sizeOf(n: bigint) {
  const v = Number(n);
  return v >= 1e12 ? `${(v / 1e12).toFixed(1)} TB` : `${Math.round(v / 1e9)} GB`;
}
