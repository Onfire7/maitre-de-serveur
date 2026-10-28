// The Files tab (decision 122, docs/ui-needs.md): browse a Space's Pool on a
// Node or a Drive's root, see where each entry lives and how big it is, and
// queue moves between Spaces, Drives and Nodes. Every call goes straight to
// the Node that holds the files (160); jobs live on that Node. Under
// Services, each Service shows the folders it uses, wherever they are (195).
// The Fleet and each Node open too, as the places they hold (198).
import { useEffect, useMemo, useRef, useState } from "react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { toast } from "sonner";
import { Folder, File, ChevronRight, HardDrive, Layers, MoveRight, X, Loader2, RefreshCw, Boxes, FolderPlus, CornerLeftUp, Package, Server, Upload, Download, FileArchive, PackageOpen, ChevronDown, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { isDemo } from "@/demo/flag";
import { Preview } from "@/files/Preview";
import { FolderBrowser } from "@/files/FolderBrowser";
import { HoldButton } from "@/components/hold-button";
import { unzippable } from "@/files/kinds";
import { demoUpload, download, pickAndUpload, uploadPaths, type Place } from "@/files/transfers";
import { cn } from "@/lib/utils";
import { formatBytes, ago } from "@/lib/format";
import { FilesService, type Entry, type Job } from "@/gen/maitre/v1/files_pb";
import { transportFor, type Target } from "@/transport";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { nodeDown, nodeWord } from "@/fleet/useFleet";
import { targetFor } from "@/fleet/actions";
import type { ConsoleState } from "@/state";
import { spacesOf } from "@/spaces";
import { spaceColor } from "./StorageTab";
import type { FleetService } from "@/fleet/useFleet";
import { serviceFolders, type ServiceFolder } from "@/files/sources";
import { ServiceIcon } from "./status";

export interface FileScope {
  nodeId: string;
  spaceId?: string;
  driveId?: string;
  path?: string;
}

const scopeKey = (s: FileScope) => `${s.nodeId}:${s.spaceId ?? ""}:${s.driveId ?? ""}`;
const sameScope = (a: FileScope, b: FileScope & { group?: string }) => (b.group === "fleet" ? a.spaceId === b.spaceId && !a.driveId : scopeKey(a) === scopeKey(b));

/** A Service's folder opened from the Services section: browsing stays
 * rooted at it, under the Service's name. */
interface Via {
  service: string;
  folder: ServiceFolder;
}

export function FilesTab({ snap, state, initial, services = [] }: { snap: FleetSnapshot | undefined; state: ConsoleState; initial?: FileScope & { service?: string; folder?: string }; services?: FleetService[] }) {
  const qc = useQueryClient();
  const nodes = snap?.nodes ?? [];
  const spaces = useMemo(() => [...(snap?.spaces ?? [])].sort((a, b) => b.priority - a.priority), [snap?.spaces]);
  const drives = snap?.drives ?? [];
  // Every place that can be browsed: a Space's Pool on each Node that has a
  // Drive of it (or spans), and each Drive's root.
  // A spanning Space is one place, the Fleet's: its merged Pool is the same
  // from any Node that has it, so it is listed once under the Fleet and read
  // through the anchor when the anchor has it, else the first Node that does.
  // Other Spaces and every Drive root belong to one Node.
  const scopes = useMemo(() => {
    const out: (FileScope & { group: string; label: string; kind: "space" | "drive"; color?: string })[] = [];
    const reachable = (id: string) => !nodeDown(nodeWord(snap?.status.get(id), Date.now()));
    const has = (n: { nodeId: string }, s: { spaceId: string }) => drives.filter((d) => d.nodeId === n.nodeId).some((d) => spacesOf(d, spaces).includes(s.spaceId));
    spaces.forEach((s, i) => {
      if (!s.spanNodes) return;
      const holders = nodes.filter((n) => has(n, s));
      const via = holders.find((n) => n.nodeId === snap?.anchor.nodeId && reachable(n.nodeId)) ?? holders.find((n) => reachable(n.nodeId)) ?? holders[0] ?? (snap ? nodes.find((n) => n.nodeId === snap.anchor.nodeId) : undefined);
      if (via) out.push({ group: "fleet", nodeId: via.nodeId, spaceId: s.spaceId, label: s.name, kind: "space", color: spaceColor(i) });
    });
    for (const n of nodes) {
      const mine = drives.filter((d) => d.nodeId === n.nodeId);
      spaces.forEach((s, i) => {
        if (!s.spanNodes && has(n, s)) out.push({ group: n.nodeId, nodeId: n.nodeId, spaceId: s.spaceId, label: s.name, kind: "space", color: spaceColor(i) });
      });
      for (const d of [...mine].sort((a, b) => a.name.localeCompare(b.name))) out.push({ group: n.nodeId, nodeId: n.nodeId, driveId: d.driveId, label: d.name, kind: "drive" });
    }
    return out;
  }, [nodes, spaces, drives, snap]);
  const [scope, setScope] = useState<FileScope | undefined>(initial?.service ? undefined : initial);
  // The Service whose folders are listed, and the one of them open (195).
  // `?files-service=<name>` opens one on load (screenshot fixture).
  const [svc, setSvc] = useState<string | null>(() => initial?.service ?? new URLSearchParams(window.location.search).get("files-service"));
  const [via, setVia] = useState<Via | null>(null);
  // The Fleet ({}) or a Node ({ nodeId }) shown as the places it holds
  // (198); where the tab opens unless asked for a place. `?files-view=fleet`
  // or `?files-view=<node name>` opens one (screenshot fixture).
  const [overview, setOverview] = useState<{ nodeId?: string } | null>(() => {
    const q = new URLSearchParams(window.location.search);
    if (initial || q.get("path") || q.get("files-service")) return null;
    const v = q.get("files-view");
    const n = v && v !== "fleet" ? nodes.find((x) => x.name.toLowerCase() === v.toLowerCase()) : undefined;
    return n ? { nodeId: n.nodeId } : {};
  });
  function showOverview(o: { nodeId?: string }) {
    setOverview(o);
    setSvc(null);
    setVia(null);
    setPreview(null);
  }
  const svcOf = (name: string | null) => services.find((s) => s.spec.name === name);
  const shownSvc = svcOf(svc);
  const svcFolders = shownSvc ? serviceFolders(shownSvc.spec) : [];
  function openFolder(service: FleetService, f: ServiceFolder) {
    setOverview(null);
    setSvc(service.spec.name);
    setVia({ service: service.spec.name, folder: f });
    setScope({ nodeId: service.spec.nodeId, spaceId: f.spaceId });
    setPath(f.path);
    setPreview(null);
  }
  function showService(name: string) {
    setOverview(null);
    setSvc(name);
    setVia(null);
    setPreview(null);
  }
  // `?path=` opens that folder of the first place on load (screenshot fixture).
  const [path, setPath] = useState(initial?.path ?? new URLSearchParams(window.location.search).get("path") ?? "");
  useEffect(() => {
    if (!scope && !svc && !overview && scopes[0]) setScope(scopes[0]);
  }, [scopes, scope, svc, overview]);
  useEffect(() => {
    if (!initial) return;
    setOverview(null);
    if (initial.service) {
      const s = svcOf(initial.service);
      const f = s && initial.folder ? serviceFolders(s.spec).find((x) => x.key === initial.folder) : undefined;
      if (s && f) openFolder(s, f);
      else showService(initial.service);
      return;
    }
    setSvc(null);
    setVia(null);
    setScope(initial);
    setPath(initial.path ?? "");
  }, [initial]);
  // Listing a Service's folders, none open yet: nothing to read.
  const listing = (!!shownSvc && !via) || !!overview;
  // `?move=<name>` opens Move for that entry of `?path=` (screenshot fixture).
  const moveFixture = useState(() => new URLSearchParams(window.location.search).get("move"))[0];
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [moving, setMoving] = useState(!!moveFixture);
  const [newFolder, setNewFolder] = useState(false);
  // Decision 186: names about to be deleted, waiting for the hold.
  const [deleting, setDeleting] = useState<string[] | null>(null);
  // The file being previewed and whether it fills the window (179).
  // `?preview=<name>` opens one of `?path=` on load (screenshot fixture).
  const [preview, setPreview] = useState<string | null>(() => new URLSearchParams(window.location.search).get("preview"));
  const [full, setFull] = useState(false);
  const [dropping, setDropping] = useState(false);
  const pickFiles = useRef<HTMLInputElement>(null);
  const pickFolder = useRef<HTMLInputElement>(null);
  const target = scope ? targetFor(snap, state.nodes, scope.nodeId) : undefined;
  const client = useMemo(() => (target ? createClient(FilesService, transportFor(target)) : undefined), [target?.addr, target?.fingerprint]);
  const unreachable = scope ? nodeDown(nodeWord(snap?.status.get(scope.nodeId), Date.now())) : false;
  const list = useQuery({
    queryKey: ["files", scope && scopeKey(scope), path, target?.addr],
    enabled: !!client && !!scope && !unreachable && !listing,
    retry: false,
    queryFn: async () => (await client!.listDir({ scope: { spaceId: scope!.spaceId ?? "", driveId: scope!.driveId ?? "" }, path })).entries,
  });
  const entries = useMemo(() => [...(list.data ?? [])].sort((a, b) => Number(b.dir) - Number(a.dir) || a.name.localeCompare(b.name)), [list.data]);
  useEffect(() => setSelected(new Set(moveFixture && list.data ? [moveFixture] : [])), [scope && scopeKey(scope), path, !!list.data]);
  const fleetName = state.fleet?.name ?? "Fleet";
  // The Fleet's and each Node's places, as rows that open them (198).
  const drivesOf = (spaceId: string, nodeId?: string) => drives.filter((d) => (!nodeId || d.nodeId === nodeId) && spacesOf(d, spaces).includes(spaceId));
  const bytes = (ds: { sizeBytes: bigint }[]) => formatBytes(ds.reduce((a, d) => a + Number(d.sizeBytes), 0));
  const count = (n: number, one: string) => `${n} ${one}${n === 1 ? "" : "s"}`;
  const openPlace = (s: (typeof scopes)[number]) => {
    setOverview(null);
    setSvc(null);
    setVia(null);
    setScope(s);
    setPath("");
    setPreview(null);
  };
  const placeRow = (s: (typeof scopes)[number], nodeId?: string): PlaceRow => {
    if (s.kind === "drive") {
      const d = drives.find((x) => x.nodeId === s.nodeId && x.driveId === s.driveId);
      return { key: scopeKey(s), icon: <HardDrive className="size-4 text-muted-foreground" />, label: s.label, kind: "Drive", detail: d ? `${bytes([d])}${d.model ? ` · ${d.model}` : ""}` : "", open: () => openPlace(s) };
    }
    // Inside a Node, a spanning Space counts that Node's Drives of it.
    const ds = drivesOf(s.spaceId!, nodeId ?? (s.group === "fleet" ? undefined : s.nodeId));
    const here = s.group === "fleet" && nodeId ? ` on ${nodeName(nodeId)}` : "";
    return { key: scopeKey(s), icon: <Layers className="size-4" style={{ color: s.color }} />, label: s.label, kind: s.group === "fleet" ? "Space, spans Nodes" : "Space", detail: `${count(ds.length, "Drive")} · ${bytes(ds)}${here}`, open: () => openPlace(s) };
  };
  const fleetRows = (): PlaceRow[] => [
    ...scopes.filter((s) => s.group === "fleet").map((s) => placeRow(s)),
    ...nodes.map((n) => {
      const own = scopes.filter((s) => s.group === n.nodeId);
      const mine = drives.filter((d) => d.nodeId === n.nodeId);
      const down = nodeDown(nodeWord(snap?.status.get(n.nodeId), Date.now()));
      return { key: `node:${n.nodeId}`, icon: <Server className="size-4 text-muted-foreground" />, label: n.name, kind: "Node", detail: `${count(own.filter((s) => s.kind === "space").length, "Space")} · ${count(mine.length, "Drive")} · ${bytes(mine)}${down ? " · unreachable" : ""}`, open: () => showOverview({ nodeId: n.nodeId }) };
    }),
  ];
  // A Node's own Spaces, the spanning ones it holds a Drive of, its Drives.
  const nodeRows = (nodeId: string): PlaceRow[] => [
    ...scopes.filter((s) => s.group === "fleet" && drivesOf(s.spaceId!, nodeId).length > 0).map((s) => placeRow(s, nodeId)),
    ...scopes.filter((s) => s.group === nodeId && s.kind === "space").map((s) => placeRow(s)),
    ...scopes.filter((s) => s.group === nodeId && s.kind === "drive").map((s) => placeRow(s)),
  ];
  const spaceLabel = (id: string) => spaces.find((s) => s.spaceId === id)?.name ?? id;
  const nodeName = (id: string) => nodes.find((n) => n.nodeId === id)?.name ?? id.slice(0, 8);
  const driveName = (nid: string, did: string) => drives.find((d) => d.nodeId === nid && d.driveId === did)?.name ?? did;
  const crumbs = path.split("/").filter(Boolean);
  const current = scope ? scopes.find((s) => sameScope(scope, s)) : undefined;
  const go = (p: string) => {
    setPath(p);
    setPreview(null);
  };
  const place: Place | undefined = scope && target ? { target, nodeId: scope.nodeId, spaceId: scope.spaceId, driveId: scope.driveId } : undefined;
  const wire = scope ? { spaceId: scope.spaceId ?? "", driveId: scope.driveId ?? "" } : undefined;
  const files = entries.filter((e) => !e.dir);
  const shown = preview ? files.find((e) => e.name === preview) : undefined;
  const shownAt = shown ? files.indexOf(shown) : -1;
  const step = (d: number) => files.length && setPreview(files[(shownAt + d + files.length) % files.length].name);
  const chosen = entries.filter((e) => selected.has(e.name));
  const canWrite = !!client && !!scope && !unreachable && !!list.data && !listing;
  // Inside a Service's folder the path shows from that folder down.
  const base = via ? via.folder.path.split("/").filter(Boolean) : [];
  const rel = via ? crumbs.slice(base.length) : crumbs;
  async function afterUpload() {
    await list.refetch();
  }
  async function zip(names: string[]) {
    if (!client || !wire) return;
    try {
      const r = await client.zip({ scope: wire, folder: path, names });
      toast.success(`Zipping into ${r.name}`);
      setSelected(new Set());
      qc.invalidateQueries({ queryKey: ["jobs"] });
    } catch (e) {
      toast.error(e instanceof ConnectError && e.code === Code.Unimplemented ? `${nodeName(scope!.nodeId)} runs an older Maître Server; update it to zip here.` : String((e as Error).message ?? e));
    }
  }
  async function unzip(name: string) {
    if (!client || !wire) return;
    try {
      const r = await client.unzip({ scope: wire, path: [...crumbs, name].join("/") });
      toast.success(`Unzipping into ${r.folder}`);
      qc.invalidateQueries({ queryKey: ["jobs"] });
    } catch (e) {
      toast.error(e instanceof ConnectError && e.code === Code.Unimplemented ? `${nodeName(scope!.nodeId)} runs an older Maître Server; update it to unzip here.` : String((e as Error).message ?? e));
    }
  }
  async function remove(names: string[]) {
    if (!client || !wire) return;
    try {
      await client.delete({ scope: wire, folder: path, names });
      toast.success(names.length === 1 ? `Deleting ${names[0]}` : `Deleting ${names.length} items`);
      setSelected(new Set());
      if (preview && names.includes(preview)) setPreview(null);
      qc.invalidateQueries({ queryKey: ["jobs"] });
    } catch (e) {
      toast.error(e instanceof ConnectError && e.code === Code.Unimplemented ? `${nodeName(scope!.nodeId)} runs an older Maître Server; update it to delete here.` : String((e as Error).message ?? e));
    }
  }
  // Files dropped on the window from the desktop upload into the open
  // folder (185). Tauri reports the drop with the files' paths.
  useEffect(() => {
    if (isDemo() || !place || !canWrite) return;
    let off: (() => void) | undefined;
    let gone = false;
    import("@tauri-apps/api/webview")
      .then(({ getCurrentWebview }) =>
        getCurrentWebview().onDragDropEvent((e) => {
          if (e.payload.type === "over" || e.payload.type === "enter") setDropping(true);
          else if (e.payload.type === "leave") setDropping(false);
          else if (e.payload.type === "drop") {
            setDropping(false);
            uploadPaths(place, path, e.payload.paths).then(afterUpload);
          }
        }),
      )
      .then((f) => (gone ? f() : (off = f)))
      .catch(() => {});
    return () => {
      gone = true;
      off?.();
    };
  }, [place?.target.addr, place?.spaceId, place?.driveId, path, canWrite]);

  return (
    <div className="grid h-full grid-cols-[220px_1fr] gap-6">
      <aside className="space-y-4 overflow-y-auto pr-2">
        {[{ id: "fleet", letter: "", name: state.fleet?.name ?? "Fleet" }, ...nodes.map((n) => ({ id: n.nodeId, letter: n.letter, name: n.name }))].map((n) => (
          <div key={n.id}>
            <button
              onClick={() => showOverview(n.id === "fleet" ? {} : { nodeId: n.id })}
              title={n.id === "fleet" ? "Everything in the Fleet" : `Everything on ${n.name}`}
              className={cn("mb-1 flex w-full items-center gap-1.5 rounded-md px-1 py-0.5 text-left text-xs font-semibold text-muted-foreground hover:bg-accent hover:text-foreground", overview && (overview.nodeId ?? "fleet") === n.id && "bg-accent text-foreground")}
            >
              {n.id === "fleet" ? <Boxes className="size-4" /> : <span className="inline-flex size-4 items-center justify-center rounded-full bg-primary text-[10px] text-primary-foreground">{n.letter}</span>} {n.name}
            </button>
            <ul className="space-y-0.5">
              {scopes
                .filter((s) => s.group === n.id)
                .map((s) => (
                  <li key={scopeKey(s)}>
                    <button
                      onClick={() => {
                        setOverview(null);
                        setSvc(null);
                        setVia(null);
                        setScope(s);
                        setPath("");
                      }}
                      className={cn("flex w-full items-center gap-2 rounded-md px-2 py-1 text-left text-sm hover:bg-accent", !svc && !overview && scope && sameScope(scope, s) && "bg-accent")}
                    >
                      {s.kind === "space" ? <Layers className="size-4 shrink-0" style={{ color: s.color }} /> : <HardDrive className="size-4 shrink-0 text-muted-foreground" />}
                      <span className="truncate">{s.label}</span>
                      {s.kind === "drive" && <span className="ml-auto text-[10px] text-muted-foreground">Drive</span>}
                    </button>
                  </li>
                ))}
              {scopes.filter((s) => s.group === n.id).length === 0 && <li className="px-2 text-xs text-muted-foreground">{n.id === "fleet" ? "no Space spans Nodes" : "nothing to browse"}</li>}
            </ul>
          </div>
        ))}
        {nodes.length === 0 && <p className="text-sm text-muted-foreground">No Node answers yet.</p>}
        <div>
          <div className="mb-1 flex items-center gap-1.5 text-xs font-semibold text-muted-foreground">
            <Package className="size-4" /> Services
          </div>
          <ul className="space-y-0.5">
            {[...services]
              .sort((a, b) => a.spec.name.localeCompare(b.spec.name))
              .map((s) => (
                <li key={s.spec.name}>
                  <button onClick={() => showService(s.spec.name)} className={cn("flex w-full items-center gap-2 rounded-md px-2 py-1 text-left text-sm hover:bg-accent", svc === s.spec.name && "bg-accent")}>
                    <ServiceIcon catalogId={s.spec.catalogId} word={s.word} size={16} />
                    <span className="truncate">{s.spec.name}</span>
                    <span className="ml-auto text-[10px] text-muted-foreground">{serviceFolders(s.spec).length || ""}</span>
                  </button>
                </li>
              ))}
            {services.length === 0 && <li className="px-2 text-xs text-muted-foreground">no Services yet</li>}
          </ul>
        </div>
      </aside>

      <div className="flex min-h-0 flex-col">
        <div className="mb-3 flex flex-wrap items-center gap-2">
          <nav className="flex min-w-0 flex-1 items-center gap-1 text-sm">
            {shownSvc ? (
              <>
                <button className="flex items-center gap-1.5 rounded px-1 font-medium hover:bg-accent" onClick={() => showService(shownSvc.spec.name)}>
                  <ServiceIcon catalogId={shownSvc.spec.catalogId} word={shownSvc.word} size={16} />
                  {shownSvc.spec.name}
                  <span className="text-xs font-normal text-muted-foreground">{via ? `${via.folder.inContainer} in the container · ${[spaceLabel(via.folder.spaceId), ...base].join(" / ")}` : `on ${nodeName(shownSvc.spec.nodeId)}`}</span>
                </button>
                {via && (
                  <span className="flex items-center gap-1">
                    <ChevronRight className="size-3.5 text-muted-foreground" />
                    <button className="rounded px-1 hover:bg-accent" onClick={() => go(base.join("/"))}>
                      {via.folder.label}
                    </button>
                  </span>
                )}
              </>
            ) : overview ? (
              <>
                <button className="flex items-center gap-1.5 rounded px-1 font-medium hover:bg-accent" onClick={() => showOverview({})}>
                  <Boxes className="size-4" /> {fleetName}
                </button>
                {overview.nodeId && (
                  <span className="flex items-center gap-1">
                    <ChevronRight className="size-3.5 text-muted-foreground" />
                    <span className="flex items-center gap-1.5 px-1 font-medium">
                      <Server className="size-4 text-muted-foreground" /> {nodeName(overview.nodeId)}
                    </span>
                  </span>
                )}
              </>
            ) : current && (
              <>
              <button className="flex items-center gap-1 rounded px-1 text-muted-foreground hover:bg-accent hover:text-foreground" onClick={() => showOverview({})} title={`Everything in ${fleetName}`}>
                <Boxes className="size-4" /> {fleetName}
              </button>
              <ChevronRight className="size-3.5 text-muted-foreground" />
              {current.group !== "fleet" && (
                <>
                  <button className="flex items-center gap-1 rounded px-1 text-muted-foreground hover:bg-accent hover:text-foreground" onClick={() => showOverview({ nodeId: current.nodeId })} title={`Everything on ${nodeName(current.nodeId)}`}>
                    <Server className="size-4" /> {nodeName(current.nodeId)}
                  </button>
                  <ChevronRight className="size-3.5 text-muted-foreground" />
                </>
              )}
              <button className="flex items-center gap-1.5 rounded px-1 font-medium hover:bg-accent" onClick={() => go("")}>
                {current.kind === "space" ? <Layers className="size-4" style={{ color: current.color }} /> : <HardDrive className="size-4 text-muted-foreground" />}
                {current.label}
                {current.group === "fleet" && <span className="text-xs font-normal text-muted-foreground">spans Nodes · via {nodeName(current.nodeId)}</span>}
              </button>
              </>
            )}
            {!listing && rel.map((c, i) => (
              <span key={i} className="flex items-center gap-1">
                <ChevronRight className="size-3.5 text-muted-foreground" />
                <button className="rounded px-1 hover:bg-accent" onClick={() => go([...base, ...rel.slice(0, i + 1)].join("/"))}>
                  {c}
                </button>
              </span>
            ))}
          </nav>
          {!listing && (
          <>
          <Button size="sm" variant="ghost" onClick={() => list.refetch()} title="Refresh">
            <RefreshCw className={cn("size-4", list.isFetching && "animate-spin")} />
          </Button>
          {selected.size > 0 ? (
            <>
              <span className="text-xs text-muted-foreground">{selected.size} selected</span>
              <Button size="sm" variant="outline" onClick={() => place && download(place, path, chosen)}>
                <Download /> Download
              </Button>
              <Button size="sm" variant="outline" disabled={!canWrite} onClick={() => zip([...selected])}>
                <FileArchive /> Zip
              </Button>
              {chosen.length === 1 && !chosen[0].dir && unzippable(chosen[0].name) && (
                <Button size="sm" variant="outline" disabled={!canWrite} onClick={() => unzip(chosen[0].name)}>
                  <PackageOpen /> Unzip
                </Button>
              )}
              <Button size="sm" variant="outline" className="text-destructive hover:text-destructive" disabled={!canWrite} onClick={() => setDeleting([...selected])}>
                <Trash2 /> Delete
              </Button>
              <Button size="sm" onClick={() => setMoving(true)}>
                <MoveRight /> Move…
              </Button>
            </>
          ) : (
            <>
              <Button size="sm" variant="outline" disabled={!canWrite} onClick={() => setNewFolder(true)}>
                <FolderPlus /> New folder
              </Button>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button size="sm" variant="outline" disabled={!canWrite}>
                    <Upload /> Upload <ChevronDown className="opacity-60" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem onSelect={() => (isDemo() ? pickFiles.current?.click() : place && pickAndUpload(place, path, false).then(afterUpload))}>Files…</DropdownMenuItem>
                  <DropdownMenuItem onSelect={() => (isDemo() ? pickFolder.current?.click() : place && pickAndUpload(place, path, true).then(afterUpload))}>A folder…</DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </>
          )}
          </>
          )}
          {isDemo() && (
            <>
              <input ref={pickFiles} type="file" multiple hidden onChange={(e) => (place && e.target.files && demoUpload(place, path, e.target.files).then(afterUpload), (e.target.value = ""))} />
              <input ref={pickFolder} type="file" multiple hidden {...{ webkitdirectory: "" }} onChange={(e) => (place && e.target.files && demoUpload(place, path, e.target.files).then(afterUpload), (e.target.value = ""))} />
            </>
          )}
        </div>

        <div className={cn("grid min-h-0 flex-1 gap-3", shown && !full ? "grid-cols-[minmax(0,1fr)_minmax(320px,45%)]" : "grid-cols-1")}>
        <div className={cn("relative min-h-0 overflow-y-auto rounded-xl border bg-card", dropping && "ring-2 ring-primary")}>
          {dropping && <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center bg-background/70 text-sm font-medium">Drop to upload into {[current?.label, ...crumbs].join(" / ")}</div>}
          {overview ? (
            <Places rows={overview.nodeId ? nodeRows(overview.nodeId) : fleetRows()} empty={overview.nodeId ? `${nodeName(overview.nodeId)} has no Space or Drive to browse yet.` : "Create a Storage Space or adopt a Drive first; then there is something to browse."} />
          ) : listing ? (
            <ServiceFolders service={shownSvc!} folders={svcFolders} spaceLabel={spaceLabel} nodeName={nodeName} onOpen={(f) => openFolder(shownSvc!, f)} />
          ) : !scope ? (
            <p className="p-4 text-sm text-muted-foreground">Create a Storage Space or adopt a Drive first; then there is something to browse.</p>
          ) : unreachable ? (
            <p className="p-4 text-sm text-muted-foreground">{nodeName(scope.nodeId)} is unreachable. Its files show when it is back.</p>
          ) : list.error ? (
            <p className="p-4 text-sm text-destructive">{String((list.error as Error).message ?? list.error)}</p>
          ) : !list.data ? (
            <p className="p-4 text-sm text-muted-foreground">Reading…</p>
          ) : entries.length === 0 && crumbs.length === 0 ? (
            <p className="p-4 text-sm text-muted-foreground">Empty.</p>
          ) : (
            <table className="w-full text-sm">
              <thead className="sticky top-0 bg-card text-xs text-muted-foreground">
                <tr className="border-b">
                  <th className="w-8 px-3 py-2">
                    <Checkbox disabled={entries.length === 0} checked={selected.size > 0 && selected.size === entries.length ? true : selected.size > 0 ? "indeterminate" : false} onCheckedChange={(v) => setSelected(v === true ? new Set(entries.map((e) => e.name)) : new Set())} aria-label="Select all" />
                  </th>
                  <th className="px-2 py-2 text-left font-medium">Name</th>
                  <th className="px-2 py-2 text-right font-medium">Size</th>
                  <th className="px-2 py-2 text-left font-medium">Changed</th>
                  <th className="px-3 py-2 text-left font-medium">Where</th>
                </tr>
              </thead>
              <tbody>
                {(crumbs.length > 0 || via) && (
                  <tr className="border-b last:border-0 hover:bg-accent/40">
                    <td className="px-3 py-1.5" />
                    <td className="px-2 py-1.5" colSpan={4}>
                      <button className="flex items-center gap-2 text-left hover:underline" title="Up one folder" onClick={() => (via && rel.length === 0 ? showService(via.service) : go(crumbs.slice(0, -1).join("/")))}>
                        <CornerLeftUp className="size-4 text-muted-foreground" />
                        ..
                      </button>
                    </td>
                  </tr>
                )}
                {entries.map((e) => (
                  <Row key={e.name} e={e} client={client!} scope={scope} path={path} selected={selected.has(e.name)} previewing={shown === e} onSelect={(on) => setSelected((s) => (on ? new Set([...s, e.name]) : new Set([...s].filter((x) => x !== e.name))))} onOpen={() => (e.dir ? go([...crumbs, e.name].join("/")) : setPreview(e.name))} nodeName={nodeName} driveName={driveName} />
                ))}
              </tbody>
            </table>
          )}
        </div>
        {shown && place && (
          <Preview
            entry={shown}
            file={{ ...place, path: [...crumbs, shown.name].join("/") }}
            index={shownAt}
            count={files.length}
            full={full}
            onFull={setFull}
            onPrev={() => step(-1)}
            onNext={() => step(1)}
            onClose={() => (setPreview(null), setFull(false))}
            onDownload={() => download(place, path, [shown])}
            onUnzip={() => unzip(shown.name)}
            onDelete={canWrite ? () => setDeleting([shown.name]) : undefined}
          />
        )}
        </div>

        <Jobs snap={snap} state={state} nodeName={nodeName} />
      </div>

      <DeleteDialog names={deleting} entries={entries} where={[current?.label ?? "", ...crumbs].join(" / ")} onClose={() => setDeleting(null)} onDelete={(names) => (setDeleting(null), remove(names))} />
      {scope && client && (
        <NewFolderDialog
          open={newFolder}
          onClose={() => setNewFolder(false)}
          where={[current?.label ?? "", ...crumbs].join(" / ")}
          taken={entries.map((e) => e.name)}
          onMake={async (name) => {
            try {
              await client.makeDir({ scope: { spaceId: scope.spaceId ?? "", driveId: scope.driveId ?? "" }, path: [...crumbs, name].join("/") });
            } catch (e) {
              if (e instanceof ConnectError && e.code === Code.Unimplemented) throw new Error(`${nodeName(scope.nodeId)} runs an older Maître Server; update it to make folders here.`);
              throw e;
            }
            toast.success(`${name} made`);
            await list.refetch();
          }}
        />
      )}
      {scope && client && (
        <MoveDialog
          open={moving}
          onClose={() => setMoving(false)}
          snap={snap}
          scope={scope}
          path={path}
          names={[...selected]}
          nodeName={nodeName}
          listFolders={async (nodeId, spaceId, p) => {
            const t = targetFor(snap, state.nodes, nodeId);
            if (!t) throw new Error("no address");
            const res = await createClient(FilesService, transportFor(t)).listDir({ scope: { spaceId, driveId: "" }, path: p });
            return res.entries.filter((e) => e.dir).map((e) => e.name).sort((a, b) => a.localeCompare(b));
          }}
          makeFolder={async (nodeId, spaceId, p) => {
            const t = targetFor(snap, state.nodes, nodeId);
            if (!t) throw new Error("no address");
            await createClient(FilesService, transportFor(t)).makeDir({ scope: { spaceId, driveId: "" }, path: p });
          }}
          onMove={async (req) => {
            for (const name of selected) {
              const rel = [...crumbs, name].join("/");
              await client.move({ from: { spaceId: scope.spaceId ?? "", driveId: scope.driveId ?? "" }, path: rel, toSpace: req.toSpace, toPath: [req.toPath, name].filter(Boolean).join("/"), toNodeId: req.toNodeId, toDriveId: req.toDriveId });
            }
            toast.success(`${selected.size} ${selected.size === 1 ? "item" : "items"} queued to move`);
            setSelected(new Set());
            qc.invalidateQueries({ queryKey: ["jobs"] });
          }}
        />
      )}
    </div>
  );
}

interface PlaceRow {
  key: string;
  icon: React.ReactNode;
  label: string;
  kind: string;
  detail: string;
  open: () => void;
}

/** The Fleet's or a Node's places as rows, each opening it (198). */
function Places({ rows, empty }: { rows: PlaceRow[]; empty: string }) {
  if (rows.length === 0) return <p className="p-4 text-sm text-muted-foreground">{empty}</p>;
  return (
    <table className="w-full text-sm">
      <thead className="sticky top-0 bg-card text-xs text-muted-foreground">
        <tr className="border-b">
          <th className="px-3 py-2 text-left font-medium">Name</th>
          <th className="px-2 py-2 text-left font-medium">Kind</th>
          <th className="px-3 py-2 text-left font-medium">Holds</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.key} className="border-b last:border-0 hover:bg-accent/40">
            <td className="px-3 py-1.5">
              <button className="flex items-center gap-2 text-left hover:underline" onClick={r.open} onDoubleClick={r.open}>
                {r.icon}
                {r.label}
              </button>
            </td>
            <td className="px-2 py-1.5 text-xs text-muted-foreground">{r.kind}</td>
            <td className="px-3 py-1.5 text-xs text-muted-foreground">{r.detail}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** A Service's folders as rows, each opening where it really is. */
function ServiceFolders({ service, folders, spaceLabel, nodeName, onOpen }: { service: FleetService; folders: ServiceFolder[]; spaceLabel: (id: string) => string; nodeName: (id: string) => string; onOpen: (f: ServiceFolder) => void }) {
  if (folders.length === 0) return <p className="p-4 text-sm text-muted-foreground">{service.spec.name} keeps no data and uses no folders.</p>;
  return (
    <table className="w-full text-sm">
      <thead className="sticky top-0 bg-card text-xs text-muted-foreground">
        <tr className="border-b">
          <th className="px-3 py-2 text-left font-medium">Folder</th>
          <th className="px-2 py-2 text-left font-medium">In the container</th>
          <th className="px-3 py-2 text-left font-medium">Where</th>
        </tr>
      </thead>
      <tbody>
        {folders.map((f) => (
          <tr key={f.key} className="border-b last:border-0 hover:bg-accent/40">
            <td className="px-3 py-1.5">
              <button className="flex items-center gap-2 text-left hover:underline" onClick={() => onOpen(f)}>
                <Folder className={cn("size-4", f.shared ? "text-status-starting" : "text-muted-foreground")} />
                {f.label}
                {f.key.startsWith("mount:") && <Badge variant="secondary">extra</Badge>}
                {f.readOnly && <Badge variant="outline">read only</Badge>}
              </button>
            </td>
            <td className="px-2 py-1.5 font-mono text-xs">{f.inContainer}</td>
            <td className="px-3 py-1.5 text-xs text-muted-foreground">
              {[spaceLabel(f.spaceId), ...f.path.split("/").filter(Boolean)].join(" / ")} on {nodeName(service.spec.nodeId)}
              {f.shared ? ", shared" : ", its own"}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function Row({ e, client, scope, path, selected, previewing, onSelect, onOpen, nodeName, driveName }: { e: Entry; client: ReturnType<typeof createClient<typeof FilesService>>; scope: FileScope; path: string; selected: boolean; previewing: boolean; onSelect: (on: boolean) => void; onOpen: () => void; nodeName: (id: string) => string; driveName: (n: string, d: string) => string }) {
  const [askSize, setAskSize] = useState(false);
  const usage = useQuery({
    queryKey: ["usage", scopeKey(scope), path, e.name],
    enabled: e.dir && askSize,
    refetchInterval: (q) => (q.state.data?.inProgress ? 1500 : false),
    queryFn: () => client.usage({ scope: { spaceId: scope.spaceId ?? "", driveId: scope.driveId ?? "" }, path: [path, e.name].filter(Boolean).join("/") }),
  });
  const where = whereOf(e, nodeName, driveName);
  return (
    <tr className={cn("border-b last:border-0 hover:bg-accent/40", selected && "bg-accent/60", previewing && "bg-primary/10")}>
      <td className="px-3 py-1.5">
        <Checkbox checked={selected} onCheckedChange={(v) => onSelect(v === true)} aria-label={`Select ${e.name}`} />
      </td>
      <td className="px-2 py-1.5">
        <button className="flex items-center gap-2 text-left hover:underline" onClick={onOpen} title={e.dir ? "Open" : "Preview"}>
          {e.dir ? <Folder className="size-4 text-status-starting" /> : <File className="size-4 text-muted-foreground" />}
          {e.name}
        </button>
      </td>
      <td className="px-2 py-1.5 text-right tabular-nums text-muted-foreground">
        {e.dir ? (
          usage.data && !usage.data.inProgress ? (
            <span title={`${usage.data.files} files`}>{formatBytes(usage.data.bytes)}</span>
          ) : askSize ? (
            <Loader2 className="ml-auto size-3.5 animate-spin" />
          ) : (
            <button className="text-xs hover:underline" onClick={() => setAskSize(true)}>
              size?
            </button>
          )
        ) : (
          formatBytes(e.sizeBytes)
        )}
      </td>
      <td className="px-2 py-1.5 text-xs text-muted-foreground">{e.modifiedUnixMs > 0n ? ago(Number(e.modifiedUnixMs)) : ""}</td>
      <td className="max-w-64 px-3 py-1.5 text-xs text-muted-foreground">
        {where.length > 0 && (
          <Tooltip open={new URLSearchParams(window.location.search).get("where") === e.name ? true : undefined}>
            <TooltipTrigger asChild>
              <span className="block cursor-default truncate">{where.length === 1 ? where[0].drives.join(", ") || where[0].node : where.map((w) => `${w.node}: ${w.drives.join(", ") || "its Pool"}`).join(" • ")}</span>
            </TooltipTrigger>
            <TooltipContent side="left" className="max-w-[90vw] p-0">
              <WhereBreakdown where={where} />
            </TooltipContent>
          </Tooltip>
        )}
      </td>
    </tr>
  );
}

interface Where {
  node: string;
  drives: string[];
}

/** An entry's copies grouped by Node, Nodes in name order, Drives in name order. */
function whereOf(e: Entry, nodeName: (id: string) => string, driveName: (n: string, d: string) => string): Where[] {
  const byNode = new Map<string, Set<string>>();
  for (const l of e.locations) {
    const set = byNode.get(l.nodeId) ?? new Set<string>();
    if (l.driveId) set.add(driveName(l.nodeId, l.driveId));
    byNode.set(l.nodeId, set);
  }
  return [...byNode.entries()].map(([id, drives]) => ({ node: nodeName(id), drives: [...drives].sort((a, b) => a.localeCompare(b)) })).sort((a, b) => a.node.localeCompare(b.node));
}

/**
 * The tooltip's breakdown: one row per Node with its Drives as chips that
 * wrap, a line between the Nodes and their Drives. When it would grow too
 * tall it adds columns instead: Nodes are packed into columns of about
 * sixteen lines each, so ten Nodes with forty-two Drives each read as a
 * few columns side by side rather than a scroll.
 */
function WhereBreakdown({ where }: { where: Where[] }) {
  const total = where.reduce((n, w) => n + Math.max(1, w.drives.length), 0);
  const lines = (w: Where) => Math.max(1, Math.ceil(w.drives.length / 6));
  const columns: Where[][] = [[]];
  let used = 0;
  for (const w of where) {
    if (used > 0 && used + lines(w) > 16) {
      columns.push([]);
      used = 0;
    }
    columns[columns.length - 1].push(w);
    used += lines(w);
  }
  return (
    <div className="text-xs">
      <div className="border-b border-primary-foreground/30 px-3 py-1.5 font-medium">
        {total} {total === 1 ? "copy" : "copies"} on {where.length} {where.length === 1 ? "Node" : "Nodes"}
      </div>
      <div className="flex divide-x divide-primary-foreground/30">
        {columns.map((col, ci) => (
          <dl key={ci} className="grid max-w-72 grid-cols-[auto_1fr] py-1">
            {col.map((w) => (
              <div key={w.node} className="contents">
                <dt className="whitespace-nowrap border-r border-primary-foreground/40 py-0.5 pl-3 pr-2 font-medium">
                  {w.node}
                  <span className="ml-1 opacity-70">{w.drives.length || ""}</span>
                </dt>
                <dd className="flex flex-wrap gap-1 py-0.5 pl-2 pr-3">{w.drives.length === 0 ? <span className="opacity-70">its Pool</span> : w.drives.map((d) => <span key={d} className="rounded bg-background/20 px-1.5 leading-5">{d}</span>)}</dd>
              </div>
            ))}
          </dl>
        ))}
      </div>
    </div>
  );
}

/** Decision 186: deleting is for good, so it lists what goes and needs a hold. */
function DeleteDialog({ names, entries, where, onClose, onDelete }: { names: string[] | null; entries: Entry[]; where: string; onClose: () => void; onDelete: (names: string[]) => void }) {
  const items = entries.filter((e) => names?.includes(e.name));
  const files = items.filter((e) => !e.dir);
  const bytes = files.reduce((n, e) => n + e.sizeBytes, 0n);
  const folders = items.length - files.length;
  return (
    <Dialog open={!!names} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Delete {items.length === 1 ? items[0].name : `${items.length} items`}?</DialogTitle>
          <DialogDescription>
            From {where}, on every Drive that holds a copy. This cannot be undone.
          </DialogDescription>
        </DialogHeader>
        <ul className="max-h-48 space-y-0.5 overflow-y-auto rounded-md border p-2 text-sm">
          {items.slice(0, 50).map((e) => (
            <li key={e.name} className="flex items-center gap-2">
              {e.dir ? <Folder className="size-4 shrink-0 text-status-starting" /> : <File className="size-4 shrink-0 text-muted-foreground" />}
              <span className="truncate">{e.name}</span>
              {!e.dir && <span className="ml-auto text-xs text-muted-foreground tabular-nums">{formatBytes(e.sizeBytes)}</span>}
            </li>
          ))}
          {items.length > 50 && <li className="text-xs text-muted-foreground">and {items.length - 50} more</li>}
        </ul>
        <p className="text-xs text-muted-foreground">
          {files.length > 0 && `${files.length} ${files.length === 1 ? "file" : "files"}, ${formatBytes(bytes)}`}
          {files.length > 0 && folders > 0 && " and "}
          {folders > 0 && `${folders} ${folders === 1 ? "folder" : "folders"} with everything in ${folders === 1 ? "it" : "them"}`}.
        </p>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <HoldButton variant="destructive" onConfirm={() => names && onDelete(names)}>
            Hold to delete
          </HoldButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function NewFolderDialog({ open, onClose, where, taken, onMake }: { open: boolean; onClose: () => void; where: string; taken: string[]; onMake: (name: string) => Promise<void> }) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) setName("");
  }, [open]);
  const n = name.trim();
  const problem = !n ? "" : n.includes("/") ? "A name cannot contain /" : n.startsWith(".") ? "A name cannot start with a dot" : taken.includes(n) ? "Something here already has that name" : "";
  async function make() {
    if (!n || problem || busy) return;
    setBusy(true);
    try {
      await onMake(n);
      onClose();
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle>New folder</DialogTitle>
          <DialogDescription>In {where}</DialogDescription>
        </DialogHeader>
        <div className="space-y-1.5">
          <Label htmlFor="new-folder">Name</Label>
          <Input id="new-folder" autoFocus value={name} onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === "Enter" && make()} placeholder="Untitled folder" />
          {problem && <p className="text-xs text-destructive">{problem}</p>}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={!n || !!problem || busy} onClick={make}>
            Make folder
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * Where selected items go: a folder of the same Space, another Space, or the
 * same Space on another Node. The folder is picked by browsing the
 * destination; a Space that does not span Nodes is browsed on the Node
 * chosen, a spanning one through any Node that has it.
 */
function MoveDialog({ open, onClose, snap, scope, path, names, nodeName, listFolders, makeFolder, onMove }: { open: boolean; onClose: () => void; snap: FleetSnapshot | undefined; scope: FileScope; path: string; names: string[]; nodeName: (id: string) => string; listFolders: (nodeId: string, spaceId: string, path: string) => Promise<string[]>; makeFolder: (nodeId: string, spaceId: string, path: string) => Promise<void>; onMove: (req: { toSpace: string; toPath: string; toNodeId: string; toDriveId: string }) => Promise<void> }) {
  // Same order as the file manager's list, so each Space keeps its colour.
  const spaces = [...(snap?.spaces ?? [])].sort((a, b) => b.priority - a.priority);
  const drives = snap?.drives ?? [];
  const nodes = snap?.nodes ?? [];
  // Files on a Drive root move to a Space on their own Node first.
  const ownNodeOnly = !!scope.driveId;
  const holders = (spaceId: string) => nodes.filter((n) => drives.some((d) => d.nodeId === n.nodeId && spacesOf(d, spaces).includes(spaceId))).filter((n) => !ownNodeOnly || n.nodeId === scope.nodeId);
  const spaceChoices = spaces.filter((s) => holders(s.spaceId).length > 0);
  const firstSpace = scope.spaceId ?? spaceChoices.find((s) => s.defaultForNew)?.spaceId ?? spaceChoices[0]?.spaceId ?? "";
  const [toSpace, setToSpace] = useState(firstSpace);
  const [toNode, setToNode] = useState("");
  const [toDrive, setToDrive] = useState("");
  const [toPath, setToPath] = useState(path);
  const [busy, setBusy] = useState(false);
  const space = spaces.find((s) => s.spaceId === toSpace);
  const spaceHolders = holders(toSpace);
  // The Node a Space that does not span is on; "" for a spanning one lets
  // the Space place it.
  const defaultNode = (spaceId: string) => {
    const sp = spaces.find((s) => s.spaceId === spaceId);
    const hs = holders(spaceId);
    if (sp?.spanNodes) return "";
    return hs.some((n) => n.nodeId === scope.nodeId) ? scope.nodeId : (hs[0]?.nodeId ?? "");
  };
  function pickSpace(id: string) {
    setToSpace(id);
    setToNode(defaultNode(id));
    setToDrive("");
    setToPath(id === scope.spaceId ? path : "");
  }
  useEffect(() => {
    if (!open) return;
    setToSpace(firstSpace);
    setToNode(defaultNode(firstSpace));
    setToDrive("");
    setToPath(firstSpace === scope.spaceId ? path : "");
  }, [open]);
  // Browse on the chosen Node, else the one the files are on, else any holder.
  const browseNode = toNode || (spaceHolders.some((n) => n.nodeId === scope.nodeId) ? scope.nodeId : spaceHolders[0]?.nodeId) || "";
  const memberDrives = space ? drives.filter((d) => spacesOf(d, spaces).includes(space.spaceId)) : [];
  const driveChoices = memberDrives.filter((d) => d.nodeId === (toNode || scope.nodeId));
  const crumbs = toPath.split("/").filter(Boolean);
  const sameNode = !toNode || toNode === scope.nodeId;
  const here = toSpace === scope.spaceId && sameNode && !toDrive && toPath === path;
  const into = (n: string) => toSpace === scope.spaceId && sameNode && [path, n].filter(Boolean).join("/");
  const intoItself = names.some((n) => {
    const self = into(n);
    return self && (toPath === self || toPath.startsWith(self + "/"));
  });
  const problem = here ? `${names.length === 1 ? "It is" : "They are"} already here. Pick another folder, Space or Node.` : intoItself ? "A folder cannot move into itself." : "";
  const spaceName = space?.name ?? "";
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Move {names.length === 1 ? names[0] : `${names.length} items`}</DialogTitle>
          <DialogDescription>Copied first, checked, then removed from where it is. A move that fails leaves the original untouched. Within one Space on one Node it is a rename.</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label>Space</Label>
              <Select value={toSpace} onValueChange={pickSpace}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="Space" />
                </SelectTrigger>
                <SelectContent>
                  {spaceChoices.map((s) => (
                    <SelectItem key={s.spaceId} value={s.spaceId}>
                      <span className="mr-1.5 inline-block size-2 rounded-full" style={{ background: spaceColor(spaces.indexOf(s)) }} />
                      {s.name}
                      {s.spaceId === scope.spaceId && <span className="text-muted-foreground"> (this one)</span>}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label>Node</Label>
              <Select value={toNode || "any"} onValueChange={(v) => (setToNode(v === "any" ? "" : v), setToDrive(""))} disabled={ownNodeOnly || (!space?.spanNodes && spaceHolders.length < 2)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {space?.spanNodes && <SelectItem value="any">any Node</SelectItem>}
                  {spaceHolders.map((n) => (
                    <SelectItem key={n.nodeId} value={n.nodeId}>
                      {n.name}
                      {n.nodeId === scope.nodeId && <span className="text-muted-foreground"> (where it is)</span>}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          {ownNodeOnly && <p className="-mt-2 text-xs text-muted-foreground">Files on a Drive move to a Space on {nodeName(scope.nodeId)} first; from there, anywhere.</p>}
          {driveChoices.length > 1 && (
            <div className="space-y-1.5">
              <Label>Drive</Label>
              <Select value={toDrive || "any"} onValueChange={(v) => setToDrive(v === "any" ? "" : v)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="any">the Space decides</SelectItem>
                  {driveChoices.map((d) => (
                    <SelectItem key={d.driveId} value={d.driveId}>
                      {d.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
          <div className="space-y-1.5">
            <Label>Folder</Label>
            <FolderBrowser
              source={{ key: `${browseNode}:${toSpace}`, list: (p) => listFolders(browseNode, toSpace, p), make: (p) => makeFolder(browseNode, toSpace, p) }}
              top={spaceName || "Space"}
              path={toPath}
              onPath={setToPath}
              disabled={(p) => (names.some((n) => into(n) === p) ? "Being moved" : undefined)}
            />
            <p className="text-xs text-muted-foreground">
              Into <span className="font-medium text-foreground">{[spaceName, ...crumbs].join(" / ")}</span>
              {toNode ? ` on ${nodeName(toNode)}` : space?.spanNodes ? ", wherever the Space places it" : ""}
            </p>
          </div>
          {problem && <p className={cn("text-xs", here ? "text-muted-foreground" : "text-destructive")}>{problem}</p>}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={busy || !toSpace || names.length === 0 || !!problem}
            onClick={async () => {
              setBusy(true);
              try {
                await onMove({ toSpace, toPath, toNodeId: toNode, toDriveId: toDrive });
                onClose();
              } catch (e) {
                toast.error(String((e as Error).message ?? e));
              } finally {
                setBusy(false);
              }
            }}
          >
            Move here
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function Jobs({ snap, state, nodeName }: { snap: FleetSnapshot | undefined; state: ConsoleState; nodeName: (id: string) => string }) {
  const qc = useQueryClient();
  const nodes = snap?.nodes ?? [];
  const results = useQueries({
    queries: nodes.map((n) => {
      const t = targetFor(snap, state.nodes, n.nodeId);
      return { queryKey: ["jobs", n.nodeId, t?.addr], enabled: !!t, refetchInterval: 2000, retry: false, queryFn: async () => (await createClient(FilesService, transportFor(t!)).listJobs({})).jobs.map((j) => ({ job: j, nodeId: n.nodeId, target: t! })) };
    }),
  });
  const jobs = results.flatMap((r) => r.data ?? []).sort((a, b) => Number(b.job.startedUnixMs - a.job.startedUnixMs));
  const [open, setOpen] = useState(true);
  // A job that finishes changes the folders: list them again.
  const finished = jobs.filter((j) => j.job.state === "done").length;
  useEffect(() => {
    if (finished > 0) qc.invalidateQueries({ queryKey: ["files"] });
  }, [finished]);
  if (jobs.length === 0) return null;
  const active = jobs.filter((j) => j.job.state === "running" || j.job.state === "queued").length;
  async function cancel(j: Job, t: Target) {
    try {
      await createClient(FilesService, transportFor(t)).cancelJob({ jobId: j.jobId });
      qc.invalidateQueries({ queryKey: ["jobs"] });
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    }
  }
  return (
    <div className="mt-3 rounded-xl border bg-card">
      <button onClick={() => setOpen((o) => !o)} className="flex w-full items-center gap-2 px-3 py-2 text-sm">
        <ChevronRight className={cn("size-4 transition-transform", open && "rotate-90")} />
        <span className="font-medium">Moves and archives</span>
        {active > 0 && <Badge className="bg-status-starting text-white">{active} running</Badge>}
        <span className="text-xs text-muted-foreground">{jobs.length} in all</span>
      </button>
      {open && (
        <ul className="max-h-48 divide-y overflow-y-auto border-t">
          {jobs.slice(0, 20).map(({ job: j, nodeId, target }) => {
            const pct = j.bytesTotal > 0n ? Number((j.bytesDone * 100n) / j.bytesTotal) : 0;
            return (
              <li key={nodeId + j.jobId} className="px-3 py-2 text-xs">
                <div className="flex items-center gap-2">
                  <span className="min-w-0 flex-1 truncate">
                    {j.kind ? (
                      <span className="font-medium text-foreground">{j.summary}</span>
                    ) : (
                      <>
                        <span className="font-medium text-foreground">{j.request?.path}</span> → {snap?.spaces.find((s) => s.spaceId === j.request?.toSpace)?.name ?? j.request?.toSpace}
                        {j.request?.toNodeId && ` on ${nodeName(j.request.toNodeId)}`}
                      </>
                    )}{" "}
                    <span className="text-muted-foreground">· {j.kind ? "on" : "from"} {nodeName(nodeId)}</span>
                  </span>
                  <Badge variant={j.state === "failed" ? "destructive" : j.state === "done" ? "secondary" : "outline"}>{j.state}</Badge>
                  {(j.state === "running" || j.state === "queued") && (
                    <button className="rounded p-0.5 text-muted-foreground hover:text-foreground" title="Cancel" onClick={() => cancel(j, target)}>
                      <X className="size-3.5" />
                    </button>
                  )}
                </div>
                {j.state === "running" && (
                  <div className="mt-1 flex items-center gap-2">
                    <div className="h-1 flex-1 rounded bg-muted">
                      <div className="h-1 rounded bg-status-starting transition-[width]" style={{ width: `${pct}%` }} />
                    </div>
                    <span className="tabular-nums text-muted-foreground">
                      {formatBytes(j.bytesDone)} of {formatBytes(j.bytesTotal)}
                    </span>
                  </div>
                )}
                {j.message && j.state !== "running" && <div className="text-muted-foreground">{j.message}</div>}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
