// User Catalogs in the Console (plan C9): the sheet that lists the Fleet's
// Catalogs and adds or removes them, Add to Catalog with its report shown at
// once (C10), and what changed in a Catalog since this Console last looked.
import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { invoke } from "@tauri-apps/api/core";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { toast } from "sonner";
import { FileUp, FolderOpen, GitBranch, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { UserCatalogSchema, type UserCatalog } from "@/gen/maitre/v1/service_pb";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { services } from "@/fleet/actions";
import { optionNote } from "@/fleet/grants";
import { catalogId, fleetCatalogs, markSeen, onThisComputer, saveCatalogs, type Changes, type LoadedCatalog } from "@/fleet/catalogs";
import { cn } from "@/lib/utils";
import { specFor, type CatalogEntry } from "@/services";
import type { Target } from "@/transport";

const anchorOf = (snap: FleetSnapshot | undefined): Target | undefined => (snap ? { addr: snap.anchor.addr, fingerprint: snap.anchor.nodeId } : undefined);

/** The Fleet's Catalogs: the project's own, then the users', each with what this Console made of it. */
export function CatalogsSheet({ open, onClose, snap, deviceId, loaded }: { open: boolean; onClose: () => void; snap: FleetSnapshot | undefined; deviceId: string; loaded: LoadedCatalog[] | undefined }) {
  const qc = useQueryClient();
  const list = fleetCatalogs(snap?.records);
  const [adding, setAdding] = useState<"" | "git">("");
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [branch, setBranch] = useState("");
  const [busy, setBusy] = useState(false);
  const [report, setReport] = useState<{ catalog: UserCatalog; result: AddResult } | null>(null);

  async function save(next: UserCatalog[], done: string) {
    const t = anchorOf(snap);
    if (!t) return toast.error("No Node of the Fleet can be reached");
    setBusy(true);
    try {
      await saveCatalogs(t, snap?.records, next);
      toast.success(done);
      await qc.invalidateQueries({ queryKey: ["fleet"] });
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    } finally {
      setBusy(false);
    }
  }

  async function addFolder() {
    const { open: pick } = await import("@tauri-apps/plugin-dialog");
    const folder = await pick({ directory: true, title: "A Catalog folder: its entries, or a folder holding entries/" });
    if (typeof folder !== "string") return;
    const n = folder.split(/[\\/]/).filter(Boolean).pop() ?? "My Catalog";
    const c = create(UserCatalogSchema, { id: catalogId(n, list.map((x) => x.id)), name: n, folder, folderDevice: deviceId, addedUnixMs: BigInt(Date.now()) });
    await save([...list, c], `${n} added; its entries show in the Catalog`);
  }

  async function addGit() {
    const n = name.trim() || url.replace(/\.git$/, "").split("/").pop() || "Catalog";
    const c = create(UserCatalogSchema, { id: catalogId(n, list.map((x) => x.id)), name: n, gitUrl: url.trim(), gitBranch: branch.trim(), addedUnixMs: BigInt(Date.now()) });
    await save([...list, c], `${n} added; every Console of the Fleet follows it`);
    setAdding("");
    setName("");
    setUrl("");
    setBranch("");
  }

  async function addCompose(c: UserCatalog) {
    const { open: pick } = await import("@tauri-apps/plugin-dialog");
    const file = await pick({ title: "A Compose file to add to " + c.name, filters: [{ name: "Compose file", extensions: ["yaml", "yml"] }] });
    if (typeof file !== "string") return;
    setBusy(true);
    try {
      const at = loaded?.find((l) => l.catalog.id === c.id)?.dir || c.folder;
      const result = await convertCompose(at, file, anchorOf(snap));
      setReport({ catalog: c, result });
      await qc.invalidateQueries({ queryKey: ["catalogs"] });
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
        <SheetContent className="flex w-[480px] flex-col sm:max-w-[480px]">
          <SheetHeader>
            <SheetTitle>Catalogs</SheetTitle>
            <SheetDescription>Where the Services you can add come from. Every Console of the Fleet shows the same Catalogs.</SheetDescription>
          </SheetHeader>
          <div className="min-h-0 flex-1 space-y-3 overflow-y-auto px-4 pb-4 text-sm">
            <div className="rounded-lg border p-3">
              <div className="font-medium">Maître Catalog</div>
              <div className="text-xs text-muted-foreground">Built into this app; tested on a Node before each release.</div>
            </div>
            {list.map((c) => {
              const l = loaded?.find((x) => x.catalog.id === c.id);
              const mine = onThisComputer(c, deviceId);
              return (
                <div key={c.id} className="rounded-lg border p-3">
                  <div className="flex items-start gap-2">
                    {c.gitUrl ? <GitBranch className="mt-0.5 size-4 text-muted-foreground" /> : <FolderOpen className="mt-0.5 size-4 text-muted-foreground" />}
                    <div className="min-w-0 flex-1">
                      <div className="font-medium">{c.name}</div>
                      <div className="break-all text-xs text-muted-foreground">
                        {c.gitUrl ? `${c.gitUrl}${c.gitBranch ? ` (${c.gitBranch})` : ""}` : c.folder}
                        {l?.commit && <span> · at {l.commit.slice(0, 8)}</span>}
                      </div>
                      <div className="mt-1 text-xs">
                        {l?.elsewhere ? (
                          <span className="text-muted-foreground">A folder on another computer; its entries show in that computer's Console.</span>
                        ) : l?.error ? (
                          <span className="text-destructive">{l.error}</span>
                        ) : l ? (
                          <span className="text-muted-foreground">
                            {l.entries.length} {l.entries.length === 1 ? "entry" : "entries"}
                            {l.problems.length > 0 && `, ${l.problems.length} that could not be read: ${l.problems.map((p) => `${p.entry} (${p.error})`).join("; ")}`}
                          </span>
                        ) : (
                          <span className="text-muted-foreground">Reading…</span>
                        )}
                      </div>
                    </div>
                    <Button size="icon" variant="ghost" className="size-7" aria-label={`Remove ${c.name}`} disabled={busy} onClick={() => save(list.filter((x) => x.id !== c.id), `${c.name} removed; Services installed from it keep running`)}>
                      <Trash2 />
                    </Button>
                  </div>
                  {mine && (
                    <Button size="sm" variant="outline" className="mt-2" disabled={busy} onClick={() => addCompose(c)}>
                      <FileUp /> Add a Compose file…
                    </Button>
                  )}
                </div>
              );
            })}
            {adding === "git" ? (
              <form
                className="space-y-2 rounded-lg border p-3"
                onSubmit={(e) => {
                  e.preventDefault();
                  addGit();
                }}
              >
                <div className="space-y-1">
                  <Label htmlFor="cat-url">Git repository</Label>
                  <Input id="cat-url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://codeberg.org/me/my-catalog.git" autoFocus />
                </div>
                <div className="flex gap-2">
                  <div className="flex-1 space-y-1">
                    <Label htmlFor="cat-name">Name</Label>
                    <Input id="cat-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="from the repository" />
                  </div>
                  <div className="w-32 space-y-1">
                    <Label htmlFor="cat-branch">Branch</Label>
                    <Input id="cat-branch" value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="default" />
                  </div>
                </div>
                <p className="text-xs text-muted-foreground">Each Console follows the branch; what changes shows on the Catalog page. Nothing installed changes until someone installs or updates from it, and Nodes check every entry as they install it.</p>
                <div className="flex gap-2">
                  <Button type="submit" size="sm" disabled={busy || !/^(https?:\/\/|git@|ssh:\/\/)/.test(url.trim())}>
                    Add
                  </Button>
                  <Button type="button" size="sm" variant="ghost" onClick={() => setAdding("")}>
                    Cancel
                  </Button>
                </div>
              </form>
            ) : (
              <div className="flex flex-wrap gap-2">
                <Button size="sm" variant="outline" disabled={busy} onClick={addFolder}>
                  <FolderOpen /> Add a folder…
                </Button>
                <Button size="sm" variant="outline" disabled={busy} onClick={() => setAdding("git")}>
                  <GitBranch /> Add a git repository…
                </Button>
              </div>
            )}
          </div>
        </SheetContent>
      </Sheet>
      <ReportDialog report={report} onClose={() => setReport(null)} />
    </>
  );
}

/** What `maitre catalog add --json` says, and what a Node made of the result. */
export interface AddResult {
  report: { must_fix?: string[]; needs_person?: string[]; changed?: string[]; notes?: string[] };
  entry: CatalogEntry | null;
  dir: string;
  node?: { ok: boolean; protected: string[]; message: string };
}

/** Convert a Compose file into a folder Catalog, then have a Node check the bundle as it would install it. */
export async function convertCompose(folder: string, compose: string, node: Target | undefined): Promise<AddResult> {
  const res = await invoke<AddResult>("catalog_add", { folder, compose });
  if (res.entry && res.dir && node) res.node = await nodeCheck(node, res.entry);
  return res;
}

/** CheckService on a Node (plan C10); an older Node says so instead. */
export async function nodeCheck(t: Target, entry: CatalogEntry): Promise<NonNullable<AddResult["node"]>> {
  try {
    const r = await services(t).checkService({ spec: specFor(entry, "", {}) });
    return { ok: true, protected: r.protected.map((p) => (p.component ? `${p.component}: ${p.option}` : p.option)), message: "" };
  } catch (e) {
    if (e instanceof ConnectError && e.code === Code.Unimplemented) return { ok: false, protected: [], message: "The Node runs an older Maître Server, so only this computer's checks ran. Update it on the Nodes tab for the full check." };
    if (e instanceof ConnectError && e.code === Code.InvalidArgument) return { ok: false, protected: [], message: `The Node would refuse it: ${e.rawMessage}` };
    return { ok: false, protected: [], message: `The Node could not check it: ${String((e as Error).message ?? e)}` };
  }
}

function ReportDialog({ report, onClose }: { report: { catalog: UserCatalog; result: AddResult } | null; onClose: () => void }) {
  const r = report?.result.report;
  const written = !!report?.result.dir;
  const section = (title: string, items: string[] | undefined, tone?: string) =>
    items?.length ? (
      <div>
        <h4 className={cn("mb-1 text-sm font-medium", tone)}>{title}</h4>
        <ul className="list-disc space-y-0.5 pl-5 text-xs">
          {items.map((i) => (
            <li key={i}>{i}</li>
          ))}
        </ul>
      </div>
    ) : null;
  const node = report?.result.node;
  return (
    <Dialog open={!!report} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{written ? `${report?.result.entry?.name} is in ${report?.catalog.name}` : "Not added"}</DialogTitle>
          <DialogDescription>
            {written
              ? report?.result.entry?.unfinished?.length
                ? "Written, but unfinished: it cannot be installed until the points below are decided in its folder and its unfinished: list is removed."
                : "Written and ready to install. Review what was changed for you."
              : "Nothing was written. Fix these in the Compose file and add it again."}
          </DialogDescription>
        </DialogHeader>
        <div className="max-h-[60vh] space-y-3 overflow-y-auto">
          {section("Must fix", r?.must_fix, "text-destructive")}
          {section("Needs a person", r?.needs_person, "text-status-attention")}
          {section("Changed for you", r?.changed)}
          {section("Notes", r?.notes, "text-muted-foreground")}
          {node && (
            <div>
              <h4 className="mb-1 text-sm font-medium">Checked on a Node</h4>
              {node.ok ? (
                <p className="text-xs">
                  Podman's Quadlet on the Node accepts every unit.
                  {node.protected.length > 0 && " Installing it will ask a Trusted Device to allow:"}
                </p>
              ) : (
                <p className="text-xs text-muted-foreground">{node.message}</p>
              )}
              <ul className="mt-1 space-y-1 text-xs">
                {node.protected.map((p) => (
                  <li key={p}>
                    <code>{p}</code> <span className="text-muted-foreground">{optionNote(p.replace(/^[^:]+: (?=\w+=)/, ""))}</span>
                  </li>
                ))}
              </ul>
            </div>
          )}
          {written && <p className="text-xs text-muted-foreground">Saved as CONVERSION.md in {report?.result.dir}.</p>}
        </div>
      </DialogContent>
    </Dialog>
  );
}

/** A Catalog's changes since this Console last looked, with each changed entry's diff. */
export function ChangesDialog({ loaded, changes, onClose }: { loaded: LoadedCatalog | null; changes: Changes | null; onClose: () => void }) {
  return (
    <Dialog open={!!loaded} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>What changed in {loaded?.catalog.name}</DialogTitle>
          <DialogDescription>Since you last looked{loaded?.commit ? `; now at ${loaded.commit.slice(0, 8)}` : ""}. Installed Services are not changed by this; each changes only when someone updates it.</DialogDescription>
        </DialogHeader>
        <div className="max-h-[60vh] space-y-3 overflow-y-auto text-sm">
          {!!changes?.added.length && (
            <div>
              <h4 className="mb-1 font-medium">New</h4>
              <ul className="list-disc pl-5 text-xs">
                {changes.added.map((e) => (
                  <li key={e.id}>
                    {e.name}: {e.summary}
                  </li>
                ))}
              </ul>
            </div>
          )}
          {changes?.changed.map(({ entry, diff }) => (
            <div key={entry.id}>
              <h4 className="mb-1 font-medium">Changed: {entry.name}</h4>
              <pre className="overflow-x-auto rounded-md border bg-muted/40 p-2 text-[11px] leading-snug" aria-label={`Changes to ${entry.name}`}>
                {diff.map((l, i) => (
                  <div key={i} className={cn(l.kind === "+" && "bg-status-good/15", l.kind === "-" && "bg-destructive/10")}>
                    {l.kind} {l.text}
                  </div>
                ))}
              </pre>
            </div>
          ))}
          {!!changes?.removed.length && (
            <div>
              <h4 className="mb-1 font-medium">Removed</h4>
              <p className="text-xs">{changes.removed.join(", ")}</p>
            </div>
          )}
        </div>
        <div className="flex justify-end">
          <Button
            size="sm"
            onClick={() => {
              if (loaded) markSeen(loaded);
              onClose();
            }}
          >
            Mark as seen
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
