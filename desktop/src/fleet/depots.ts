// Developer Depots on the LAN (decisions 81, 91): their images, their netboot
// files, and now installer sticks built from those files (221).
import { useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import type { Found } from "@/state";

export interface Depot {
  registry: string;
  images: Record<string, { tag: string; version?: string; created?: string }[]>;
  host: string;
  addr: string;
}

/** What a Depot offers to install: its newest image, and where its boot files are. */
export function depotImage(d: Depot): { ref: string; version: string; created: string; boot: string; insecure: string } | null {
  const all = Object.entries(d.images).flatMap(([name, tags]) => tags.map((t) => ({ ...t, name })));
  if (all.length === 0) return null;
  const newest = all.reduce((a, b) => ((b.created ?? "") > (a.created ?? "") ? b : a));
  return { ref: `${d.registry}/${newest.name}:${newest.tag}`, version: newest.version ?? "", created: newest.created ?? "", boot: `http://${d.registry}/boot/`, insecure: d.registry };
}

async function scanDepots(): Promise<Depot[]> {
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
  return out;
}

/** Depots announcing themselves on this network, checked every 10 s. */
export function useDepots(): Depot[] {
  const [depots, setDepots] = useState<Depot[]>([]);
  useEffect(() => {
    let stop = false;
    const scan = () => scanDepots().then((d) => !stop && setDepots(d));
    scan();
    const t = setInterval(scan, 10000);
    return () => {
      stop = true;
      clearInterval(t);
    };
  }, []);
  return depots;
}

/** A Depot's boot files now: digests by name (decision 263), and the version
 * and build time of the Installer they boot (267; "" from older Depots). */
export interface DepotBoot {
  digests: Record<string, string>;
  version: string;
  built: string;
}

/**
 * The boot files a Depot serves now, from its netboot.json. Read by the
 * Rust side: the webview cannot, as a Depot from before decision 263 sends
 * no CORS header for /boot/.
 */
export async function depotBoot(base: string): Promise<DepotBoot | null> {
  try {
    return await invoke<DepotBoot>("netboot_depot_boot", { base });
  } catch {
    return null;
  }
}

/** A build's version and time in words: "778bc2d, built 31 Oct 14:26"; with
 * no version, just "built 31 Oct 14:26"; with neither, "unknown version". */
export function buildWords(version: string, at: string, verb = "built"): string {
  const d = new Date(at);
  const when = at && !Number.isNaN(d.getTime()) ? `${verb} ${d.toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })}` : "";
  if (!version) return when || "unknown version";
  return when ? `${version}, ${when}` : version;
}

/**
 * The boot files on this computer that the Depot has since rebuilt
 * (decision 263): names whose digest differs. Files the Depot no longer
 * lists are not counted, and nothing is when either side is unknown.
 */
export function bootFilesBehind(local: Record<string, string> | undefined, depot: Record<string, string> | null): string[] {
  if (!local || !depot) return [];
  return Object.entries(local)
    .filter(([n, sha]) => depot[n] && depot[n] !== sha)
    .map(([n]) => n);
}
