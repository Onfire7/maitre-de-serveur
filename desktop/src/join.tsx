import { useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { createClient } from "@connectrpc/connect";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { NodeService } from "./gen/maitre/v1/node_pb";
import { RecoveryService } from "./gen/maitre/v1/recovery_pb";
import { FleetSchema, NodeSchema, TrustedDeviceSchema } from "./gen/maitre/v1/fleet_pb";
import { transportFor, type Target } from "./transport";
import { api, type ConsoleState, type DeviceInfo, type Found, type KnownNode } from "./state";

// Milestone 3, decisions 99 to 101: three ways into an existing Fleet.
//  - Join: announce this Console, a trusted Console approves it.
//  - Recovery Phrase: the phrase's key asks a Node to add this Console.
//  - Code from a Node: a Node rebooted through recovery hands out a code.
// All three end the same way: adopt the Fleet from a Node that now trusts us.

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
async function firstTrustingNode(found: Found[]): Promise<Target | null> {
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

export function JoinScreen({ device, onJoined, onCancel }: { device: DeviceInfo; onJoined: (s: ConsoleState) => void; onCancel: () => void }) {
  const [code, setCode] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    let fullname = "";
    let stop = false;
    invoke<string>("device_code", {}).then(setCode);
    invoke<string>("announce_join")
      .then((n) => (fullname = n))
      .catch((e) => setError(String(e)));
    const tick = async () => {
      try {
        const found = await api.discover();
        const t = await firstTrustingNode(found);
        if (t && !stop) {
          stop = true;
          onJoined(await adoptFleet(t, found));
        }
      } catch (e) {
        if (!stop) setError(String(e));
      }
    };
    const timer = setInterval(tick, 3000);
    return () => {
      stop = true;
      clearInterval(timer);
      if (fullname) invoke("withdraw_join", { fullname }).catch(() => {});
    };
  }, [onJoined]);
  return (
    <section>
      <h2>Join a Fleet</h2>
      <p>On a computer that already manages the Fleet, open Maître Desktop: this computer appears under "Devices asking to join" as {device.name}.</p>
      <p>Approve it there if it shows this code:</p>
      <p style={{ fontSize: 40, fontFamily: "monospace", letterSpacing: 4 }}>{code || "…"}</p>
      <p style={{ color: "#555" }}>Waiting for approval…</p>
      {error && <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>{error}</pre>}
      <button onClick={onCancel}>Cancel</button>
    </section>
  );
}

export function RecoverScreen({ device, onJoined, onCancel }: { device: DeviceInfo; onJoined: (s: ConsoleState) => void; onCancel: () => void }) {
  const [phrase, setPhrase] = useState("");
  const [nodes, setNodes] = useState<Found[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    const tick = () => api.discover().then((f) => setNodes(f.filter((x) => x.kind === "node"))).catch(() => {});
    tick();
    const t = setInterval(tick, 2000);
    return () => clearInterval(t);
  }, []);
  async function recover() {
    setBusy(true);
    setError("");
    try {
      const found = await api.discover();
      let last = "";
      for (const n of nodes)
        for (const addr of n.addresses) {
          const body = toBinary(TrustedDeviceSchema, create(TrustedDeviceSchema, { deviceId: device.fingerprint, name: device.name, publicKeyPem: device.public_key_pem, addedUnixMs: BigInt(Date.now()) }));
          // PutRecord request: Record{kind, id, body}. Built by hand to keep the Rust command generic.
          const { PutRecordRequestSchema } = await import("./gen/maitre/v1/node_pb");
          const req = toBinary(PutRecordRequestSchema, create(PutRecordRequestSchema, { record: { kind: "trusted_device", id: device.fingerprint, body } }));
          try {
            await invoke<string>("recover_with_phrase", { phrase: phrase.trim().toLowerCase(), addr, fingerprint: n.id, body: Array.from(req) });
            onJoined(await adoptFleet({ addr, fingerprint: n.id }, found));
            return;
          } catch (e) {
            last = String(e);
          }
        }
      throw new Error(last || "no Node on this network answered");
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <section>
      <h2>Recover with your phrase</h2>
      <p>The twelve words you wrote down when the Fleet was created. They let this computer add itself; nothing else.</p>
      <textarea value={phrase} onChange={(e) => setPhrase(e.target.value)} rows={3} style={{ width: "100%", fontFamily: "monospace" }} />
      <p style={{ color: "#555", fontSize: 13 }}>{nodes.length === 0 ? "Looking for a Node on this network…" : `Nodes found: ${nodes.map((n) => n.name).join(", ")}`}</p>
      {error && <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>{error}</pre>}
      <button disabled={busy || nodes.length === 0 || phrase.trim().split(/\s+/).length !== 12} onClick={recover}>
        Recover
      </button>{" "}
      <button onClick={onCancel}>Cancel</button>
    </section>
  );
}

