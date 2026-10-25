import { useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";

// Milestone 7: "Prepare an installer USB" from the Console. The image is
// downloaded once; writing it runs the bundled helper elevated. The disk's
// model must be typed back (the guard), since everything on it is erased.

interface ImageStatus {
  path: string;
  ready: boolean;
  size_bytes: number;
}
interface Disk {
  device: string;
  model: string;
  size_bytes: number;
  removable: boolean;
  transport: string;
  in_use: boolean;
}
interface WriteStatus {
  running: boolean;
  done: number;
  total: number;
  finished: boolean;
  error: string;
}

export function MediaPanel() {
  const [open, setOpen] = useState(false);
  const [image, setImage] = useState<ImageStatus | null>(null);
  const [disks, setDisks] = useState<Disk[]>([]);
  const [chosen, setChosen] = useState<Disk | null>(null);
  const [typed, setTyped] = useState("");
  const [progress, setProgress] = useState<WriteStatus | null>(null);
  const [downloading, setDownloading] = useState(false);
  const [error, setError] = useState("");
  const refresh = async () => {
    setImage(await invoke<ImageStatus>("media_image"));
    try {
      setDisks((await invoke<Disk[]>("media_disks")).filter((d) => d.removable));
    } catch (e) {
      setError(String(e));
    }
  };
  useEffect(() => {
    if (!open) return;
    refresh();
    const t = setInterval(refresh, 4000);
    const un = listen<WriteStatus>("media-progress", (e) => setProgress(e.payload));
    return () => {
      clearInterval(t);
      un.then((f) => f());
    };
  }, [open]);

  if (!open)
    return (
      <p>
        <button onClick={() => setOpen(true)}>Prepare an installer USB…</button>
        <span style={{ color: "#555", fontSize: 13, marginLeft: 8 }}>For machines that cannot boot from the network.</span>
      </p>
    );
  const writing = progress?.running ?? false;
  return (
    <section style={{ border: "1px solid #ddd", borderRadius: 8, padding: 12, margin: "8px 0" }}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
        <b>Prepare an installer USB</b>
        {!writing && <button onClick={() => setOpen(false)}>Close</button>}
      </div>
      {error && <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>{error}</pre>}
      {image && !image.ready && (
        <p>
          The installer image is downloaded once from the latest release.{" "}
          <button
            disabled={downloading}
            onClick={async () => {
              setDownloading(true);
              setError("");
              try {
                setImage(await invoke<ImageStatus>("media_download", { url: "" }));
              } catch (e) {
                setError(String(e));
              } finally {
                setDownloading(false);
                setProgress(null);
              }
            }}
          >
            {downloading ? "Downloading…" : "Download"}
          </button>
          {downloading && progress && progress.total > 0 && <progress value={progress.done} max={progress.total} style={{ width: "100%" }} />}
        </p>
      )}
      {image?.ready && !writing && !progress?.finished && (
        <div>
          <p>Plug in a USB stick of 8 GB or more. Everything on it will be erased.</p>
          {disks.length === 0 ? (
            <p style={{ color: "#555" }}>No removable disk found yet.</p>
          ) : (
            disks.map((d) => (
              <label key={d.device} style={{ display: "block" }}>
                <input type="radio" name="usb" checked={chosen?.device === d.device} onChange={() => setChosen(d)} /> <b>{d.model || d.device}</b> · {Math.round(d.size_bytes / 1e9)} GB · {d.device}
                {d.in_use && <span style={{ color: "#a60" }}> · has mounted files</span>}
              </label>
            ))
          )}
          {chosen && (
            <p>
              Type <code>{chosen.model || chosen.device}</code> to confirm: <input value={typed} onChange={(e) => setTyped(e.target.value)} style={{ width: 240 }} />{" "}
              <button
                disabled={typed.trim() !== (chosen.model || chosen.device)}
                onClick={async () => {
                  setError("");
                  try {
                    await invoke("media_write", { device: chosen.device });
                    setProgress({ running: true, done: 0, total: 0, finished: false, error: "" });
                  } catch (e) {
                    setError(String(e));
                  }
                }}
              >
                Erase and write
              </button>
              <span style={{ color: "#555", fontSize: 13 }}> Asks for your password.</span>
            </p>
          )}
        </div>
      )}
      {writing && progress && (
        <div>
          <progress value={progress.total > 0 ? progress.done : undefined} max={progress.total > 0 ? progress.total : undefined} style={{ width: "100%" }} />
          <div style={{ color: "#555", fontSize: 13 }}>Writing… {Math.round(progress.done / 1e6)} MB. Do not unplug the stick.</div>
        </div>
      )}
      {progress?.finished && (
        <p style={{ color: "#2a7" }}>
          Done. The stick is safe to unplug. Boot the new machine from it and it appears above as an Installer.{" "}
          <button onClick={() => setProgress(null)}>Prepare another</button>
        </p>
      )}
      {progress?.error && !writing && <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>{progress.error}</pre>}
    </section>
  );
}
