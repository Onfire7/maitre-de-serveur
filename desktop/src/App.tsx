import { useEffect, useState } from "react";
import { createClient } from "@connectrpc/connect";
import { NodeService, type NodeInfo, type NodeMetrics } from "./gen/maitre/v1/node_pb";
import { NodeSchema, TrustedDeviceSchema, type Node as FleetNode, type TrustedDevice } from "./gen/maitre/v1/fleet_pb";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { InstallerService, Provisioning_Phase, type InstallerStatus } from "./gen/maitre/v1/installer_pb";
import { transportFor } from "./transport";
import { api, type ConsoleState, type DeviceInfo, type Found, type KnownNode } from "./state";
import { ServicesPanel } from "./services";
import { UpdatePanel, MaintenanceScreen, type Maintenance } from "./update";
import { NetbootPanel } from "./netboot";
import { JoinScreen, RecoverScreen, CodeScreen, AskingDevices } from "./join";
import { StoragePanel } from "./storage";
import { TiersPanel, useTiers } from "./tiers";
import { TailscalePanel, TailscaleRow } from "./tailscale";
import { UpdatePolicyPanel } from "./policy";
import { MediaPanel } from "./media";
import { nextLetter, randomNodeName } from "./names";

type Screen =
  | { name: "home" }
  | { name: "phrase"; phrase: string }
  | { name: "add"; found: Found }
  | { name: "node"; node: KnownNode }
  | { name: "code" };

export default function App() {
  const [device, setDevice] = useState<DeviceInfo | null>(null);
  const [state, setState] = useState<ConsoleState | null>(null);
  const [screen, setScreen] = useState<Screen>({ name: "home" });
  const [firstRun, setFirstRun] = useState<"create" | "join" | "recover" | "code">("create");

  useEffect(() => {
    api.deviceInfo().then(setDevice);
    api.getState().then(setState);
  }, []);

  async function save(next: ConsoleState) {
    await api.setState(next);
    setState(next);
  }

  if (!device || !state) return <Shell>loading…</Shell>;

  if (!state.fleet) {
    const joined = async (s: ConsoleState) => {
      await save(s);
      setFirstRun("create");
      setScreen({ name: "home" });
    };
    return (
      <Shell>
        {firstRun === "create" && (
          <>
            <CreateFleet
              onCreated={async (phrase) => {
                setState(await api.getState());
                setScreen({ name: "phrase", phrase });
              }}
            />
            <section style={{ marginTop: 24, color: "#555" }}>
              <p>Already have a Fleet?</p>
              <button onClick={() => setFirstRun("join")}>Join it from another computer that manages it</button>{" "}
              <button onClick={() => setFirstRun("recover")}>Recover with your phrase</button>{" "}
              <button onClick={() => setFirstRun("code")}>Join with a code from a Node</button>
            </section>
          </>
        )}
        {firstRun === "join" && <JoinScreen device={device} onJoined={joined} onCancel={() => setFirstRun("create")} />}
        {firstRun === "recover" && <RecoverScreen device={device} onJoined={joined} onCancel={() => setFirstRun("create")} />}
        {firstRun === "code" && <CodeScreen device={device} onJoined={joined} onCancel={() => setFirstRun("create")} />}
      </Shell>
    );
  }

  switch (screen.name) {
    case "phrase":
      return (
        <Shell>
          <RecoveryPhrase phrase={screen.phrase} onDone={() => setScreen({ name: "home" })} />
        </Shell>
      );
    case "add":
      return (
        <Shell>
          <AddNode
            found={screen.found}
            device={device}
            state={state}
            onCancel={() => setScreen({ name: "home" })}
            onProvisioned={async (node) => {
              await save({ ...state, nodes: [...state.nodes.filter((n) => n.node_id !== node.node_id), node] });
              setScreen({ name: "home" });
            }}
          />
        </Shell>
      );
    case "code":
      return (
        <Shell>
          <CodeScreen device={device} promote onJoined={async (s) => { await save(s); setScreen({ name: "home" }); }} onCancel={() => setScreen({ name: "home" })} />
        </Shell>
      );
    case "node":
      return (
        <Shell>
          <NodeView
            node={screen.node}
            onBack={() => setScreen({ name: "home" })}
            state={state}
            onRenamed={async (name) => {
              const nodes = state.nodes.map((n) => (n.node_id === screen.node.node_id ? { ...n, name } : n));
              await save({ ...state, nodes });
              setScreen({ name: "node", node: { ...screen.node, name } });
            }}
          />
        </Shell>
      );
    default:
      return (
        <Shell>
          <Home
            device={device}
            state={state}
            onAdd={(found) => setScreen({ name: "add", found })}
            onPromote={() => setScreen({ name: "code" })}
            onOpen={(node) => {
              // Persist the address the Node answered on last.
              if (node.addresses[0] !== state.nodes.find((n) => n.node_id === node.node_id)?.addresses[0])
                save({ ...state, nodes: state.nodes.map((n) => (n.node_id === node.node_id ? node : n)) });
              setScreen({ name: "node", node });
            }}
            onAddDev={(node) => save({ ...state, nodes: [...state.nodes.filter((n) => n.node_id !== node.node_id), node] })}
            onForget={(node) => save({ ...state, nodes: state.nodes.filter((n) => n.node_id !== node.node_id) })}
            onAddresses={(nodes) => save({ ...state, nodes })}
          />
        </Shell>
      );
  }
}

