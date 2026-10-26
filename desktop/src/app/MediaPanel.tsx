// Milestone 7: prepare an installer USB from the Console. The image is
// downloaded once; writing runs the bundled helper elevated. The disk's
// model must be typed back (the guard), since everything on it is erased.
import { useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";
import { Usb } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { cn } from "@/lib/utils";

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
  const [image, setImage] = useState<ImageStatus | null>(null);
  const [disks, setDisks] = useState<Disk[]>([]);
  const [chosen, setChosen] = useState<Disk | null>(null);
  const [typed, setTyped] = useState("");
  const [progress, setProgress] = useState<WriteStatus | null>(null);
  const [downloading, setDownloading] = useState(false);
  const [error, setError] = useState("");
  const refresh = async () => {
    try {
      setImage(await invoke<ImageStatus>("media_image"));
      setDisks((await invoke<Disk[]>("media_disks")).filter((d) => d.removable));
    } catch (e) {
      setError(String((e as Error).message ?? e));
    }
  };
  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 4000);
    const un = listen<WriteStatus>("media-progress", (e) => setProgress(e.payload));
    return () => {
      clearInterval(t);
      un.then((f) => f()).catch(() => {});
    };
  }, []);
  const writing = progress?.running ?? false;
  const expected = chosen ? chosen.model || chosen.device : "";
  return (
    <div className="space-y-3 text-sm">
      <div>
        <div className="font-medium">Prepare an installer USB</div>
        <div className="text-xs text-muted-foreground">For machines that cannot boot from the network.</div>
      </div>
      {error && <p className="text-xs text-destructive">{error}</p>}
      {image && !image.ready && (
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            disabled={downloading}
            onClick={async () => {
              setDownloading(true);
              setError("");
              try {
                setImage(await invoke<ImageStatus>("media_download", { url: "" }));
              } catch (e) {
                setError(String((e as Error).message ?? e));
              } finally {
                setDownloading(false);
                setProgress(null);
              }
            }}
          >
            {downloading ? "Downloading…" : "Download the installer image"}
          </Button>
          <span className="text-xs text-muted-foreground">Once, from the latest release.</span>
        </div>
      )}
      {image?.ready && !writing && !progress?.finished && (
        <div className="space-y-2">
          <p className="text-xs text-muted-foreground">Plug in a USB stick of 8 GB or more. Everything on it will be erased.</p>
          {disks.length === 0 ? (
            <p className="text-xs text-muted-foreground">No removable disk found yet.</p>
          ) : (
            <RadioGroup value={chosen?.device ?? ""} onValueChange={(v) => (setChosen(disks.find((d) => d.device === v) ?? null), setTyped(""))} className="gap-1.5">
              {disks.map((d) => (
                <label key={d.device} className={cn("flex cursor-pointer items-center gap-3 rounded-lg border p-2.5", chosen?.device === d.device && "border-primary")}>
                  <RadioGroupItem value={d.device} />
                  <Usb className="size-4 text-muted-foreground" />
                  <span className="min-w-0 flex-1">
                    <span className="block font-medium">{d.model || d.device}</span>
                    <span className="block text-xs text-muted-foreground">
                      {Math.round(d.size_bytes / 1e9)} GB · {d.device}
                      {d.in_use && <span className="text-status-attention"> · has mounted files</span>}
                    </span>
                  </span>
                </label>
              ))}
            </RadioGroup>
          )}
          {chosen && (
            <div className="space-y-1.5">
              <Label>
                Type <code className="rounded bg-muted px-1">{expected}</code> to confirm
              </Label>
              <div className="flex gap-2">
                <Input value={typed} onChange={(e) => setTyped(e.target.value)} className="max-w-xs" />
                <Button
                  size="sm"
                  variant="destructive"
                  disabled={typed.trim() !== expected}
                  onClick={async () => {
                    setError("");
                    try {
                      await invoke("media_write", { device: chosen.device });
                      setProgress({ running: true, done: 0, total: 0, finished: false, error: "" });
                    } catch (e) {
                      setError(String((e as Error).message ?? e));
                    }
                  }}
                >
                  Erase and write
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">Asks for your password.</p>
            </div>
          )}
        </div>
      )}
      {writing && progress && (
        <div className="space-y-1">
          <div className="h-1.5 overflow-hidden rounded bg-muted">
            <div className={cn("h-1.5 rounded bg-status-starting", progress.total === 0 && "w-1/3 animate-pulse")} style={progress.total > 0 ? { width: `${(progress.done / progress.total) * 100}%` } : undefined} />
          </div>
          <p className="text-xs text-muted-foreground">Writing… {Math.round(progress.done / 1e6)} MB. Do not unplug the stick.</p>
        </div>
      )}
      {progress?.finished && !progress.error && (
        <p className="flex items-center gap-2 text-xs">
          <span className="size-2 rounded-full bg-status-online" /> Done. The stick is safe to unplug. Boot the new machine from it and it appears above as an Installer.
          <Button size="sm" variant="ghost" onClick={() => setProgress(null)}>
            Prepare another
          </Button>
        </p>
      )}
      {progress?.error && !writing && <p className="text-xs text-destructive">{progress.error}</p>}
    </div>
  );
}
