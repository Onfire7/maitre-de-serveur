import { useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";
import type { Found } from "./state";

// Decisions 92 to 95: boot a new machine from the network with nothing but
// the Console. The Rust side downloads the boot bundle and runs the bundled
// `maitre netboot` helper elevated; this is the switch and its status.

interface FilesStatus {
  dir: string;
  image: string;
  ready: boolean;
  have_bytes: number;
  total_bytes: number;
  missing: string[];
}
interface Progress {
  file: string;
  done: number;
  total: number;
  finished: boolean;
  error: string;
}
interface Status {
  running: boolean;
  ip: string;
  interface: string;
  image: string;
  idle_seconds: number;
  clients: string[];
  last_error: string;
  events: { event: string; time: string; mac?: string; type?: string; file?: string; reason?: string }[];
}
interface Depot {
  registry: string;
  images: Record<string, string[]>;
  /** Hostname and address it announced itself from. */
  host: string;
  addr: string;
}

const IDLE_CHOICES: [number, string][] = [
  [15 * 60, "15 minutes"],
  [60 * 60, "1 hour"],
  [4 * 60 * 60, "4 hours"],
  [0, "until I turn it off"],
];

export function NetbootPanel() {
  const [files, setFiles] = useState<FilesStatus | null>(null);
  const [status, setStatus] = useState<Status | null>(null);
  const [depots, setDepots] = useState<Depot[]>([]);
  const [depotAddr, setDepotAddr] = useState("");
  const [progress, setProgress] = useState<Progress | null>(null);
  const [downloading, setDownloading] = useState(false);
  const [idle, setIdle] = useState(3600);
  const [error, setError] = useState("");
  const [open, setOpen] = useState(false);
  // Where the boot files come from. The release is the default; a Depot on
  // the LAN is offered only when one answers, and only takes effect when
  // chosen here, since anything on the network could claim that name.
  const [useDepot, setUseDepot] = useState(false);

  const refresh = async () => {
    setFiles(await invoke<FilesStatus>("netboot_files"));
    setStatus(await invoke<Status>("netboot_status"));
  };
  useEffect(() => {
    refresh();
    const t = setInterval(() => invoke<Status>("netboot_status").then(setStatus), 2000);
    const unlistenP = listen<Progress>("netboot-progress", (e) => setProgress(e.payload));
    const unlistenE = listen("netboot-event", () => invoke<Status>("netboot_status").then(setStatus));
    // Every Depot announcing itself on the network (decision 96: list them
    // all and warn when there is more than one), each asked for its images.
    const ctl = new AbortController();
    const scan = async () => {
      const found = (await invoke<Found[]>("discover")).filter((f) => f.kind === "depot");
      const out: Depot[] = [];
      for (const f of found) {
        const addr = f.addresses[0];
        if (!addr) continue;
        try {
          const tt = setTimeout(() => ctl.abort(), 3000);
          const d = (await (await fetch(`http://${addr}/api/images`, { signal: ctl.signal })).json()) as Depot;
          clearTimeout(tt);
          out.push({ ...d, host: f.name, addr });
        } catch {
          /* not answering; skip */
        }
      }
      setDepots(out);
      setDepotAddr((cur) => (out.some((d) => d.addr === cur) ? cur : ""));
    };
    scan();
    const ts = setInterval(scan, 10000);
    return () => {
      clearInterval(t);
      clearInterval(ts);
      unlistenP.then((f) => f());
      unlistenE.then((f) => f());
      ctl.abort();
    };
  }, []);

  const depot = depots.find((d) => d.addr === depotAddr) ?? null;
  const viaDepot = useDepot && depot !== null;
  const source = viaDepot ? { label: `the Depot on ${depot!.host} (${depot!.registry})`, base: `http://${depot!.registry}/boot/` } : { label: "the latest release", base: "" };
  const depotRef = viaDepot ? Object.entries(depot!.images).flatMap(([n, tags]) => tags.map((t) => `${depot!.registry}/${n}:${t}`))[0] ?? "" : "";
  const image = viaDepot ? depotRef : files?.image || "";
  const insecure = viaDepot ? depot!.registry : "";

  async function download() {
    setDownloading(true);
    setError("");
    try {
      setFiles(await invoke<FilesStatus>("netboot_download", { base: source.base }));
    } catch (e) {
      setError(String(e));
    } finally {
      setDownloading(false);
      setProgress(null);
    }
  }
  async function start() {
    setError("");
    try {
      await invoke("netboot_start", { opts: { image, insecure, idle_seconds: idle, serial: false } });
      await refresh();
    } catch (e) {
      setError(String(e));
    }
  }
  async function stop() {
    await invoke("netboot_stop");
    await refresh();
  }

  const running = status?.running ?? false;
  if (!open && !running)
    return (
      <p>
        <button onClick={() => setOpen(true)}>Boot a new machine from the network…</button>
        <span style={{ color: "#555", fontSize: 13, marginLeft: 8 }}>No USB stick needed: the machine boots the Installer from this computer.</span>
      </p>
    );

  return (
    <section style={{ border: "1px solid #ddd", borderRadius: 8, padding: 12, margin: "8px 0" }}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
        <b>Boot a new machine from the network</b>
        {!running && <button onClick={() => setOpen(false)}>Close</button>}
      </div>
      {error && <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>{error}</pre>}
      {status?.last_error && !running && <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>{status.last_error}</pre>}

      {depots.length > 1 && (
        <p style={{ color: "#a60", fontSize: 13 }}>
          More than one Depot is announcing itself on this network: {depots.map((d) => `${d.host} (${d.registry})`).join(", ")}. Only use one you know is yours; anything on the network can call itself a Depot.
        </p>
      )}
      {depots.length > 0 && (
        <div style={{ color: "#555", fontSize: 13 }}>
          <label>
            <input type="checkbox" checked={useDepot} onChange={(e) => setUseDepot(e.target.checked)} /> Use a developer Depot over plain HTTP instead of the release:{" "}
          </label>
          <select value={depotAddr} onChange={(e) => setDepotAddr(e.target.value)} disabled={!useDepot}>
            <option value="">choose one…</option>
            {depots.map((d) => (
              <option key={d.addr} value={d.addr}>
                {d.host} · {d.registry}
              </option>
            ))}
          </select>
        </div>
      )}
      {files && !files.ready && (
        <div>
          <p>
            The boot files ({formatGB(files.total_bytes || 1.3e9)}) are downloaded once from {source.label} and kept on this computer.
          </p>
          {progress && !progress.finished && (
            <div>
              <progress value={progress.done} max={progress.total} style={{ width: "100%" }} />
              <div style={{ color: "#555", fontSize: 13 }}>
                {progress.file}: {formatGB(progress.done)} of {formatGB(progress.total)}
              </div>
            </div>
          )}
          <button disabled={downloading} onClick={download}>
            {downloading ? "Downloading…" : "Download"}
          </button>
        </div>
      )}

      {files?.ready && !running && (
        <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
          <div style={{ color: "#555", fontSize: 13 }}>
            Boot files from {source.label}. The machine will install <code>{image || "(no image known)"}</code>.
          </div>
          <label>
            Turn off after{" "}
            <select value={idle} onChange={(e) => setIdle(Number(e.target.value))}>
              {IDLE_CHOICES.map(([s, l]) => (
                <option key={s} value={s}>
                  {l}
                </option>
              ))}
            </select>{" "}
            <span style={{ color: "#555", fontSize: 13 }}>without a machine booting</span>
          </label>
          <div>
            <button disabled={!image} onClick={start}>
              Turn on
            </button>
            <span style={{ color: "#555", fontSize: 13, marginLeft: 8 }}>Asks for your password: answering network boot requests needs administrator rights.</span>
          </div>
          <p style={{ color: "#555", fontSize: 13, margin: 0 }}>
            Then turn the new machine on and pick network boot (UEFI). It appears above as an Installer once it is up, usually within two minutes.
          </p>
        </div>
      )}

      {running && status && (
        <div>
          <p>
            <span style={{ color: "#2a7" }}>●</span> Answering network boot requests on {status.interface || "the network"} ({status.ip}).{" "}
            {status.idle_seconds > 0 ? `Turns off after ${describeIdle(status.idle_seconds)} without a machine booting.` : ""}
          </p>
          <p style={{ color: "#555", fontSize: 13 }}>
            {status.clients.length === 0 ? "No machine has asked yet. Turn one on and choose network boot." : `Machines that booted from here: ${status.clients.join(", ")}`}
          </p>
          <details>
            <summary style={{ color: "#555", fontSize: 13 }}>Activity</summary>
            <pre style={{ maxHeight: 160, overflow: "auto", background: "#111", color: "#ddd", padding: 8, fontSize: 12 }}>
              {status.events.map((e) => `${e.time.slice(11, 19)}  ${e.event}${e.mac ? ` ${e.mac} ${e.type ?? ""}` : ""}${e.file ? ` ${e.file}` : ""}${e.reason ? ` (${e.reason})` : ""}`).join("\n")}
            </pre>
          </details>
          <button onClick={stop}>Turn off</button>
        </div>
      )}
    </section>
  );
}

function formatGB(n: number) {
  return n >= 1e9 ? `${(n / 1e9).toFixed(1)} GB` : `${(n / 1e6).toFixed(0)} MB`;
}
function describeIdle(s: number) {
  return IDLE_CHOICES.find(([v]) => v === s)?.[1] ?? `${Math.round(s / 60)} minutes`;
}
