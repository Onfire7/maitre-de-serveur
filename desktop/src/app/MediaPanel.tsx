// Milestone 7: prepare an installer USB from the Console. The image is
// downloaded once; writing runs the bundled helper elevated. The disk's
// model must be typed back (the guard), since everything on it is erased.
// With a Depot on the network the stick can carry its build instead: the
// Depot's netboot files, made into a stick that installs its newest image
// (decision 221).
import { useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";
import { Usb } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { cn } from "@/lib/utils";
import { depotImage, useDepots } from "@/fleet/depots";

const RELEASE = "release";

interface ImageStatus {
  path: string;
  ready: boolean;
  size_bytes: number;
  installs: string;
}
interface BootProgress {
  file: string;
  done: number;
  total: number;
  finished: boolean;
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
  const depots = useDepots();
  // The one Depot on the network is offered first; with several, the user picks.
  const [picked, setPicked] = useState<string | null>(null);
  const sourceKey = picked ?? (depots.length === 1 ? depots[0].addr : RELEASE);
  const depot = depots.find((d) => d.addr === sourceKey) ?? null;
  const offered = depot ? depotImage(depot) : null;
  const viaDepot = !!depot && !!offered;
  const [boot, setBoot] = useState<BootProgress | null>(null);
  const refresh = async () => {
    try {
      setImage(await invoke<ImageStatus>("media_image", { source: viaDepot ? "depot" : RELEASE }));
      setDisks((await invoke<Disk[]>("media_disks")).filter((d) => d.removable));
    } catch (e) {
      setError(String((e as Error).message ?? e));
    }
  };
  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 4000);
    return () => clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [viaDepot]);
  useEffect(() => {
    const un = listen<WriteStatus>("media-progress", (e) => setProgress(e.payload));
    const unBoot = listen<BootProgress>("netboot-progress", (e) => setBoot(e.payload));
    return () => {
      un.then((f) => f()).catch(() => {});
      unBoot.then((f) => f()).catch(() => {});
    };
  }, []);
  // A Depot stick is ready when it installs what the Depot offers now.
  const ready = !!image?.ready && (!viaDepot || image.installs === offered!.ref);
  async function fromDepot() {
    setDownloading(true);
    setError("");
    try {
      await invoke("netboot_download", { base: offered!.boot });
      setBoot(null);
      setImage(await invoke<ImageStatus>("media_build", { image: offered!.ref, insecure: offered!.insecure }));
    } catch (e) {
      setError(String((e as Error).message ?? e));
    } finally {
      setDownloading(false);
      setBoot(null);
    }
  }
  const writing = progress?.running ?? false;
  const expected = chosen ? chosen.model || chosen.device : "";
  return (
    <div className="space-y-3 text-sm">
      <div>
        <div className="font-medium">Prepare an installer USB</div>
        <div className="text-xs text-muted-foreground">For machines that cannot boot from the network.</div>
      </div>
      {error && <p className="text-xs text-destructive">{error}</p>}
      {depots.length > 0 && !writing && (
        <div className="space-y-1.5">
          <RadioGroup value={sourceKey} onValueChange={(v) => (setPicked(v), setChosen(null), setTyped(""))} className="gap-1.5" aria-label="What the stick installs">
            {depots.map((d) => {
              const o = depotImage(d);
              return (
                <label key={d.addr} className={cn("flex cursor-pointer items-start gap-3 rounded-lg border p-2.5", sourceKey === d.addr && "border-primary", !o && "cursor-not-allowed opacity-60")}>
                  <RadioGroupItem value={d.addr} disabled={!o} className="mt-0.5" />
                  <span className="min-w-0 flex-1">
                    <span className="block font-medium">From the Depot on {d.host}</span>
                    <span className="block text-xs text-muted-foreground">{o ? <>Your own build, <code>{o.ref}</code>{o.version && ` (version ${o.version})`}, over plain HTTP on this network. No release needed.</> : "It has no image to install yet."}</span>
                  </span>
                </label>
              );
            })}
            <label className={cn("flex cursor-pointer items-start gap-3 rounded-lg border p-2.5", sourceKey === RELEASE && "border-primary")}>
              <RadioGroupItem value={RELEASE} className="mt-0.5" />
              <span className="min-w-0 flex-1">
                <span className="block font-medium">The latest release</span>
                <span className="block text-xs text-muted-foreground">Downloaded once from GitHub.</span>
              </span>
            </label>
          </RadioGroup>
          {depots.length > 1 && <p className="text-xs text-status-attention">More than one Depot is announcing itself on this network. Only use one you know is yours.</p>}
        </div>
      )}
      {viaDepot && image && !ready && !writing && (
        <div className="space-y-2">
          {downloading && boot && !boot.finished && (
            <div>
              <div className="h-1.5 rounded bg-muted">
                <div className="h-1.5 rounded bg-status-starting" style={{ width: `${boot.total ? (boot.done / boot.total) * 100 : 0}%` }} />
              </div>
              <div className="text-xs text-muted-foreground">
                {boot.file}: {Math.round(boot.done / 1e6)} of {Math.round(boot.total / 1e6)} MB
              </div>
            </div>
          )}
          <div className="flex items-center gap-2">
            <Button size="sm" disabled={downloading} onClick={fromDepot}>
              {downloading ? "Getting it…" : image.installs ? "Update from the Depot" : "Get the Installer from the Depot"}
            </Button>
            <span className="text-xs text-muted-foreground">{image.installs ? <>The stick image on this computer installs <code>{image.installs}</code>.</> : "About 1.2 GB, once; what network boot already fetched is reused."}</span>
          </div>
        </div>
      )}
      {!viaDepot && image && !image.ready && (
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
      {ready && !writing && !progress?.finished && (
        <div className="space-y-2">
          <p className="text-xs text-muted-foreground">
            {viaDepot ? <>The stick installs <code>{offered!.ref}</code> from the Depot on {depot!.host}. </> : ""}Plug in a USB stick of {viaDepot ? "2" : "8"} GB or more. Everything on it will be erased.
          </p>
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
                      await invoke("media_write", { device: chosen.device, source: viaDepot ? "depot" : RELEASE });
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
