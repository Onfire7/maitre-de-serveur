// Decisions 92 to 96: boot a new machine from the network with nothing but
// the Console. The Rust side downloads the boot bundle and runs the bundled
// `maitre netboot` helper elevated; this is the switch and its status.
import { useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import type { Found } from "@/state";

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
  images: Record<string, { tag: string; version?: string }[]>;
  host: string;
  addr: string;
}

const IDLE_CHOICES: [number, string][] = [
  [15 * 60, "15 minutes"],
  [60 * 60, "1 hour"],
  [4 * 60 * 60, "4 hours"],
  [0, "until I turn it off"],
];
const gb = (n: number) => (n >= 1e9 ? `${(n / 1e9).toFixed(1)} GB` : `${(n / 1e6).toFixed(0)} MB`);

export function NetbootPanel() {
  const [files, setFiles] = useState<FilesStatus | null>(null);
  const [status, setStatus] = useState<Status | null>(null);
  const [depots, setDepots] = useState<Depot[]>([]);
  const [depotAddr, setDepotAddr] = useState("");
  const [progress, setProgress] = useState<Progress | null>(null);
  const [downloading, setDownloading] = useState(false);
  const [idle, setIdle] = useState(3600);
  const [error, setError] = useState("");
  const [useDepot, setUseDepot] = useState(false);
  const refresh = async () => {
    try {
      setFiles(await invoke<FilesStatus>("netboot_files"));
      setStatus(await invoke<Status>("netboot_status"));
    } catch (e) {
      setError(String((e as Error).message ?? e));
    }
  };
  useEffect(() => {
    refresh();
    const t = setInterval(() => invoke<Status>("netboot_status").then(setStatus).catch(() => {}), 2000);
    const unlistenP = listen<Progress>("netboot-progress", (e) => setProgress(e.payload));
    const unlistenE = listen("netboot-event", () => invoke<Status>("netboot_status").then(setStatus).catch(() => {}));
    const scan = async () => {
      const found = (await invoke<Found[]>("discover").catch(() => [] as Found[])).filter((f) => f.kind === "depot");
      const out: Depot[] = [];
      for (const f of found) {
        const addr = f.addresses[0];
        if (!addr) continue;
        try {
          const d = (await (await fetch(`http://${addr}/api/images`, { signal: AbortSignal.timeout(3000) })).json()) as Depot;
          out.push({ ...d, host: f.name, addr });
        } catch {
          /* not answering */
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
      unlistenP.then((f) => f()).catch(() => {});
      unlistenE.then((f) => f()).catch(() => {});
    };
  }, []);
  const depot = depots.find((d) => d.addr === depotAddr) ?? null;
  const viaDepot = useDepot && depot !== null;
  const source = viaDepot ? { label: `the Depot on ${depot!.host} (${depot!.registry})`, base: `http://${depot!.registry}/boot/` } : { label: "the latest release", base: "" };
  const depotRef = viaDepot ? (Object.entries(depot!.images).flatMap(([n, tags]) => tags.map((t) => `${depot!.registry}/${n}:${t.tag}`))[0] ?? "") : "";
  const depotVersion = viaDepot ? (Object.values(depot!.images).flat()[0]?.version ?? "") : "";
  const image = viaDepot ? depotRef : files?.image || "";
  const insecure = viaDepot ? depot!.registry : "";
  async function download() {
    setDownloading(true);
    setError("");
    try {
      setFiles(await invoke<FilesStatus>("netboot_download", { base: source.base }));
    } catch (e) {
      setError(String((e as Error).message ?? e));
    } finally {
      setDownloading(false);
      setProgress(null);
    }
  }
  const running = status?.running ?? false;
  return (
    <div className="space-y-3 text-sm">
      <div>
        <div className="font-medium">Boot a new machine from the network</div>
        <div className="text-xs text-muted-foreground">No USB stick needed: the machine boots the Installer from this computer.</div>
      </div>
      {error && <p className="text-xs text-destructive">{error}</p>}
      {status?.last_error && !running && <p className="text-xs text-destructive">{status.last_error}</p>}
      {depots.length > 1 && <p className="text-xs text-status-attention">More than one Depot is announcing itself on this network: {depots.map((d) => `${d.host} (${d.registry})`).join(", ")}. Only use one you know is yours.</p>}
      {depots.length > 0 && !running && (
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <label className="flex items-center gap-2">
            <Switch checked={useDepot} onCheckedChange={setUseDepot} /> Use a developer Depot over plain HTTP
          </label>
          <Select value={depotAddr || "none"} onValueChange={(v) => setDepotAddr(v === "none" ? "" : v)} disabled={!useDepot}>
            <SelectTrigger size="sm" className="w-56">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="none">choose one…</SelectItem>
              {depots.map((d) => (
                <SelectItem key={d.addr} value={d.addr}>
                  {d.host} · {d.registry}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      )}
      {files && !files.ready && !running && (
        <div className="space-y-2">
          <p className="text-xs text-muted-foreground">The boot files ({gb(files.total_bytes || 1.3e9)}) are downloaded once from {source.label} and kept on this computer.</p>
          {progress && !progress.finished && (
            <div>
              <div className="h-1.5 rounded bg-muted">
                <div className="h-1.5 rounded bg-status-starting" style={{ width: `${progress.total ? (progress.done / progress.total) * 100 : 0}%` }} />
              </div>
              <div className="text-xs text-muted-foreground">
                {progress.file}: {gb(progress.done)} of {gb(progress.total)}
              </div>
            </div>
          )}
          <Button size="sm" disabled={downloading} onClick={download}>
            {downloading ? "Downloading…" : "Download"}
          </Button>
        </div>
      )}
      {files?.ready && !running && (
        <div className="space-y-2">
          <p className="text-xs text-muted-foreground">
            Boot files from {source.label}. The machine will install <code>{image || "(no image known)"}</code>
            {depotVersion && ` (version ${depotVersion})`}.
          </p>
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <span>Turn off after</span>
            <Select value={String(idle)} onValueChange={(v) => setIdle(Number(v))}>
              <SelectTrigger size="sm" className="w-44">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {IDLE_CHOICES.map(([s, l]) => (
                  <SelectItem key={s} value={String(s)}>
                    {l}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <span className="text-muted-foreground">without a machine booting</span>
          </div>
          <div className="flex items-center gap-2">
            <Button size="sm" disabled={!image} onClick={() => invoke("netboot_start", { opts: { image, insecure, idle_seconds: idle, serial: false } }).then(refresh).catch((e) => setError(String((e as Error).message ?? e)))}>
              Turn on
            </Button>
            <span className="text-xs text-muted-foreground">Asks for your password: answering network boot requests needs administrator rights.</span>
          </div>
          <p className="text-xs text-muted-foreground">Then turn the new machine on and pick network boot (UEFI). It appears above as an Installer once it is up, usually within two minutes.</p>
        </div>
      )}
      {running && status && (
        <div className="space-y-2">
          <p className="flex items-center gap-2">
            <span className="size-2 rounded-full bg-status-online" /> Answering network boot requests on {status.interface || "the network"} ({status.ip}). {status.idle_seconds > 0 ? `Turns off after ${IDLE_CHOICES.find(([v]) => v === status.idle_seconds)?.[1] ?? `${Math.round(status.idle_seconds / 60)} minutes`} without a machine booting.` : ""}
          </p>
          <p className="text-xs text-muted-foreground">{status.clients.length === 0 ? "No machine has asked yet. Turn one on and choose network boot." : `Machines that booted from here: ${status.clients.join(", ")}`}</p>
          <details className="text-xs">
            <summary className="cursor-pointer text-muted-foreground">Activity</summary>
            <pre className="mt-1 max-h-40 overflow-auto rounded-md bg-muted p-2 font-mono text-[11px]">{status.events.map((e) => `${e.time.slice(11, 19)}  ${e.event}${e.mac ? ` ${e.mac} ${e.type ?? ""}` : ""}${e.file ? ` ${e.file}` : ""}${e.reason ? ` (${e.reason})` : ""}`).join("\n")}</pre>
          </details>
          <Button size="sm" variant="outline" onClick={() => invoke("netboot_stop").then(refresh)}>
            Turn off
          </Button>
        </div>
      )}
    </div>
  );
}
