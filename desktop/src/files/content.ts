// File contents for previews (decision 183): a URL the web view can load
// directly, through the Console's loopback proxy for that Node. The demo
// makes the contents up in the browser instead.
import { invoke } from "@tauri-apps/api/core";
import { isDemo } from "@/demo/flag";
import type { Target } from "@/transport";

export interface FileRef {
  target: Target;
  nodeId: string;
  spaceId?: string;
  driveId?: string;
  path: string;
}

const bases = new Map<string, Promise<string>>();

/** A URL for the file's contents; "" when there is none (demo media). */
export async function contentUrl(f: FileRef): Promise<string> {
  if (isDemo()) {
    const { demoContentUrl } = await import("@/demo/files");
    return demoContentUrl(f);
  }
  const key = `${f.target.fingerprint}@${f.target.addr}`;
  let base = bases.get(key);
  if (!base) {
    base = invoke<string>("files_url", { addr: f.target.addr, fingerprint: f.target.fingerprint });
    base.catch(() => bases.delete(key));
    bases.set(key, base);
  }
  const q = new URLSearchParams({ space: f.spaceId ?? "", drive: f.driveId ?? "", path: f.path });
  return `${await base}/maitre/files/content?${q}`;
}
