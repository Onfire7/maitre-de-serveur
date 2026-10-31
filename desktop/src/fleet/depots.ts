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
export function depotImage(d: Depot): { ref: string; version: string; boot: string; insecure: string } | null {
  const all = Object.entries(d.images).flatMap(([name, tags]) => tags.map((t) => ({ ...t, name })));
  if (all.length === 0) return null;
  const newest = all.reduce((a, b) => ((b.created ?? "") > (a.created ?? "") ? b : a));
  return { ref: `${d.registry}/${newest.name}:${newest.tag}`, version: newest.version ?? "", boot: `http://${d.registry}/boot/`, insecure: d.registry };
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

/**
 * The boot files a Depot serves now, by name, from its netboot.json. Read
 * by the Rust side: the webview cannot, as a Depot from before decision 263
 * sends no CORS header for /boot/.
 */
export async function depotBootDigests(base: string): Promise<Record<string, string> | null> {
  try {
    return await invoke<Record<string, string>>("netboot_depot_digests", { base });
  } catch {
    return null;
  }
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