export function CodeScreen({ device, onJoined, onCancel }: { device: DeviceInfo; onJoined: (s: ConsoleState) => void; onCancel: () => void }) {
  const [code, setCode] = useState("");
  const [nodes, setNodes] = useState<Found[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    const tick = () => api.discover().then((f) => setNodes(f.filter((x) => x.kind === "node"))).catch(() => {});
    tick();
    const t = setInterval(tick, 2000);
    return () => clearInterval(t);
  }, []);
  const open = nodes.filter((n) => n.recovery);
  async function authorize(n: Found) {
    setBusy(true);
    setError("");
    try {
      const found = await api.discover();
      let last = "";
      for (const a of n.addresses) {
        const host = a.replace(/:\d+$/, "");
        try {
          const rc = createClient(RecoveryService, transportFor({ addr: `${host}:7444`, fingerprint: n.id }));
          await rc.authorize({ code: code.replace(/\s+/g, ""), name: device.name, publicKeyPem: device.public_key_pem });
          onJoined(await adoptFleet({ addr: a, fingerprint: n.id }, found));
          return;
        } catch (e) {
          last = String(e);
        }
      }
      throw new Error(last);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <section>
      <h2>Join with a code from a Node</h2>
      <p>At the Node's keyboard, press r on the Status page and choose "reboot into recovery". It shows an eight-digit code and restarts; enter the code here within 15 minutes.</p>
      <input value={code} onChange={(e) => setCode(e.target.value)} placeholder="1234 5678" style={{ fontSize: 24, fontFamily: "monospace", width: 200 }} />
      {open.length === 0 ? (
        <p style={{ color: "#555", fontSize: 13 }}>No Node is accepting a code right now{nodes.length > 0 ? ` (found: ${nodes.map((n) => n.name).join(", ")})` : ""}.</p>
      ) : (
        <ul style={{ listStyle: "none", padding: 0 }}>
          {open.map((n) => (
            <li key={n.id} style={{ padding: "6px 0" }}>
              <b>{n.name}</b> is accepting a code{" "}
              <button disabled={busy || code.replace(/\s+/g, "").length !== 8} onClick={() => authorize(n)}>
                Join through {n.name}
              </button>
            </li>
          ))}
        </ul>
      )}
      {error && <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>{error}</pre>}
      <button onClick={onCancel}>Cancel</button>
    </section>
  );
}

/** Devices announcing themselves on the LAN, for a trusted Console to approve (decision 99). */
export function AskingDevices({ state, found }: { state: ConsoleState; found: Found[] }) {
  const [codes, setCodes] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [done, setDone] = useState<Record<string, boolean>>({});
  const asking = found.filter((f) => f.kind === "device" && f.key_pem);
  useEffect(() => {
    for (const d of asking)
      if (!codes[d.id]) invoke<string>("device_code", { keyPem: d.key_pem }).then((c) => setCodes((m) => ({ ...m, [d.id]: c })));
  }, [asking.map((d) => d.id).join(",")]);
  if (asking.length === 0) return null;
  async function approve(d: Found) {
    setError("");
    const body = toBinary(TrustedDeviceSchema, create(TrustedDeviceSchema, { deviceId: d.id, name: d.name, publicKeyPem: d.key_pem, addedUnixMs: BigInt(Date.now()) }));
    for (const n of state.nodes)
      for (const addr of n.addresses) {
        try {
          await createClient(NodeService, transportFor({ addr, fingerprint: n.node_id })).putRecord({ record: { kind: "trusted_device", id: d.id, body } });
          setDone((m) => ({ ...m, [d.id]: true }));
          return;
        } catch {
          /* next */
        }
      }
    setError("no Node of the Fleet could be reached to add the device");
  }
  return (
    <section>
      <h3>Devices asking to join</h3>
      {error && <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>{error}</pre>}
      <ul style={{ listStyle: "none", padding: 0 }}>
        {asking.map((d) => (
          <li key={d.id} style={{ padding: "6px 0" }}>
            <b>{d.name}</b> shows code <code style={{ fontSize: 18 }}>{codes[d.id] ?? "…"}</code>{" "}
            {done[d.id] ? <span style={{ color: "#2a7" }}>approved</span> : <button onClick={() => approve(d)}>Approve</button>}
            <div style={{ color: "#555", fontSize: 13 }}>Approve only if the code on that computer's screen matches.</div>
          </li>
        ))}
      </ul>
    </section>
  );
}
