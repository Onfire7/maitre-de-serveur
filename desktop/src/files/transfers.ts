// Upload and Download (decisions 180, 185): the Tauri side picks and
// streams the files; here the pickers, a progress toast, and the demo's
// browser-only versions.
import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";
import { toast } from "sonner";
import { isDemo } from "@/demo/flag";
import { formatBytes } from "@/lib/format";
import type { Target } from "@/transport";
import { contentUrl } from "./content";

export interface Place {
  target: Target;
  nodeId: string;
  spaceId?: string;
  driveId?: string;
}

interface UploadResult {
  uploaded: number;
  skipped: string[];
  failed: string[];
}

let seq = 0;

/** Show a transfer's progress in one toast until done() is called. */
function progressToast(what: string) {
  const id = `transfer-${Date.now()}-${seq++}`;
  toast.loading(what, { id });
  const off = listen<{ id: string; done: number; total: number; current: string }>("transfer", (e) => {
    if (e.payload.id !== id) return;
    const { done, total } = e.payload;
    toast.loading(what, { id, description: total > 0 ? `${Math.round((done / total) * 100)}% · ${formatBytes(done)} of ${formatBytes(total)}` : formatBytes(done) });
  });
  return {
    id,
    done: (fn: () => void) => {
      off.then((f) => f());
      toast.dismiss(id);
      fn();
    },
  };
}

function scopeArg(p: Place) {
  return { space: p.spaceId ?? "", drive: p.driveId ?? "" };
}

/** Download items of `folder` (folders as zips): one asks where to save it, several ask for a folder. */
export async function download(p: Place, folder: string, items: { name: string; dir: boolean }[]) {
  if (items.length === 0) return;
  if (isDemo()) {
    for (const it of items) {
      if (it.dir) {
        toast(`Folders download as a zip from a real Node; the demo has none for ${it.name}`);
        continue;
      }
      const url = await contentUrl({ ...p, path: [folder, it.name].filter(Boolean).join("/") });
      if (!url) {
        toast(`The demo has no contents for ${it.name}`);
        continue;
      }
      const a = Object.assign(document.createElement("a"), { href: url, download: it.name });
      a.click();
    }
    return;
  }
  const { save, open } = await import("@tauri-apps/plugin-dialog");
  const fileName = (it: { name: string; dir: boolean }) => (it.dir ? `${it.name}.zip` : it.name);
  let dests: string[];
  if (items.length === 1) {
    const d = await save({ defaultPath: fileName(items[0]), title: `Save ${items[0].name}` });
    if (!d) return;
    dests = [d];
  } else {
    const d = await open({ directory: true, title: `Save ${items.length} items into…` });
    if (!d || Array.isArray(d)) return;
    dests = items.map((it) => `${d}/${fileName(it)}`);
  }
  const t = progressToast(items.length === 1 ? `Downloading ${items[0].name}` : `Downloading ${items.length} items`);
  const failed: string[] = [];
  for (const [i, it] of items.entries()) {
    try {
      await invoke("files_download", { addr: p.target.addr, fingerprint: p.target.fingerprint, scope: scopeArg(p), path: [folder, it.name].filter(Boolean).join("/"), folder: it.dir, dest: dests[i], id: t.id });
    } catch (e) {
      failed.push(`${it.name}: ${String((e as Error)?.message ?? e)}`);
    }
  }
  t.done(() => (failed.length ? toast.error(`Not downloaded: ${failed.join("; ")}`) : toast.success(items.length === 1 ? `${items[0].name} saved` : `${items.length} items saved`)));
}

/** Upload local paths (from the picker or a drop) into `folder`. */
export async function uploadPaths(p: Place, folder: string, paths: string[]): Promise<UploadResult | undefined> {
  if (paths.length === 0) return;
  const t = progressToast(paths.length === 1 ? `Uploading ${paths[0].split(/[\\/]/).pop()}` : `Uploading ${paths.length} items`);
  try {
    const r = await invoke<UploadResult>("files_upload", { addr: p.target.addr, fingerprint: p.target.fingerprint, scope: scopeArg(p), folder, paths, id: t.id });
    t.done(() => report(r));
    return r;
  } catch (e) {
    t.done(() => toast.error(String((e as Error)?.message ?? e)));
  }
}

export async function pickAndUpload(p: Place, folder: string, folders: boolean) {
  const { open } = await import("@tauri-apps/plugin-dialog");
  const picked = await open({ multiple: true, directory: folders, title: folders ? "Upload folders" : "Upload files" });
  if (!picked) return;
  return uploadPaths(p, folder, Array.isArray(picked) ? picked : [picked]);
}

/** The demo: files chosen in the browser go into the made-up Fleet. */
export async function demoUpload(p: Place, folder: string, files: FileList | File[]) {
  const { demoFleet } = await import("@/demo/fleet");
  const r: UploadResult = { uploaded: 0, skipped: [], failed: [] };
  for (const f of Array.from(files)) {
    const rel = (f as File & { webkitRelativePath?: string }).webkitRelativePath || f.name;
    if (rel.split("/").some((part) => part.startsWith("."))) continue;
    const path = [folder, rel].filter(Boolean).join("/");
    if (demoFleet().upload({ spaceId: p.spaceId, driveId: p.driveId }, p.nodeId, path, f)) r.uploaded++;
    else r.skipped.push(path);
  }
  report(r);
  return r;
}

function report(r: UploadResult) {
  const parts = [`${r.uploaded} uploaded`];
  if (r.skipped.length) parts.push(`${r.skipped.length} skipped, already there: ${r.skipped.slice(0, 3).join(", ")}${r.skipped.length > 3 ? "…" : ""}`);
  if (r.failed.length) parts.push(`${r.failed.length} failed: ${r.failed.slice(0, 3).join(", ")}`);
  (r.failed.length ? toast.error : r.skipped.length ? toast.warning : toast.success)(parts.join(" · "));
}