function Shell({ children }: { children: React.ReactNode }) {
  return (
    <main style={{ fontFamily: "system-ui", padding: 24, maxWidth: 760, margin: "0 auto" }}>
      <h1 style={{ marginBottom: 4 }}>Maître Desktop</h1>
      {children}
    </main>
  );
}

function CreateFleet({ onCreated }: { onCreated: (phrase: string) => void }) {
  const [name, setName] = useState("Home");
  const [error, setError] = useState("");
  return (
    <section>
      <h2>Create your Fleet</h2>
      <p style={{ color: "#555" }}>A Fleet is the set of servers you manage from this app, usually everything in one home.</p>
      <label>
        Fleet name{" "}
        <input value={name} onChange={(e) => setName(e.target.value)} style={{ width: 240 }} />
      </label>
      <br />
      <button
        style={{ marginTop: 12 }}
        disabled={!name.trim()}
        onClick={async () => {
          try {
            onCreated(await api.createFleet(name));
          } catch (e) {
            setError(String(e));
          }
        }}
      >
        Create Fleet
      </button>
      {error && <ErrorBox>{error}</ErrorBox>}
    </section>
  );
}

function RecoveryPhrase({ phrase, onDone }: { phrase: string; onDone: () => void }) {
  const [ack, setAck] = useState(false);
  return (
    <section>
      <h2>Your Recovery Phrase</h2>
      <p>
        If you ever lose every computer that has Maître Desktop, these twelve words let a new one take over your Fleet.
        Write them down and keep them somewhere safe. They are shown only once and are not stored anywhere.
      </p>
      <pre style={{ fontSize: 20, padding: 16, background: "#f4f4f4", borderRadius: 8, whiteSpace: "pre-wrap" }}>{phrase}</pre>
      <label>
        <input type="checkbox" checked={ack} onChange={(e) => setAck(e.target.checked)} /> I have written these words down
      </label>
      <br />
      <button style={{ marginTop: 12 }} disabled={!ack} onClick={onDone}>
        Continue
      </button>
    </section>
  );
}

