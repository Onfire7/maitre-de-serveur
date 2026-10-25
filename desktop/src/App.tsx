import { useEffect, useState } from "react";
import { createClient } from "@connectrpc/connect";
import { NodeService, type NodeInfo, type NodeMetrics } from "./gen/maitre/v1/node_pb";
import { InstallerService, Provisioning_Phase, type InstallerStatus } from "./gen/maitre/v1/installer_pb";
import { transportFor } from "./transport";
import { api, type ConsoleState, type DeviceInfo, type Found, type KnownNode } from "./state";
import { ServicesPanel } from "./services";
import { UpdatePanel, MaintenanceScreen, type Maintenance } from "./update";

type Screen =
  | { name: "home" }
  | { name: "phrase"; phrase: string }
  | { name: "add"; found: Found }
  | { name: "node"; node: KnownNode };

export default function App() {
  const [device, setDevice] = useState<DeviceInfo | null>(null);
  const [state, setState] = useState<ConsoleState | null>(null);
  const [screen, setScreen] = useState<Screen>({ name: "home" });

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
    return (
      <Shell>
        <CreateFleet
          onCreated={async (phrase) => {
            setState(await api.getState());
            setScreen({ name: "phrase", phrase });
          }}
        />
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
    case "node":
      return (
        <Shell>
          <NodeView node={screen.node} onBack={() => setScreen({ name: "home" })} />
        </Shell>
      );
    default:
      return (
        <Shell>
          <Home
            device={device}
            state={state}
            onAdd={(found) => setScreen({ name: "add", found })}
            onOpen={(node) => {
              // Persist the address the Node answered on last.
              if (node.addresses[0] !== state.nodes.find((n) => n.node_id === node.node_id)?.addresses[0])
                save({ ...state, nodes: state.nodes.map((n) => (n.node_id === node.node_id ? node : n)) });
              setScreen({ name: "node", node });
            }}
            onAddDev={(node) => save({ ...state, nodes: [...state.nodes.filter((n) => n.node_id !== node.node_id), node] })}
            onForget={(node) => save({ ...state, nodes: state.nodes.filter((n) => n.node_id !== node.node_id) })}
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
}: {
  device: DeviceInfo;
  state: ConsoleState;
  onAdd: (f: Found) => void;
  onOpen: (n: KnownNode) => void;
  onAddDev: (n: KnownNode) => void;
  onForget: (n: KnownNode) => void;
}) {
  const [found, setFound] = useState<Found[]>([]);
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

  const installers = found.filter((f) => f.kind === "installer");
  const knownIds = new Set(state.nodes.map((n) => n.node_id));

  return (
    <>
      <p style={{ color: "#555", marginTop: 0 }}>
        Fleet <b>{state.fleet?.name}</b> · this device is <b>{device.name}</b>
      </p>

      <section>
        <h2>Nodes</h2>
        {state.nodes.length === 0 && <p style={{ color: "#555" }}>No Nodes yet. Boot a machine from the Maître installer USB and it will appear below.</p>}
        <ul style={{ listStyle: "none", padding: 0 }}>
          {state.nodes.map((n) => (
            <NodeRow
              key={n.node_id}
              node={n}
              live={found.find((f) => f.kind === "node" && f.id === n.node_id)}
              onOpen={() => {
                // A Node can come back from a reinstall or a new DHCP lease on
                // another address; what mDNS says now beats what we stored.
                const live = found.find((f) => f.kind === "node" && f.id === n.node_id);
                onOpen(live ? { ...n, addresses: [...live.addresses, ...n.addresses.filter((a) => !live.addresses.includes(a))] } : n);
              }}
              onForget={() => onForget(n)}
            />
          ))}
        </ul>
      </section>

      <section>
        <h2>Add a Node</h2>
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
          <button disabled={!byAddr.includes(":")} onClick={() => onAdd({ kind: "installer", id: "", name: "", machine: "", fleet_id: "", version: "", addresses: [byAddr.trim()] })}>
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

function NodeRow({ node, live, onOpen, onForget }: { node: KnownNode; live?: Found; onOpen: () => void; onForget: () => void }) {
  const [info, setInfo] = useState<NodeInfo | null>(null);
  const [err, setErr] = useState("");
  const addr = live?.addresses[0] ?? node.addresses[0];
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
        }
      }
    };
    tick();
    const t = setInterval(tick, 3000);
    return () => {
      stop = true;
      clearInterval(t);
    };
  }, [addr, node.node_id]);
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
        <button onClick={onOpen}>Open</button> <button onClick={onForget}>Forget</button>
      </div>
    </li>
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
  const [code, setCode] = useState("");
  const [status, setStatus] = useState<InstallerStatus | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const [name, setName] = useState(found.machine ? found.machine.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") : "node");
  const [disk, setDisk] = useState("");
  const [dataPart, setDataPart] = useState(false);
  const [started, setStarted] = useState(false);
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
          onProvisioned({ node_id: fp, name, addresses: [addr, ...reported.filter((a) => a !== addr)] });
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
  }, [fp, code, started, name, addr, onProvisioned]);

  const disks = status?.disks.filter((d) => !d.isInstallerMedia) ?? [];
  const chosen = disks.find((d) => d.path === disk);
  const prov = status?.provisioning;

  async function provision() {
    if (!state.fleet || !chosen) return;
    setError("");
    const client = createClient(InstallerService, transportFor({ addr, fingerprint: fp }));
    try {
      await client.provision({
        fleet: {
          fleetId: state.fleet.fleet_id,
          name: state.fleet.name,
          createdUnixMs: BigInt(state.fleet.created_unix_ms),
        },
        nodeName: name.trim(),
        targetDisk: chosen.path,
        dataPartition: dataPart,
        trustedDevices: [
          { name: device.name, publicKeyPem: device.public_key_pem, recoveryOnly: false },
          { name: "Recovery Phrase", publicKeyPem: state.fleet.recovery_public_key_pem, recoveryOnly: true },
        ],
      });
      setStarted(true);
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
          <label>
            Node name{" "}
            <input value={name} onChange={(e) => setName(e.target.value)} style={{ width: 240 }} />
          </label>
          <h4 style={{ marginBottom: 4 }}>Install Maître Server on</h4>
          <p style={{ color: "#b00020", fontSize: 13, marginTop: 0 }}>Everything on the chosen disk will be erased.</p>
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
              {chosen.removable && <span style={{ color: "#555", fontSize: 13 }}> (off by default on USB drives)</span>}
            </label>
          )}
          <button style={{ marginTop: 12 }} disabled={!chosen || !name.trim()} onClick={provision}>
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

function NodeView({ node, onBack }: { node: KnownNode; onBack: () => void }) {
  const [maint, setMaint] = useState<Maintenance | null>(null);
  if (maint)
    return (
      <MaintenanceScreen
        target={{ addr: node.addresses[0], fingerprint: node.node_id }}
        name={node.name}
        m={maint}
        onDone={() => setMaint(null)}
      />
    );
  return <NodeDetails node={node} onBack={onBack} onMaintenance={setMaint} />;
}

function NodeDetails({ node, onBack, onMaintenance }: { node: KnownNode; onBack: () => void; onMaintenance: (m: Maintenance) => void }) {
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
      <h2>{node.name}</h2>
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
          </tbody>
        </table>
      )}
      {metrics && <Gauges m={metrics} />}
      {info && !info.devMode && <ServicesPanel target={{ addr: node.addresses[0], fingerprint: node.node_id }} />}
      {info && !info.devMode && <UpdatePanel target={{ addr: node.addresses[0], fingerprint: node.node_id }} onStarted={onMaintenance} />}
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
