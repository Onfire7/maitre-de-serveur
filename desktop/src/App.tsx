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
import { JoinScreen, RecoverScreen, CodeScreen } from "./join";
import { StoragePanel } from "./storage";
import { useSpaces, defaultSpace } from "./spaces";
import { TailscaleRow } from "./tailscale";
import { nextLetter, randomNodeName } from "./names";
import { Shell as ConsoleShell } from "./app/Shell";
import { RouteProvider, routeFromUrl } from "./app/routes";
import { ThemeProvider } from "./app/theme";

type Screen = { name: "home" } | { name: "phrase"; phrase: string };

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

  if (screen.name === "phrase")
    return (
      <Shell>
        <RecoveryPhrase phrase={screen.phrase} onDone={() => setScreen({ name: "home" })} />
      </Shell>
    );
  return (
    <ThemeProvider>
      <RouteProvider initial={routeFromUrl({ name: "fleet", tab: "overview" })}>
        <ConsoleShell device={device} state={state} save={save} />
      </RouteProvider>
    </ThemeProvider>
  );
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

export function AddNode({
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
  const { nodes: fleetNodes, drives: fleetDrives } = useSpaces(state);
  const letter = nextLetter(fleetNodes.map((n) => n.letter));
  const takenNames = [...state.nodes.map((n) => n.name), ...fleetNodes.map((n) => n.name), ...fleetDrives.map((d) => d.name)];
  const [name, setName] = useState("");
  useEffect(() => {
    if (!name) setName(randomNodeName(letter, takenNames));
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
          <button type="button" title="Draw another name" onClick={() => setName(randomNodeName(letter, takenNames))}>
            🎲
          </button>
          <span style={{ color: "#555", fontSize: 13 }}>
            {" "}
            This will be Node {letter || "?"}; it and its Drives share the {letter || "?"} names.{found.machine ? ` The machine calls itself ${found.machine}.` : ""}
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

export function NodeView({ node, onBack, state, onRenamed }: { node: KnownNode; onBack: () => void; state: ConsoleState; onRenamed: (name: string) => void }) {
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
  const { spaces, nodes: fleetNodes, drives: fleetDrives } = useSpaces(state);
  const myLetter = fleetNodes.find((n) => n.nodeId === node.node_id)?.letter || "";
  const takenNames = [...state.nodes.map((n) => n.name), ...fleetNodes.map((n) => n.name), ...fleetDrives.map((d) => d.name)];
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
          <button type="button" title="Draw another name" onClick={() => setRenaming(randomNodeName(myLetter, takenNames))}>
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
      {info && !info.devMode && <ServicesPanel target={{ addr: node.addresses[0], fingerprint: node.node_id }} spaces={spaces} defaultSpaceId={defaultSpace(spaces)?.spaceId ?? ""} remoteReady={info.tailscale?.state === "Running"} publicIp={info.publicIp} portMapping={info.portMapping} />}
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
