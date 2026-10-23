import { useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { createClient } from "@connectrpc/connect";
import { NodeService, type NodeInfo } from "./gen/maitre/v1/node_pb";
import { transport } from "./transport";

interface DeviceInfo {
  fingerprint: string;
  public_key_pem: string;
  public_key_path: string;
}
interface Settings {
  node_addr: string;
  node_fingerprint: string;
}

const node = createClient(NodeService, transport);

export default function App() {
  const [device, setDevice] = useState<DeviceInfo | null>(null);
  const [settings, setSettings] = useState<Settings>({ node_addr: "127.0.0.1:7443", node_fingerprint: "" });
  const [info, setInfo] = useState<NodeInfo | null>(null);
  const [error, setError] = useState<string>("");

  async function connect(s: Settings) {
    setError("");
    setInfo(null);
    try {
      await invoke("set_settings", { settings: s });
      const res = await node.getNodeInfo({});
      setInfo(res.node ?? null);
    } catch (e) {
      setError(String(e));
    }
  }

  useEffect(() => {
    invoke<DeviceInfo>("device_info").then(setDevice);
    invoke<Settings>("get_settings").then((s) => {
      if (s.node_addr) setSettings(s);
      // Saved settings mean the user already paired this device by hand:
      // connect straight away rather than making them click again.
      if (s.node_addr && s.node_fingerprint) void connect(s);
    });
  }, []);

  useEffect(() => {
    if (!info) return;
    const t = setInterval(async () => {
      try {
        const res = await node.getNodeInfo({});
        setInfo(res.node ?? null);
      } catch (e) {
        setError(String(e));
      }
    }, 2000);
    return () => clearInterval(t);
  }, [info !== null]);

  return (
    <main style={{ fontFamily: "system-ui", padding: 24, maxWidth: 720 }}>
      <h1>Maître Desktop</h1>
      <p style={{ color: "#666" }}>Milestone 0: this device talks to a dev Node over mutual TLS.</p>

      <section>
        <h2>This device</h2>
        {device ? (
          <>
            <div>
              Fingerprint: <code>{device.fingerprint}</code>
            </div>
            <div>
              Public key file: <code>{device.public_key_path}</code>
            </div>
            <p style={{ color: "#666", fontSize: 13 }}>
              Copy that file into the dev Node's <code>trusted/</code> directory so the Node accepts this device.
            </p>
          </>
        ) : (
          "loading…"
        )}
      </section>

      <section>
        <h2>Dev Node</h2>
        <label>
          Address{" "}
          <input value={settings.node_addr} onChange={(e) => setSettings({ ...settings, node_addr: e.target.value })} style={{ width: 240 }} />
        </label>
        <br />
        <label>
          Node fingerprint{" "}
          <input
            value={settings.node_fingerprint}
            onChange={(e) => setSettings({ ...settings, node_fingerprint: e.target.value })}
            style={{ width: 560, fontFamily: "monospace" }}
            placeholder="hex fingerprint printed by `maitre dev`"
          />
        </label>
        <br />
        <button onClick={() => connect(settings)} style={{ marginTop: 8 }}>
          Connect
        </button>
      </section>

      {error && (
        <pre style={{ color: "#b00020", whiteSpace: "pre-wrap" }}>{error}</pre>
      )}

      {info && (
        <section>
          <h2>Node</h2>
          <table>
            <tbody>
              <tr><td>Name</td><td>{info.name}</td></tr>
              <tr><td>Node ID</td><td><code>{info.nodeId}</code></td></tr>
              <tr><td>Fleet</td><td>{info.fleetId || "(none)"}</td></tr>
              <tr><td>Version</td><td>{info.version}</td></tr>
              <tr><td>Uptime</td><td>{String(info.uptimeSeconds)}s</td></tr>
              <tr><td>Dev mode</td><td>{info.devMode ? "yes" : "no"}</td></tr>
            </tbody>
          </table>
        </section>
      )}
    </main>
  );
}