function Home({
  device,
  state,
  onAdd,
  onOpen,
  onAddDev,
  onForget,
  onAddresses,
  onPromote,
}: {
  device: DeviceInfo;
  state: ConsoleState;
  onAdd: (f: Found) => void;
  onOpen: (n: KnownNode) => void;
  onAddDev: (n: KnownNode) => void;
  onForget: (n: KnownNode) => void;
  onAddresses: (nodes: KnownNode[]) => void;
  onPromote: () => void;
}) {
  const [found, setFound] = useState<Found[]>([]);
  // Decision 120: this device may be trusted by one Node only so far.
  const [scope, setScope] = useState<{ nodeId: string; addedUnixMs: number } | null>(null);
  useEffect(() => {
    let stop = false;
    (async () => {
      for (const n of state.nodes)
        for (const addr of n.addresses) {
          try {
            const recs = (await createClient(NodeService, transportFor({ addr, fingerprint: n.node_id })).listRecords({ kind: "trusted_device" })).records.filter((r) => !r.deleted);
            const me = recs.map((r) => fromBinary(TrustedDeviceSchema, r.body)).find((d) => d.deviceId === device.fingerprint);
            if (!stop) setScope(me?.nodeScope ? { nodeId: me.nodeScope, addedUnixMs: Number(me.addedUnixMs) } : null);
            return;
          } catch {
            /* next */
          }
        }
    })();
    return () => {
      stop = true;
    };
  }, [state.nodes.map((n) => n.node_id).join(","), device.fingerprint]);
  const [showDev, setShowDev] = useState(false);
  const [devAddr, setDevAddr] = useState("127.0.0.1:7443");
  const [devFp, setDevFp] = useState("");
  const [byAddr, setByAddr] = useState("");

  useEffect(() => {
    const tick = () => api.discover().then(setFound).catch(() => {});
    tick();
    const t = setInterval(tick, 2000);
    return () => clearInterval(t);
  }, []);

  // Milestone 3 repair: Nodes provisioned before the sync layer existed (or
  // while their sponsor was down) do not know each other. Whenever two known
  // Nodes answer and one lacks the other's record, hand it over; from then
  // on they sync by themselves.
  useEffect(() => {
    let stop = false;
    const introduce = async () => {
      const alive: { node: KnownNode; addr: string; records: Map<string, Uint8Array> }[] = [];
      for (const n of state.nodes) {
        for (const a of n.addresses) {
          try {
            const recs = (await createClient(NodeService, transportFor({ addr: a, fingerprint: n.node_id })).listRecords({ kind: "node" })).records;
            alive.push({ node: n, addr: a, records: new Map(recs.filter((r) => !r.deleted).map((r) => [r.id, r.body])) });
            break;
          } catch {
            /* offline or unreachable here */
          }
        }
      }
      if (stop) return;
      // Addresses in the records (LAN leases, and the tailnet address a Node
      // adds once it joins Tailscale) become fallbacks for every known Node,
      // so the Console reaches a Node away from home (Milestone 5).
      let changed = false;
      const next = state.nodes.map((n) => {
        for (const a of alive) {
          const own = a.records.get(n.node_id);
          if (!own) continue;
          const rec = fromBinary(NodeSchema, own);
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
            await createClient(NodeService, transportFor({ addr: a.addr, fingerprint: a.node.node_id })).putRecord({ record: { kind: "node", id: b.node.node_id, body: own } });
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
  }, [state.nodes]);

  const installers = found.filter((f) => f.kind === "installer");
  const knownIds = new Set(state.nodes.map((n) => n.node_id));

  return (
    <>
      <p style={{ color: "#555", marginTop: 0 }}>
        Fleet <b>{state.fleet?.name}</b> · this device is <b>{device.name}</b>
      </p>
      {scope && (
        <div style={{ border: "1px solid #a60", borderRadius: 6, padding: 8, marginBottom: 12, fontSize: 13 }}>
          <b>Trusted by {state.nodes.find((n) => n.node_id === scope.nodeId)?.name ?? "one Node"} only.</b> The other Nodes do not answer this device yet. To be trusted everywhere, reboot any other Node into
          recovery from its keyboard and enter its code here{scope.addedUnixMs ? ` before ${new Date(scope.addedUnixMs + 24 * 3600 * 1000).toLocaleString()}` : ""}.{" "}
          <button onClick={onPromote}>Enter a code from another Node</button>
        </div>
      )}

      <section>
        <h2>Nodes</h2>
        {state.nodes.length === 0 && <p style={{ color: "#555" }}>No Nodes yet. Boot a machine from the network or from the Maître installer USB and it will appear below.</p>}
        <ul style={{ listStyle: "none", padding: 0 }}>
          {[...state.nodes].sort((a, b) => a.name.localeCompare(b.name)).map((n) => (
            <NodeRow
              key={n.node_id}
              node={n}
              live={found.find((f) => f.kind === "node" && f.id === n.node_id)}
              onOpen={(addr) => onOpen({ ...n, addresses: [addr, ...n.addresses.filter((a) => a !== addr)] })}
              onForget={() => onForget(n)}
              state={state}
              onAddress={(addr) => onAddDev({ ...n, addresses: [addr, ...n.addresses.filter((a) => a !== addr)] })}
            />
          ))}
        </ul>
      </section>

      <AskingDevices state={state} found={found} />

      <TiersPanel state={state} />

      <TailscalePanel state={state} />

      <UpdatePolicyPanel state={state} />

      <section>
        <h2>Add a Node</h2>
        <NetbootPanel />
        <MediaPanel />
        {installers.length === 0 ? (
          <p style={{ color: "#555" }}>Looking for machines booted from the installer… none found on this network yet.</p>
        ) : (
          <ul style={{ listStyle: "none", padding: 0 }}>
            {installers.map((f) => (
              <li key={f.id} style={row}>
                <div>
                  <b>{f.machine || "A machine"}</b> is ready to install
                  <div style={{ color: "#555", fontSize: 13 }}>
                    {f.addresses[0]} · installer {f.id.slice(0, 8)}
                  </div>
                </div>
                <button onClick={() => onAdd(f)}>Set up</button>
              </li>
            ))}
          </ul>
        )}
        <p style={{ color: "#555", fontSize: 13 }}>
          Machine not listed (different network, or a VM)? Set it up by address:{" "}
          <input value={byAddr} onChange={(e) => setByAddr(e.target.value)} placeholder="192.168.1.20:7443" style={{ width: 180 }} />{" "}
          <button disabled={!byAddr.includes(":")} onClick={() => onAdd({ kind: "installer", id: "", name: "", machine: "", fleet_id: "", version: "", addresses: [byAddr.trim()], key_pem: "", recovery: false })}>
            Set up by address
          </button>
        </p>
        {found
          .filter((f) => f.kind === "node" && !knownIds.has(f.id))
          .map((f) => (
            <p key={f.id} style={{ color: "#555", fontSize: 13 }}>
              Node <b>{f.name}</b> ({f.id.slice(0, 8)}) is on this network but belongs to {f.fleet_id === state.fleet?.fleet_id ? "this Fleet; joining from a second device arrives in Milestone 3" : "another Fleet"}.
            </p>
          ))}
      </section>

      <details open={showDev} onToggle={(e) => setShowDev((e.target as HTMLDetailsElement).open)} style={{ marginTop: 32, color: "#555" }}>
        <summary>Developer</summary>
        <p style={{ fontSize: 13 }}>
          Device fingerprint <code>{device.fingerprint}</code>
          <br />
          Public key file <code>{device.public_key_path}</code>
        </p>
        <p style={{ fontSize: 13 }}>Add a <code>maitre dev</code> Node by hand (its trust dir must already contain this device's public key):</p>
        <input value={devAddr} onChange={(e) => setDevAddr(e.target.value)} style={{ width: 180 }} />{" "}
        <input value={devFp} onChange={(e) => setDevFp(e.target.value)} placeholder="node fingerprint" style={{ width: 360, fontFamily: "monospace" }} />{" "}
        <button disabled={!devAddr || devFp.length !== 64} onClick={() => onAddDev({ node_id: devFp.trim().toLowerCase(), name: "dev node", addresses: [devAddr] })}>
          Add
        </button>
      </details>
    </>
  );
}

function NodeRow({
  node,
  live,
  onOpen,
  onForget,
  onAddress,
  state,
}: {
  node: KnownNode;
  state: ConsoleState;
  live?: Found;
  /** Called with the address that answered (or the one being tried). */
  onOpen: (addr: string) => void;
  onForget: () => void;
  onAddress: (addr: string) => void;
}) {
  const [info, setInfo] = useState<NodeInfo | null>(null);
  const [err, setErr] = useState("");
  const [editing, setEditing] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [manual, setManual] = useState("");
  // The stored address (which a manual entry overrides) is tried first;
  // discovery is the fallback, since it can be wrong too.
  const [useLive, setUseLive] = useState(false);
  const liveAddr = live?.addresses[0];
  const addr = useLive && liveAddr ? liveAddr : node.addresses[0];
  useEffect(() => {
    let stop = false;
    const client = createClient(NodeService, transportFor({ addr, fingerprint: node.node_id }));
    const tick = async () => {
      try {
        const r = await client.getNodeInfo({});
        if (!stop) {
          setInfo(r.node ?? null);
          setErr("");
        }
      } catch (e) {
        if (!stop) {
          setInfo(null);
          setErr(String(e));
          if (liveAddr && liveAddr !== addr) setUseLive(true);
          else if (useLive) setUseLive(false);
        }
      }
    };
    tick();
    const t = setInterval(tick, 3000);
    return () => {
      stop = true;
      clearInterval(t);
    };
  }, [addr, liveAddr, node.node_id]);
  return (
    <li style={row}>
      <div>
        <b>{node.name}</b>{" "}
        {info ? <span style={{ color: "#2a7" }}>● online</span> : <span style={{ color: "#999" }} title={err}>● unreachable</span>}
        <div style={{ color: "#555", fontSize: 13 }}>
          {addr} · {info ? `version ${info.version}, up ${formatUptime(Number(info.uptimeSeconds))}` : err.includes("trust") ? "this device is not trusted by that Node" : "waiting…"}
        </div>
      </div>
      <div>
        <button onClick={() => onOpen(addr)}>Open</button> <button onClick={() => setEditing((e) => !e)}>Address…</button>{" "}
        <button onClick={() => setRemoving(true)}>Remove…</button>
        {removing && <RemoveNodeDialog node={node} state={state} onCancel={() => setRemoving(false)} onForget={onForget} />}
        {editing && (
          <div style={{ marginTop: 4 }}>
            <input value={manual} onChange={(e) => setManual(e.target.value)} placeholder="192.168.4.80:7443" style={{ width: 180 }} />{" "}
            <button
              disabled={!manual.includes(":")}
              onClick={() => {
                onAddress(manual.trim());
                setEditing(false);
              }}
            >
              Use
            </button>
            <div style={{ color: "#555", fontSize: 12 }}>For when discovery gets it wrong. The Node's own screen shows its address.</div>
          </div>
        )}
      </div>
    </li>
  );
}

/** The guard (Milestone 4): removing a Node names what becomes unavailable and needs its name typed. */
function RemoveNodeDialog({ node, state, onCancel, onForget }: { node: KnownNode; state: ConsoleState; onCancel: () => void; onForget: () => void }) {
  const [services, setServices] = useState<string[] | null>(null);
  const [typed, setTyped] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    (async () => {
      for (const n of state.nodes)
        for (const a of n.addresses) {
          try {
            const recs = (await createClient(NodeService, transportFor({ addr: a, fingerprint: n.node_id })).listRecords({ kind: "service" })).records;
            setServices(recs.filter((r) => !r.deleted && r.ownerNodeId === node.node_id).map((r) => r.id));
            return;
          } catch {
            /* next */
          }
        }
      setServices([]);
    })();
  }, [node.node_id]);
  async function remove() {
    // Forget here always; remove from the Fleet through any Node that answers.
    for (const n of state.nodes.filter((x) => x.node_id !== node.node_id))
      for (const a of n.addresses) {
        try {
          await createClient(NodeService, transportFor({ addr: a, fingerprint: n.node_id })).putRecord({ record: { kind: "node", id: node.node_id, deleted: true } });
          onForget();
          return;
        } catch (e) {
          setError(String(e));
        }
      }
    onForget();
  }
  const other = state.nodes.length > 1;
  return (
    <div style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,.35)", display: "flex", alignItems: "center", justifyContent: "center" }}>
      <div style={{ background: "#fff", padding: 24, borderRadius: 8, width: 480 }}>
        <h3 style={{ marginTop: 0 }}>Remove {node.name} from the Fleet</h3>
        {other ? (
          <p>The other Nodes stop trusting it and forget it. Reinstall it to bring it back.</p>
        ) : (
          <p>It is the only Node, so this only forgets it on this computer.</p>
        )}
        {services === null ? (
          <p style={{ color: "#555" }}>Checking what runs on it…</p>
        ) : services.length > 0 ? (
          <div style={{ color: "#b00020", fontSize: 13 }}>
            Services that become unavailable: <b>{services.join(", ")}</b>. Their data stays on that machine's Drives.
          </div>
        ) : (
          <p style={{ color: "#555", fontSize: 13 }}>No Services run on it.</p>
        )}
        {error && <ErrorBox>{error}</ErrorBox>}
        <p>
          Type <code>{node.name}</code> to confirm: <input value={typed} onChange={(e) => setTyped(e.target.value)} style={{ width: 200 }} />
        </p>
        <div style={{ display: "flex", gap: 8 }}>
          <button disabled={typed.trim() !== node.name || services === null} onClick={remove}>
            Remove
          </button>
          <button onClick={onCancel}>Cancel</button>
        </div>
      </div>
    </div>
  );
}

function AddNode({
  found,
  device,
  state,
  onCancel,
  onProvisioned,
}: {
  found: Found;
  device: DeviceInfo;
  state: ConsoleState;
  onCancel: () => void;
  onProvisioned: (node: KnownNode) => void;
}) {
  // Try each advertised address in turn; the user can override when none of
  // them is reachable from here (e.g. a VM behind NAT advertising its guest IP).
  const [addr, setAddr] = useState(found.addresses[0]);
  // QEMU's user-mode network gives guests 10.0.2.x; `make vm` forwards the
  // agent port to 127.0.0.1:17443 on the host, so suggest that.
  const qemuGuest = found.addresses.some((a) => a.startsWith("10.0.2."));
  const [altAddr, setAltAddr] = useState(qemuGuest ? "127.0.0.1:17443" : "");
  const [attempt, setAttempt] = useState(0);
  const [fp, setFp] = useState("");
  const [pem, setPem] = useState("");
  const [code, setCode] = useState("");
  const [status, setStatus] = useState<InstallerStatus | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const fleetNodes = useTiers(state).nodes;
  const letter = nextLetter(fleetNodes.map((n) => n.letter));
  const [name, setName] = useState("");
  useEffect(() => {
    if (!name) setName(randomNodeName(letter, state.nodes.map((n) => n.name)));
  }, [letter]);
  const [disk, setDisk] = useState("");
  const [dataPart, setDataPart] = useState(false);
  const [started, setStarted] = useState(false);
  const [restore, setRestore] = useState(""); // node_id of a backup to restore, "" for a fresh Node
  const [error, setError] = useState("");

  // Step 1: learn the Installer's key and compute the code we expect.
  useEffect(() => {
    let stop = false;
    const candidates = attempt === 0 ? found.addresses : [addr];
    (async () => {
      let lastErr = "";
      for (const a of candidates) {
        try {
          const p = await api.peekInstaller(a);
          if (stop) return;
          if (found.id && p.fingerprint !== found.id) throw new Error("the machine's key does not match what it advertised; try again");
          setAddr(a);
          setFp(p.fingerprint);
          setPem(p.public_key_pem);
          setCode(p.pairing_code);
          setError("");
          return;
        } catch (e) {
          lastErr = String(e);
        }
      }
      if (!stop) setError(lastErr);
    })();
    return () => {
      stop = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [attempt, found.id]);

  // Step 2: poll status over the pinned connection; the Installer must show the same code.
  useEffect(() => {
    if (!fp) return;
    const client = createClient(InstallerService, transportFor({ addr, fingerprint: fp }));
    let stop = false;
    const tick = async () => {
      try {
        const r = await client.getInstallerStatus({});
        if (stop || !r.status) return;
        if (r.status.pairingCode !== code) throw new Error("pairing code mismatch between this device and the installer");
        setStatus(r.status);
        setError("");
        if (started && r.status.provisioning?.phase === Provisioning_Phase.REBOOTING) {
          stop = true;
          const reported = r.status.addresses.map((a) => `${a.includes(":") ? `[${a}]` : a}:7443`);
          // A restored Node keeps its old key, so it is known by its old id.
          const b = restore ? r.status.backups.find((x) => x.nodeId === restore) : undefined;
          onProvisioned({ node_id: b ? b.nodeId : fp, name: b ? b.nodeName : name, addresses: [addr, ...reported.filter((a) => a !== addr)] });
        }
        if (r.status.provisioning?.phase === Provisioning_Phase.FAILED) setStarted(false);
      } catch (e) {
        if (!stop) setError(String(e));
      }
    };
    tick();
    const t = setInterval(tick, 1500);
    return () => {
      stop = true;
      clearInterval(t);
    };
  }, [fp, code, started, name, addr, restore, onProvisioned]);

  const disks = status?.disks.filter((d) => !d.isInstallerMedia) ?? [];
  const chosen = disks.find((d) => d.path === disk);
  // Milestone 4: a Node whose OS lives on a USB stick must have somewhere
  // else for data: the rest of the stick, or a Pool elsewhere in the Fleet.
  const usbNeedsData = !!chosen?.removable && !dataPart && state.nodes.length === 0;
  const prov = status?.provisioning;

  async function provision() {
    if (!state.fleet || !chosen) return;
    setError("");
    const client = createClient(InstallerService, transportFor({ addr, fingerprint: fp }));
    try {
      // Milestone 3: a Fleet with Nodes already gives the newcomer its
      // current devices and Nodes, and is told about the newcomer in turn,
      // through whichever existing Node answers.
      let trustedDevices: TrustedDevice[] = [
        create(TrustedDeviceSchema, { name: device.name, publicKeyPem: device.public_key_pem, recoveryOnly: false }),
        create(TrustedDeviceSchema, { name: "Recovery Phrase", publicKeyPem: state.fleet.recovery_public_key_pem, recoveryOnly: true }),
      ];
      let nodes: FleetNode[] = [];
      let sponsor: { addr: string; fingerprint: string } | null = null;
      for (const n of state.nodes) {
        for (const a of n.addresses) {
          try {
            const nc = createClient(NodeService, transportFor({ addr: a, fingerprint: n.node_id }));
            const recs = (await nc.listRecords({})).records.filter((r) => !r.deleted);
            trustedDevices = recs.filter((r) => r.kind === "trusted_device").map((r) => fromBinary(TrustedDeviceSchema, r.body));
            nodes = recs.filter((r) => r.kind === "node").map((r) => fromBinary(NodeSchema, r.body));
            sponsor = { addr: a, fingerprint: n.node_id };
            break;
          } catch {
            /* try the next address or Node */
          }
        }
        if (sponsor) break;
      }
      if (state.nodes.length > 0 && !sponsor) throw new Error("none of the Fleet's Nodes can be reached to add this one; bring one online first");
      if (!restore && nodes.some((n) => n.name === name.trim())) throw new Error(`the Fleet already has a Node named "${name.trim()}"`);
      if (restore) {
        await client.provision({ targetDisk: chosen.path, dataPartition: dataPart, restoreNodeId: restore, fleet: { fleetId: state.fleet.fleet_id }, nodeName: "restore" });
        setStarted(true);
        return;
      }
      await client.provision({
        fleet: {
          fleetId: state.fleet.fleet_id,
          name: state.fleet.name,
          createdUnixMs: BigInt(state.fleet.created_unix_ms),
        },
        nodeName: name.trim(),
        targetDisk: chosen.path,
        dataPartition: dataPart,
        trustedDevices,
        nodes,
      });
      setStarted(true);
      if (sponsor) {
        const body = toBinary(NodeSchema, create(NodeSchema, { nodeId: fp, name: name.trim(), publicKeyPem: pem, provisionedUnixMs: BigInt(Date.now()) }));
        await createClient(NodeService, transportFor(sponsor)).putRecord({ record: { kind: "node", id: fp, body } });
      }
    } catch (e) {
      setError(String(e));
    }
  }

  return (
    <section>
      <h2>Set up {found.machine || "this machine"}</h2>
      <p style={{ color: "#555" }}>{addr}</p>

      {!confirmed && (
        <>
          <h3>1. Check the pairing code</h3>
          {code ? (
            <>
              <p>The machine's screen shows a six-digit code. Does it match this one?</p>
              <pre style={{ fontSize: 32, textAlign: "center", padding: 16, background: "#f4f4f4", borderRadius: 8 }}>{code}</pre>
              <button disabled={!status} onClick={() => setConfirmed(true)}>
                Yes, it matches
              </button>{" "}
              <button onClick={onCancel}>No, cancel</button>
            </>
          ) : error ? (
            <>
              <p>Could not reach the machine at {found.addresses.join(", ")}.</p>
              <p>
                If it is reachable at a different address or port, enter it
                {qemuGuest ? " (this looks like the development VM from `make vm`, which the host reaches at 127.0.0.1:17443)" : " (for example a VM behind NAT)"}:{" "}
                <input value={altAddr} onChange={(e) => setAltAddr(e.target.value)} placeholder="192.168.1.20:7443" style={{ width: 200 }} />{" "}
                <button
                  disabled={!altAddr.includes(":")}
                  onClick={() => {
                    setAddr(altAddr.trim());
                    setAttempt((n) => n + 1);
                  }}
                >
                  Retry
                </button>
              </p>
            </>
          ) : (
            <p>Contacting the machine…</p>
          )}
        </>
      )}

      {confirmed && !started && (
        <>
          <h3>2. Name it and pick the disk</h3>
          {status && status.backups.length > 0 && (
            <div style={{ border: "1px solid #c90", borderRadius: 6, padding: 8, marginBottom: 8 }}>
              <b>A Drive in this machine holds a backup of a Node.</b>
              {status.backups.map((b) => (
                <label key={b.nodeId} style={{ display: "block", marginTop: 4 }}>
                  <input type="radio" name="restore" checked={restore === b.nodeId} onChange={() => setRestore(b.nodeId)} /> Restore <b>{b.nodeName}</b> of Fleet "{b.fleetName}" (saved{" "}
                  {new Date(Number(b.savedUnixMs)).toLocaleString()}, on {b.device})
                </label>
              ))}
              <label style={{ display: "block", marginTop: 4 }}>
                <input type="radio" name="restore" checked={restore === ""} onChange={() => setRestore("")} /> Set up a new Node instead
              </label>
            </div>
          )}
          <label>
            Node name{" "}
            <input value={name} onChange={(e) => setName(e.target.value)} style={{ width: 240 }} />
          </label>{" "}
          <button type="button" title="Draw another name" onClick={() => setName(randomNodeName(letter, state.nodes.map((n) => n.name)))}>
            🎲
          </button>
          <span style={{ color: "#555", fontSize: 13 }}>
            {" "}
            This will be Node {letter || "?"}; its name starts with that letter, like its Drives.{found.machine ? ` The machine calls itself ${found.machine}.` : ""}
          </span>
          <h4 style={{ marginBottom: 4 }}>Install Maître Server on</h4>
          <p style={{ color: "#b00020", fontSize: 13, marginTop: 0 }}>Everything on the chosen disk will be erased.</p>
          {status?.image && (
            <p style={{ color: "#555", fontSize: 13, marginTop: 0 }}>
              It will install <code>{status.image}</code>
              {status.source && status.source.startsWith("oci:") ? " from the copy on its media" : status.source ? ` from ${status.source}` : " from the registry"}
              {status.insecureRegistry && <span style={{ color: "#a60" }}> (pulling from {status.insecureRegistry} without TLS: a developer Depot)</span>}. If that is not what you expect, stop here.
            </p>
          )}
          {disks.length === 0 && <p>No other disks found on this machine.</p>}
          {disks.map((d) => (
            <label key={d.path} style={{ display: "block", padding: 4 }}>
              <input
                type="radio"
                name="disk"
                checked={disk === d.path}
                onChange={() => {
                  setDisk(d.path);
                  setDataPart(!d.removable);
                }}
              />{" "}
              <b>{d.model || d.path}</b> · {formatBytes(Number(d.sizeBytes))} {d.removable && "· USB/removable"}
              <span style={{ color: "#555", fontSize: 13 }}> {d.path}</span>
            </label>
          ))}
          {chosen && (
            <label style={{ display: "block", marginTop: 8 }}>
              <input type="checkbox" checked={dataPart} onChange={(e) => setDataPart(e.target.checked)} /> Use the rest of this disk for data
              {usbNeedsData && <span style={{ color: "#a60", fontSize: 13 }}> (required: a USB stick as the only disk needs this, or another Node with a Pool)</span>}
              {chosen.removable && <span style={{ color: "#555", fontSize: 13 }}> (off by default on USB drives)</span>}
            </label>
          )}
          <button style={{ marginTop: 12 }} disabled={!chosen || !name.trim() || usbNeedsData} onClick={provision}>
            Install
          </button>{" "}
          <button onClick={onCancel}>Cancel</button>
        </>
      )}

      {started && prov && (
        <>
          <h3>3. Installing</h3>
          <progress value={prov.percent} max={100} style={{ width: "100%" }} />
          <p>{prov.message}</p>
          <p style={{ color: "#555", fontSize: 13 }}>Do not power the machine off. It will restart on its own when done.</p>
        </>
      )}

      {error && <ErrorBox>{error}</ErrorBox>}
      {prov?.phase === Provisioning_Phase.FAILED && !started && <ErrorBox>Install failed: {prov.message}</ErrorBox>}
    </section>
  );
}

function NodeView({ node, onBack, state, onRenamed }: { node: KnownNode; onBack: () => void; state: ConsoleState; onRenamed: (name: string) => void }) {
  const [maint, setMaint] = useState<Maintenance | null>(null);
  if (maint)
    return (
      <MaintenanceScreen
        target={{ addr: node.addresses[0], fingerprint: node.node_id }}
        name={node.name}
        m={maint}
        onDone={() => setMaint(null)}
        onBack={onBack}
      />
    );
  return <NodeDetails node={node} onBack={onBack} onMaintenance={setMaint} state={state} onRenamed={onRenamed} />;
}

function NodeDetails({
  node,
  onBack,
  onMaintenance,
  state,
  onRenamed,
}: {
  node: KnownNode;
  onBack: () => void;
  onMaintenance: (m: Maintenance) => void;
  state: ConsoleState;
  onRenamed: (name: string) => void;
}) {
  const { tiers, nodes: fleetNodes } = useTiers(state);
  const myLetter = fleetNodes.find((n) => n.nodeId === node.node_id)?.letter || "";
  const [renaming, setRenaming] = useState<string | null>(null);
  const [info, setInfo] = useState<NodeInfo | null>(null);
  const [metrics, setMetrics] = useState<NodeMetrics | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    const client = createClient(NodeService, transportFor({ addr: node.addresses[0], fingerprint: node.node_id }));
    const tick = () =>
      client
        .getNodeInfo({})
        .then(async (r) => {
          setInfo(r.node ?? null);
          setError("");
          if (!r.node?.devMode) setMetrics((await client.getNodeMetrics({})).metrics ?? null);
        })
        .catch((e) => setError(String(e)));
    tick();
    const t = setInterval(tick, 2000);
    return () => clearInterval(t);
  }, [node]);
  return (
    <section>
      <button onClick={onBack}>← Nodes</button>
      {renaming === null ? (
        <h2>
          {node.name}{" "}
          <button style={{ fontSize: 13, fontWeight: "normal" }} onClick={() => setRenaming(node.name)}>
            Rename…
          </button>
        </h2>
      ) : (
        <form
          style={{ display: "flex", gap: 6, alignItems: "center", margin: "12px 0" }}
          onSubmit={async (e) => {
            e.preventDefault();
            const name = renaming.trim();
            if (!name) return;
            if (state.nodes.some((n) => n.node_id !== node.node_id && n.name.toLowerCase() === name.toLowerCase())) {
              setError(`the Fleet already has a Node named "${name}"`);
              return;
            }
            try {
              await createClient(NodeService, transportFor({ addr: node.addresses[0], fingerprint: node.node_id })).renameNode({ name });
              setRenaming(null);
              setError("");
              onRenamed(name);
            } catch (err) {
              setError(String(err));
            }
          }}
        >
          <input autoFocus value={renaming} onChange={(e) => setRenaming(e.target.value)} maxLength={40} style={{ fontSize: 18, width: 260 }} />
          <button type="button" title="Draw another name" onClick={() => setRenaming(randomNodeName(myLetter, state.nodes.map((n) => n.name)))}>
            🎲
          </button>
          <button type="submit">Save</button>
          <button type="button" onClick={() => setRenaming(null)}>
            Cancel
          </button>
          <span style={{ color: "#555", fontSize: 13 }}>
            {myLetter ? `Node ${myLetter}: names start with ${myLetter}. ` : ""}The host name follows now; the Tailscale name on the next restart.
          </span>
        </form>
      )}
      {error && <ErrorBox>{error}</ErrorBox>}
      {info && (
        <table>
          <tbody>
            <tr><td>Name</td><td>{info.name}</td></tr>
            <tr><td>Node ID</td><td><code>{info.nodeId}</code></td></tr>
            <tr><td>Fleet</td><td>{info.fleetId || "(none)"}</td></tr>
            <tr><td>Version</td><td>{info.version}</td></tr>
            <tr><td>Uptime</td><td>{formatUptime(Number(info.uptimeSeconds))}</td></tr>
            <tr><td>Address</td><td>{node.addresses[0]}</td></tr>
            <tr><td>Dev mode</td><td>{info.devMode ? "yes" : "no"}</td></tr>
            <TailscaleRow info={info} />
          </tbody>
        </table>
      )}
      {metrics && <Gauges m={metrics} />}
      {info && info.peers.length > 0 && (
        <section>
          <h3>Other Nodes in the Fleet</h3>
          <ul style={{ listStyle: "none", padding: 0 }}>
            {info.peers.map((p) => (
              <li key={p.nodeId} style={{ padding: "4px 0" }}>
                <b>{p.name}</b> {p.online ? <span style={{ color: "#2a7" }}>● in sync</span> : <span style={{ color: "#999" }}>● offline</span>}{" "}
                <span style={{ color: "#555", fontSize: 13 }}>
                  {p.address}
                  {p.queued > 0 && ` · ${p.queued} change(s) waiting for it`}
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}
      {info && !info.devMode && <StoragePanel target={{ addr: node.addresses[0], fingerprint: node.node_id }} />}
      {info && !info.devMode && <ServicesPanel target={{ addr: node.addresses[0], fingerprint: node.node_id }} tiers={tiers} remoteReady={info.tailscale?.state === "Running"} publicIp={info.publicIp} portMapping={info.portMapping} />}
      {info && !info.devMode && <UpdatePanel target={{ addr: node.addresses[0], fingerprint: node.node_id }} onStarted={onMaintenance} runningVersion={info.version} />}
      {info?.devMode && <p style={{ color: "#555" }}>Services are not available on a dev Node.</p>}
    </section>
  );
}

function Gauges({ m }: { m: NodeMetrics }) {
  const pct = (used: bigint, total: bigint) => (total > 0n ? Number((used * 1000n) / total) / 10 : 0);
  const memPct = pct(m.memoryUsedBytes, m.memoryTotalBytes);
  const poolPct = pct(m.poolUsedBytes, m.poolTotalBytes);
  return (
    <section style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(150px, 1fr))", gap: 12, margin: "16px 0" }}>
      <Gauge label="CPU" value={`${m.cpuPercent.toFixed(0)}%`} pct={m.cpuPercent} sub={`load ${m.load1m.toFixed(2)}`} />
      <Gauge label="Memory" value={`${memPct.toFixed(0)}%`} pct={memPct} sub={`${formatBytes(Number(m.memoryUsedBytes))} of ${formatBytes(Number(m.memoryTotalBytes))}`} />
      <Gauge
        label="Storage"
        value={`${poolPct.toFixed(0)}%`}
        pct={poolPct}
        sub={m.poolFallback ? "no Drive: data lives on the OS disk" : `${formatBytes(Number(m.poolUsedBytes))} of ${formatBytes(Number(m.poolTotalBytes))}`}
        warn={m.poolFallback}
      />
      <Gauge label="Network" value={`↓ ${formatBytes(m.netRxBytesPerSec)}/s`} sub={`↑ ${formatBytes(m.netTxBytesPerSec)}/s`} />
      {m.temperatureC > 0 && <Gauge label="Temperature" value={`${m.temperatureC.toFixed(0)} °C`} pct={m.temperatureC} />}
    </section>
  );
}

function Gauge({ label, value, pct, sub, warn }: { label: string; value: string; pct?: number; sub?: string; warn?: boolean }) {
  return (
    <div style={{ border: "1px solid #eee", borderRadius: 8, padding: 10 }}>
      <div style={{ color: "#555", fontSize: 12 }}>{label}</div>
      <div style={{ fontSize: 22 }}>{value}</div>
      {pct !== undefined && (
        <div style={{ height: 4, background: "#eee", borderRadius: 2, margin: "6px 0" }}>
          <div style={{ width: `${Math.min(100, Math.max(0, pct))}%`, height: 4, background: pct > 90 ? "#b00020" : "#2a7", borderRadius: 2 }} />
        </div>
      )}
      {sub && <div style={{ color: warn ? "#a60" : "#555", fontSize: 12 }}>{sub}</div>}
    </div>
  );
}

function ErrorBox({ children }: { children: React.ReactNode }) {
  return <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>{children}</pre>;
}

const row: React.CSSProperties = { display: "flex", justifyContent: "space-between", alignItems: "center", padding: "8px 0", borderBottom: "1px solid #eee" };

function formatBytes(n: number) {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1000 && i < units.length - 1) {
    n /= 1000;
    i++;
  }
  return `${n.toFixed(i >= 3 ? 1 : 0)} ${units[i]}`;
}
function formatUptime(s: number) {
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}
