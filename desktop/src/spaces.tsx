import { useEffect, useState } from "react";
import { createClient } from "@connectrpc/connect";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { NodeService } from "./gen/maitre/v1/node_pb";
import { SpaceSchema, DriveRecordSchema, type Space, type DriveRecord } from "./gen/maitre/v1/storage_pb";
import { NodeSchema, type Node as FleetNode } from "./gen/maitre/v1/fleet_pb";
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
  const load = async () => {
    for (const n of state.nodes)
      for (const addr of n.addresses) {
        try {
          const c = createClient(NodeService, transportFor({ addr, fingerprint: n.node_id }));
          const recs = (await c.listRecords({ kind: "space" })).records;
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
    await createClient(NodeService, transportFor(via)).putRecord({ record: { kind: "space", id: b.spaceId, body: toBinary(SpaceSchema, b), deleted: del } });
    await load();
  };
  return { spaces, drives, nodes, put };
}

/** Mirrors storage.SpacesOf on the Go side: spaces sorted highest priority first. */
export function spacesOf(d: DriveRecord, spaces: Space[]): string[] {
  const have = new Set([...d.autoTags, ...d.tags]);
  const out: string[] = [];
  for (const b of spaces) {
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

export function SpacesPanel({ state }: { state: ConsoleState }) {
  const { spaces, drives, nodes, put } = useSpaces(state);
  const [name, setName] = useState("");
  const [tags, setTags] = useState("");
  const [spanning, setSpanning] = useState(false);
  const [exclusive, setExclusive] = useState(false);
  const [placement, setPlacement] = useState("local_first");
  const [error, setError] = useState("");
  const [hover, setHover] = useState<string | null>(null);
  const nodeName = (id: string) => state.nodes.find((n) => n.node_id === id)?.name ?? id.slice(0, 8);
  const members = (b: Space) => drives.filter((d) => spacesOf(d, spaces).includes(b.spaceId));
  // Live preview of the rule being typed (decision 104).
  const previewTags = tags.split(/[,\s]+/).filter(Boolean);
  const preview = name.trim() ? drives.filter((d) => previewTags.every((x) => [...d.autoTags, ...d.tags].includes(x))) : [];
  const lit = name.trim() ? new Set(preview.map((d) => d.driveId)) : hover ? new Set(members(spaces.find((b) => b.spaceId === hover)!).map((d) => d.driveId)) : null;
  if (state.nodes.length === 0) return null;
  const def = defaultSpace(spaces);
  return (
    <section>
      <h2>Storage Spaces</h2>
      <p style={{ color: "#555", fontSize: 13 }}>
        A Space is a place for one kind of data: the Drives its rule selects, on any Node, each giving the Space a folder. A Drive can serve several Spaces; an exclusive Space keeps its Drives to itself. Services put each volume in a Space.
      </p>
      {spaces.length === 0 && <p style={{ color: "#a60" }}>No Spaces yet. Services cannot be installed until a Space is marked as the default for new volumes.</p>}
      {spaces.length > 0 && !def && <p style={{ color: "#a60" }}>No Space is marked as the default for new volumes; installing a Service will fail until one is.</p>}
      {error && <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>{error}</pre>}
      <FleetMap nodes={nodes} known={state.nodes} drives={drives} spaces={spaces} lit={lit} />
      <ul style={{ listStyle: "none", padding: 0 }}>
        {spaces.map((b, i) => (
          <li key={b.spaceId} style={{ padding: "4px 0", borderLeft: `4px solid ${spaceColor(i)}`, paddingLeft: 8, background: hover === b.spaceId ? "#f6f6f6" : undefined }} onMouseEnter={() => setHover(b.spaceId)} onMouseLeave={() => setHover(null)}>
            <b>{b.name}</b>{" "}
            <span style={{ color: "#555", fontSize: 13 }}>
              {b.requireTags.length ? `Drives tagged ${b.requireTags.join(" + ")}` : "every Drive"}
              {b.nodeIds.length ? ` on ${b.nodeIds.map(nodeName).join(", ")}` : ""} · {b.placement.replace(/_/g, " ")}
              {b.spanNodes ? " · spans Nodes" : ""}
              {b.exclusive ? " · exclusive" : ""}
              {b.defaultForNew ? " · default for new volumes" : ""}
            </span>
            <div style={{ fontSize: 13, color: "#555", marginLeft: 16 }}>
              {members(b).length === 0 ? "no Drives yet" : members(b).map((d) => `${d.name} on ${nodeName(d.nodeId)}`).join(", ")}
            </div>
            <label style={{ fontSize: 13, marginRight: 8 }}>
              <input type="checkbox" checked={b.spanNodes} onChange={(e) => put(create(SpaceSchema, { ...b, spanNodes: e.target.checked })).catch((err) => setError(String(err)))} /> spans Nodes
            </label>
            <label style={{ fontSize: 13, marginRight: 8 }}>
              <input type="checkbox" checked={b.defaultForNew} onChange={(e) => put(create(SpaceSchema, { ...b, defaultForNew: e.target.checked })).catch((err) => setError(String(err)))} /> default for new volumes
            </label>
            <button
              onClick={() => {
                if (confirm(`Remove Space ${b.name}? Its folders stay on the Drives (shown as folders of a removed Space); nothing is deleted.`)) put(b, true).catch((e) => setError(String(e)));
              }}
            >
              Remove
            </button>
          </li>
        ))}
      </ul>
      <div style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap" }}>
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Space name, e.g. Media" style={{ width: 160 }} />
        <input value={tags} onChange={(e) => setTags(e.target.value)} placeholder="required tags (empty = every Drive)" style={{ width: 220 }} />
        <select value={placement} onChange={(e) => setPlacement(e.target.value)} title="Where new files go">
          <option value="local_first">local Drives first</option>
          <option value="most_free">most free space</option>
          <option value="most_free_percent">highest free share</option>
        </select>
        <label title="One Pool across every Node that has a Drive of this Space, over the network">
          <input type="checkbox" checked={spanning} onChange={(e) => setSpanning(e.target.checked)} /> spans Nodes
        </label>
        <label title="Member Drives belong to this Space only (needs a rule)">
          <input type="checkbox" checked={exclusive} onChange={(e) => setExclusive(e.target.checked)} /> exclusive
        </label>
        <button
          disabled={!name.trim() || (exclusive && previewTags.length === 0)}
          onClick={() => {
            const id = name.trim().toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
            put(
              create(SpaceSchema, {
                spaceId: id,
                name: name.trim(),
                placement,
                requireTags: previewTags,
                spanNodes: spanning,
                exclusive,
                defaultForNew: spaces.length === 0,
                priority: spaces.length,
                createdUnixMs: BigInt(Date.now()),
              }),
            )
              .then(() => {
                setName("");
                setTags("");
                setError("");
              })
              .catch((e) => setError(String(e)));
          }}
        >
          Add Space
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

/** One colour per Space, by position in the list (highest priority first). */
export function spaceColor(i: number): string {
  const palette = ["#4c78a8", "#f58518", "#54a24b", "#b279a2", "#e45756", "#72b7b2", "#eeca3b", "#ff9da6", "#9d755d", "#bab0ac"];
  return palette[i % palette.length];
}

/**
 * Decision 104: the Fleet as rounded boxes. One per Node, carrying its letter,
 * holding one box per Drive with a colour stripe per Space it serves (126).
 * `lit` is the set of Drives to highlight (a Space under the pointer, or a rule
 * being typed); null lights everything.
 */
export function FleetMap({
  nodes,
  known,
  drives,
  spaces,
  lit,
}: {
  nodes: FleetNode[];
  known: { node_id: string; name: string }[];
  drives: DriveRecord[];
  spaces: Space[];
  lit: Set<string> | null;
}) {
  // Every Node the Console knows, with the Fleet record's letter when it has one.
  const ids = Array.from(new Set([...nodes.map((n) => n.nodeId), ...known.map((k) => k.node_id)]));
  const rows = ids.map((id) => {
    const rec = nodes.find((n) => n.nodeId === id);
    return { id, name: rec?.name || known.find((k) => k.node_id === id)?.name || id.slice(0, 8), letter: rec?.letter || "?" };
  }).sort((a, b) => a.name.localeCompare(b.name));
  const spaceIndexes = (d: DriveRecord) => spacesOf(d, spaces).map((id) => spaces.findIndex((b) => b.spaceId === id)).filter((i) => i >= 0);
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
                const idx = spaceIndexes(d);
                const color = idx.length ? spaceColor(idx[0]) : "#bbb";
                const stripes = idx.length > 1 ? `linear-gradient(90deg, ${idx.map((i, k) => `${spaceColor(i)} ${(k * 100) / idx.length}% ${((k + 1) * 100) / idx.length}%`).join(", ")})` : undefined;
                return (
                  <div
                    key={d.driveId}
                    title={`${d.name} · ${d.model} · ${[...d.autoTags, ...d.tags].join(", ")}`}
                    style={{
                      border: `2px solid ${color}`,
                      borderImage: stripes ? `${stripes} 2` : undefined,
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
                    <div style={{ color: "#777", fontSize: 11 }}>{idx.length ? idx.map((i) => spaces[i].name + (spaces[i].exclusive ? " 🔒" : "")).join(", ") : "no Space"}</div>
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
