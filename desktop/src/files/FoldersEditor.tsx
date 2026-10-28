// Where a Service's data lives (decisions 170-176): each volume in its own
// folder or a folder the user picks, and extra folders mounted into it.
// The install sheet and the Storage sheet both use it.
import { useEffect, useState } from "react";
import { FolderOpen, FolderSearch, Plus, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import type { Space } from "@/gen/maitre/v1/storage_pb";
import { FolderBrowser, type FolderSource } from "./FolderBrowser";
import { volumeDir } from "./layout";

export interface MountChoice {
  spaceId: string;
  folder: string;
  path: string;
  readOnly: boolean;
}

export interface Folders {
  volumeSpaces: Record<string, string>;
  /** Volume name -> picked folder; absent = its own folder. */
  volumeFolders: Record<string, string>;
  mounts: MountChoice[];
}

export interface VolumeInfo {
  name: string;
  path: string;
  size?: string;
  shared?: boolean;
}

/** Install defaults: own folders in the default Space, except volumes the
 * Catalog marks shared, which point at the top of a Space named like them
 * (Jellyfin's media at Media) or the default Space (175). */
export function defaultFolders(volumes: VolumeInfo[], spaces: Space[], fallback: string): Folders {
  const f: Folders = { volumeSpaces: {}, volumeFolders: {}, mounts: [] };
  for (const v of volumes) {
    if (v.shared) {
      const like = spaces.find((s) => s.name.toLowerCase() === v.name.toLowerCase());
      f.volumeSpaces[v.name] = like?.spaceId ?? fallback;
      f.volumeFolders[v.name] = "";
    } else f.volumeSpaces[v.name] = fallback;
  }
  return f;
}

export function folderLabel(spaces: Space[], spaceId: string, folder: string) {
  return [spaces.find((s) => s.spaceId === spaceId)?.name ?? spaceId, ...folder.split("/").filter(Boolean)].join(" / ");
}

export function FoldersEditor({ volumes, spaces, value, onChange, source, serviceName, lockOwnSpace, ownExtra, onOpen, ownSpaces }: { ownSpaces?: Space[]; volumes: VolumeInfo[]; spaces: Space[]; value: Folders; onChange: (f: Folders) => void; source: (spaceId: string) => FolderSource; serviceName: string; lockOwnSpace?: boolean; ownExtra?: (volume: string) => React.ReactNode; onOpen?: (at: { volume: string } | { mount: string }) => void }) {
  const [picking, setPicking] = useState<{ volume?: string; mount?: number; add?: boolean } | null>(null);
  const set = (patch: Partial<Folders>) => onChange({ ...value, ...patch });
  // Opens that folder on the Files tab, under the Service (195).
  const open = (at: { volume: string } | { mount: string }, what: string) =>
    onOpen && (
      <Button type="button" size="sm" variant="outline" className="shrink-0" title={`Open ${what} in Files`} aria-label={`Open ${what} in Files`} onClick={() => onOpen(at)}>
        <FolderSearch /> Open in Files
      </Button>
    );
  const pickedFor = picking?.volume ? { spaceId: value.volumeSpaces[picking.volume], folder: value.volumeFolders[picking.volume] ?? "" } : picking?.mount !== undefined ? value.mounts[picking.mount] : undefined;
  return (
    <div className="space-y-3">
      {volumes.length > 0 && (
        <ul className="divide-y rounded-lg border">
          {volumes.map((v) => {
            const picked = v.name in value.volumeFolders;
            const space = value.volumeSpaces[v.name] ?? "";
            return (
              <li key={v.name} className="space-y-2 px-3 py-2 text-sm">
                <div className="flex items-center gap-2">
                  <div className="min-w-0 flex-1">
                    <span className="font-medium">{v.name}</span>
                    <span className="ml-2 font-mono text-[11px] text-muted-foreground">{v.path}</span>
                  </div>
                  <div className="flex rounded-md border p-0.5 text-xs" role="radiogroup" aria-label={`Where ${v.name} lives`}>
                    <button type="button" role="radio" aria-checked={!picked} className={cn("rounded px-2 py-0.5", !picked && "bg-accent font-medium")} onClick={() => picked && set({ volumeFolders: Object.fromEntries(Object.entries(value.volumeFolders).filter(([k]) => k !== v.name)) })}>
                      Service's folder
                    </button>
                    <button type="button" role="radio" aria-checked={picked} className={cn("rounded px-2 py-0.5", picked && "bg-accent font-medium")} onClick={() => !picked && setPicking({ volume: v.name })}>
                      Shared folder
                    </button>
                  </div>
                </div>
                {picked ? (
                  <div className="flex items-center gap-2">
                    <button type="button" onClick={() => setPicking({ volume: v.name })} className="flex min-w-0 flex-1 items-center gap-2 rounded-md border px-2 py-1 text-left hover:bg-accent">
                      <FolderOpen className="size-4 text-status-starting" />
                      <span className="flex-1 truncate">{folderLabel(spaces, space, value.volumeFolders[v.name])}</span>
                      <span className="text-xs text-muted-foreground">shared · change</span>
                    </button>
                    {open({ volume: v.name }, v.name)}
                  </div>
                ) : lockOwnSpace ? (
                  <>
                    <p className="text-xs text-muted-foreground">
                      {folderLabel(spaces, space, volumeDir(serviceName, v.name))}
                      {v.size ? ` · ${v.size}` : ""}
                    </p>
                    {(ownExtra || onOpen) && (
                      <div className="flex items-center gap-2">
                        <div className="min-w-0 flex-1">{ownExtra?.(v.name)}</div>
                        {open({ volume: v.name }, v.name)}
                      </div>
                    )}
                  </>
                ) : (
                  <Select value={space} onValueChange={(val) => set({ volumeSpaces: { ...value.volumeSpaces, [v.name]: val } })}>
                    <SelectTrigger size="sm" className="w-full">
                      <SelectValue placeholder="Space" />
                    </SelectTrigger>
                    <SelectContent>
                      {(ownSpaces ?? spaces).map((s) => (
                        <SelectItem key={s.spaceId} value={s.spaceId}>
                          {s.name}
                          {s.defaultForNew ? " (default)" : ""} / .Services / {serviceName || "service"} / {v.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              </li>
            );
          })}
        </ul>
      )}
      <div className="space-y-1.5">
        <div className="flex items-center justify-between">
          <span className="text-xs font-medium text-muted-foreground">Extra folders</span>
          <Button type="button" size="sm" variant="outline" onClick={() => setPicking({ add: true })} disabled={spaces.length === 0}>
            <Plus /> Add a folder
          </Button>
        </div>
        {value.mounts.length === 0 ? (
          <p className="text-xs text-muted-foreground">None. Add folders other Services use too, such as a media library.</p>
        ) : (
          <ul className="divide-y rounded-lg border text-sm">
            {value.mounts.map((m, i) => (
              <li key={i} className="flex items-center gap-2 px-3 py-1.5">
                <button type="button" className="min-w-0 flex-1 truncate text-left hover:underline" onClick={() => setPicking({ mount: i })}>
                  {folderLabel(spaces, m.spaceId, m.folder)} <span className="font-mono text-[11px] text-muted-foreground">→ {m.path}</span>
                </button>
                {open({ mount: m.path }, m.path)}
                <label className="flex items-center gap-1 text-xs text-muted-foreground">
                  <Switch checked={m.readOnly} onCheckedChange={(ro) => set({ mounts: value.mounts.map((x, j) => (j === i ? { ...x, readOnly: ro } : x)) })} /> read only
                </label>
                <button type="button" className="rounded p-0.5 text-muted-foreground hover:text-foreground" aria-label={`Remove ${m.path}`} title="Remove (the folder stays)" onClick={() => set({ mounts: value.mounts.filter((_, j) => j !== i) })}>
                  <X className="size-3.5" />
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
      <FolderPickDialog
        open={!!picking}
        onClose={() => setPicking(null)}
        spaces={spaces}
        source={source}
        // A new extra folder starts in the Space of a folder already picked.
        initial={pickedFor ?? { spaceId: value.volumeSpaces[picking?.volume ?? Object.keys(value.volumeFolders)[0] ?? ""] ?? spaces[0]?.spaceId ?? "", folder: "" }}
        mount={picking?.add ? { path: "", readOnly: false } : picking?.mount !== undefined ? value.mounts[picking.mount] : undefined}
        taken={[...volumes.map((v) => v.path), ...value.mounts.filter((_, j) => j !== picking?.mount).map((m) => m.path)]}
        title={picking?.volume ? `Folder for ${picking.volume}` : "Extra folder"}
        onPick={(p) => {
          if (picking?.volume) set({ volumeSpaces: { ...value.volumeSpaces, [picking.volume]: p.spaceId }, volumeFolders: { ...value.volumeFolders, [picking.volume]: p.folder } });
          else if (picking?.mount !== undefined) set({ mounts: value.mounts.map((x, j) => (j === picking.mount ? { ...x, ...p, path: p.path ?? x.path, readOnly: p.readOnly ?? x.readOnly } : x)) });
          else set({ mounts: [...value.mounts, { spaceId: p.spaceId, folder: p.folder, path: p.path ?? "", readOnly: !!p.readOnly }] });
          setPicking(null);
        }}
      />
    </div>
  );
}

function FolderPickDialog({ open, onClose, spaces, source, initial, mount, taken, title, onPick }: { open: boolean; onClose: () => void; spaces: Space[]; source: (spaceId: string) => FolderSource; initial: { spaceId: string; folder: string }; mount?: { path: string; readOnly: boolean }; taken: string[]; title: string; onPick: (p: { spaceId: string; folder: string; path?: string; readOnly?: boolean }) => void }) {
  const [spaceId, setSpaceId] = useState(initial.spaceId);
  const [folder, setFolder] = useState(initial.folder);
  const [path, setPath] = useState(mount?.path ?? "");
  const [readOnly, setReadOnly] = useState(mount?.readOnly ?? false);
  const [pathTouched, setPathTouched] = useState(false);
  useEffect(() => {
    if (!open) return;
    setSpaceId(initial.spaceId || spaces[0]?.spaceId || "");
    setFolder(initial.folder);
    setPath(mount?.path ?? "");
    setReadOnly(mount?.readOnly ?? false);
    setPathTouched(!!mount?.path);
  }, [open]);
  // An extra folder's container path follows the folder until edited.
  const suggested = "/mnt/" + ((folder.split("/").filter(Boolean).pop() ?? spaces.find((s) => s.spaceId === spaceId)?.name ?? "data").toLowerCase().replace(/[^a-z0-9._-]+/g, "-") || "data");
  const effPath = mount ? (pathTouched ? path : suggested) : undefined;
  const pathProblem = effPath === undefined ? "" : !effPath.startsWith("/") || effPath === "/" ? "A full path inside the container, such as /media/movies" : effPath.includes(":") ? "A path cannot contain :" : taken.includes(effPath) ? "Already used inside the container" : "";
  const space = spaces.find((s) => s.spaceId === spaceId);
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>A folder in a Storage Space. Other Services can use the same folder; it stays when they are removed.</DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <div className="space-y-1.5">
            <Label>Space</Label>
            <Select value={spaceId} onValueChange={(v) => (setSpaceId(v), setFolder(""))}>
              <SelectTrigger className="w-full">
                <SelectValue placeholder="Space" />
              </SelectTrigger>
              <SelectContent>
                {spaces.map((s) => (
                  <SelectItem key={s.spaceId} value={s.spaceId}>
                    {s.name}
                    {s.spanNodes ? " (spans Nodes)" : ""}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <Label>Folder</Label>
            {spaceId && <FolderBrowser source={source(spaceId)} top={space?.name ?? "Space"} path={folder} onPath={setFolder} />}
            <p className="text-xs text-muted-foreground">
              Uses <span className="font-medium text-foreground">{folderLabel(spaces, spaceId, folder)}</span>
            </p>
          </div>
          {mount && (
            <div className="grid grid-cols-[1fr_auto] items-end gap-3">
              <div className="space-y-1.5">
                <Label htmlFor="mount-path">Inside the container at</Label>
                <Input
                  id="mount-path"
                  value={effPath}
                  onChange={(e) => {
                    setPath(e.target.value.trim());
                    setPathTouched(true);
                  }}
                  className="font-mono text-xs"
                />
              </div>
              <label className="flex items-center gap-2 pb-2 text-sm">
                <Switch checked={readOnly} onCheckedChange={setReadOnly} /> Read only
              </label>
              {pathProblem && <p className="col-span-2 text-xs text-destructive">{pathProblem}</p>}
            </div>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={!spaceId || !!pathProblem} onClick={() => onPick({ spaceId, folder, path: effPath, readOnly: mount ? readOnly : undefined })}>
            Use this folder
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
