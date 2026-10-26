// The Files tab (decision 122, docs/ui-needs.md): browse a Space's Pool on a
// Node or a Drive's root, see where each entry lives and how big it is, and
// queue moves between Spaces, Drives and Nodes. Every call goes straight to
// the Node that holds the files (160); jobs live on that Node.
import { useEffect, useMemo, useState } from "react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { toast } from "sonner";
import { Folder, File, ChevronRight, HardDrive, Layers, MoveRight, X, Loader2, RefreshCw, Boxes } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { formatBytes, ago } from "@/lib/format";
import { FilesService, type Entry, type Job } from "@/gen/maitre/v1/files_pb";
import { transportFor, type Target } from "@/transport";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { nodeWord } from "@/fleet/useFleet";
import { targetFor } from "@/fleet/actions";
import type { ConsoleState } from "@/state";
import { spacesOf } from "@/spaces";
import { spaceColor } from "./StorageTab";

export interface FileScope {
  nodeId: string;
  spaceId?: string;
  driveId?: string;
  path?: string;
}

const scopeKey = (s: FileScope) => `${s.nodeId}:${s.spaceId ?? ""}:${s.driveId ?? ""}`;
const sameScope = (a: FileScope, b: FileScope & { group?: string }) => (b.group === "fleet" ? a.spaceId === b.spaceId && !a.driveId : scopeKey(a) === scopeKey(b));

export function FilesTab({ snap, state, initial }: { snap: FleetSnapshot | undefined; state: ConsoleState; initial?: FileScope }) {
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
    const reachable = (id: string) => nodeWord(snap?.status.get(id), Date.now()) !== "Unreachable";
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
  const [scope, setScope] = useState<FileScope | undefined>(initial);
  const [path, setPath] = useState(initial?.path ?? "");
  useEffect(() => {
    if (!scope && scopes[0]) setScope(scopes[0]);
  }, [scopes, scope]);
  useEffect(() => {
    if (initial) {
      setScope(initial);
      setPath(initial.path ?? "");
    }
  }, [initial]);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [moving, setMoving] = useState(false);
  const target = scope ? targetFor(snap, state.nodes, scope.nodeId) : undefined;
  const client = useMemo(() => (target ? createClient(FilesService, transportFor(target)) : undefined), [target?.addr, target?.fingerprint]);
  const unreachable = scope ? nodeWord(snap?.status.get(scope.nodeId), Date.now()) === "Unreachable" : false;
  const list = useQuery({
    queryKey: ["files", scope && scopeKey(scope), path, target?.addr],
    enabled: !!client && !!scope && !unreachable,
    retry: false,
    queryFn: async () => (await client!.listDir({ scope: { spaceId: scope!.spaceId ?? "", driveId: scope!.driveId ?? "" }, path })).entries,
  });
  const entries = useMemo(() => [...(list.data ?? [])].sort((a, b) => Number(b.dir) - Number(a.dir) || a.name.localeCompare(b.name)), [list.data]);
  useEffect(() => setSelected(new Set()), [scope && scopeKey(scope), path]);
  const nodeName = (id: string) => nodes.find((n) => n.nodeId === id)?.name ?? id.slice(0, 8);
  const driveName = (nid: string, did: string) => drives.find((d) => d.nodeId === nid && d.driveId === did)?.name ?? did;
  const crumbs = path.split("/").filter(Boolean);
  const current = scope ? scopes.find((s) => sameScope(scope, s)) : undefined;
  const go = (p: string) => {
    setPath(p);
  };

  return (
    <div className="grid h-full grid-cols-[220px_1fr] gap-6">
      <aside className="space-y-4 overflow-y-auto pr-2">
        {[{ id: "fleet", letter: "", name: state.fleet?.name ?? "Fleet" }, ...nodes.map((n) => ({ id: n.nodeId, letter: n.letter, name: n.name }))].map((n) => (
          <div key={n.id}>
            <div className="mb-1 flex items-center gap-1.5 text-xs font-semibold text-muted-foreground">
              {n.id === "fleet" ? <Boxes className="size-4" /> : <span className="inline-flex size-4 items-center justify-center rounded-full bg-primary text-[10px] text-primary-foreground">{n.letter}</span>} {n.name}
            </div>
            <ul className="space-y-0.5">
              {scopes
                .filter((s) => s.group === n.id)
                .map((s) => (
                  <li key={scopeKey(s)}>
                    <button
                      onClick={() => {
                        setScope(s);
                        setPath("");
                      }}
                      className={cn("flex w-full items-center gap-2 rounded-md px-2 py-1 text-left text-sm hover:bg-accent", scope && sameScope(scope, s) && "bg-accent")}
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
      </aside>

      <div className="flex min-h-0 flex-col">
        <div className="mb-3 flex flex-wrap items-center gap-2">
          <nav className="flex min-w-0 flex-1 items-center gap-1 text-sm">
            {current && (
              <button className="flex items-center gap-1.5 rounded px-1 font-medium hover:bg-accent" onClick={() => go("")}>
                {current.kind === "space" ? <Layers className="size-4" style={{ color: current.color }} /> : <HardDrive className="size-4 text-muted-foreground" />}
                {current.label}
                <span className="text-xs font-normal text-muted-foreground">{current.group === "fleet" ? `spans Nodes · via ${nodeName(current.nodeId)}` : `on ${nodeName(current.nodeId)}`}</span>
              </button>
            )}
            {crumbs.map((c, i) => (
              <span key={i} className="flex items-center gap-1">
                <ChevronRight className="size-3.5 text-muted-foreground" />
                <button className="rounded px-1 hover:bg-accent" onClick={() => go(crumbs.slice(0, i + 1).join("/"))}>
                  {c}
                </button>
              </span>
            ))}
          </nav>
          <Button size="sm" variant="ghost" onClick={() => list.refetch()} title="Refresh">
            <RefreshCw className={cn("size-4", list.isFetching && "animate-spin")} />
          </Button>
          <Button size="sm" disabled={selected.size === 0 || !scope} onClick={() => setMoving(true)}>
            <MoveRight /> Move {selected.size > 0 ? `${selected.size} ${selected.size === 1 ? "item" : "items"}…` : "…"}
          </Button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto rounded-xl border bg-card">
          {!scope ? (
            <p className="p-4 text-sm text-muted-foreground">Create a Storage Space or adopt a Drive first; then there is something to browse.</p>
          ) : unreachable ? (
            <p className="p-4 text-sm text-muted-foreground">{nodeName(scope.nodeId)} is unreachable. Its files show when it is back.</p>
          ) : list.error ? (
            <p className="p-4 text-sm text-destructive">{String((list.error as Error).message ?? list.error)}</p>
          ) : !list.data ? (
            <p className="p-4 text-sm text-muted-foreground">Reading…</p>
          ) : entries.length === 0 ? (
            <p className="p-4 text-sm text-muted-foreground">Empty.</p>
          ) : (
            <table className="w-full text-sm">
              <thead className="sticky top-0 bg-card text-xs text-muted-foreground">
                <tr className="border-b">
                  <th className="w-8 px-3 py-2">
                    <Checkbox checked={selected.size > 0 && selected.size === entries.length ? true : selected.size > 0 ? "indeterminate" : false} onCheckedChange={(v) => setSelected(v === true ? new Set(entries.map((e) => e.name)) : new Set())} aria-label="Select all" />
                  </th>
                  <th className="px-2 py-2 text-left font-medium">Name</th>
                  <th className="px-2 py-2 text-right font-medium">Size</th>
                  <th className="px-2 py-2 text-left font-medium">Changed</th>
                  <th className="px-3 py-2 text-left font-medium">Where</th>
                </tr>
              </thead>
              <tbody>
                {entries.map((e) => (
                  <Row key={e.name} e={e} client={client!} scope={scope} path={path} selected={selected.has(e.name)} onSelect={(on) => setSelected((s) => (on ? new Set([...s, e.name]) : new Set([...s].filter((x) => x !== e.name))))} onOpen={() => e.dir && go([...crumbs, e.name].join("/"))} nodeName={nodeName} driveName={driveName} />
                ))}
              </tbody>
            </table>
          )}
        </div>

        <Jobs snap={snap} state={state} nodeName={nodeName} />
      </div>

      {scope && client && (
        <MoveDialog
          open={moving}
          onClose={() => setMoving(false)}
          snap={snap}
          scope={scope}
          path={path}
          names={[...selected]}
          nodeName={nodeName}
          onMove={async (req) => {
            for (const name of selected) {
              const rel = [...crumbs, name].join("/");
              await client.move({ from: { spaceId: scope.spaceId ?? "", driveId: scope.driveId ?? "" }, path: rel, toSpace: req.toSpace, toPath: req.toPath ? `${req.toPath}/${name}`.replace(/^\/+/, "") : rel, toNodeId: req.toNodeId, toDriveId: req.toDriveId });
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

function Row({ e, client, scope, path, selected, onSelect, onOpen, nodeName, driveName }: { e: Entry; client: ReturnType<typeof createClient<typeof FilesService>>; scope: FileScope; path: string; selected: boolean; onSelect: (on: boolean) => void; onOpen: () => void; nodeName: (id: string) => string; driveName: (n: string, d: string) => string }) {
  const [askSize, setAskSize] = useState(false);
  const usage = useQuery({
    queryKey: ["usage", scopeKey(scope), path, e.name],
    enabled: e.dir && askSize,
    refetchInterval: (q) => (q.state.data?.inProgress ? 1500 : false),
    queryFn: () => client.usage({ scope: { spaceId: scope.spaceId ?? "", driveId: scope.driveId ?? "" }, path: [path, e.name].filter(Boolean).join("/") }),
  });
  const where = whereOf(e, nodeName, driveName);
  return (
    <tr className={cn("border-b last:border-0 hover:bg-accent/40", selected && "bg-accent/60")}>
      <td className="px-3 py-1.5">
        <Checkbox checked={selected} onCheckedChange={(v) => onSelect(v === true)} aria-label={`Select ${e.name}`} />
      </td>
      <td className="px-2 py-1.5">
        <button className={cn("flex items-center gap-2 text-left", e.dir && "hover:underline")} onClick={onOpen} onDoubleClick={onOpen}>
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

function MoveDialog({ open, onClose, snap, scope, path, names, nodeName, onMove }: { open: boolean; onClose: () => void; snap: FleetSnapshot | undefined; scope: FileScope; path: string; names: string[]; nodeName: (id: string) => string; onMove: (req: { toSpace: string; toPath: string; toNodeId: string; toDriveId: string }) => Promise<void> }) {
  const spaces = snap?.spaces ?? [];
  const drives = snap?.drives ?? [];
  const nodes = snap?.nodes ?? [];
  const [toSpace, setToSpace] = useState(spaces.find((s) => s.defaultForNew)?.spaceId ?? spaces[0]?.spaceId ?? "");
  const [toNode, setToNode] = useState("");
  const [toDrive, setToDrive] = useState("");
  const [toPath, setToPath] = useState(path);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) {
      setToPath(path);
      setToNode(scope.driveId ? scope.nodeId : "");
      setToDrive("");
    }
  }, [open, path, scope.driveId, scope.nodeId]);
  // Files on a Drive root move to a Space on their own Node first.
  const ownNodeOnly = !!scope.driveId;
  const space = spaces.find((s) => s.spaceId === toSpace);
  const memberDrives = space ? drives.filter((d) => spacesOf(d, spaces).includes(space.spaceId)) : [];
  const nodeChoices = nodes.filter((n) => memberDrives.some((d) => d.nodeId === n.nodeId));
  const driveChoices = memberDrives.filter((d) => d.nodeId === (toNode || scope.nodeId));
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>
            Move {names.length === 1 ? names[0] : `${names.length} items`}
          </DialogTitle>
          <DialogDescription>Copied first, checked, then removed from where it is. A move that fails leaves the original untouched.</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label>To Space</Label>
            <Select value={toSpace} onValueChange={(v) => (setToSpace(v), setToDrive(""))}>
              <SelectTrigger>
                <SelectValue placeholder="Space" />
              </SelectTrigger>
              <SelectContent>
                {spaces.map((s, i) => (
                  <SelectItem key={s.spaceId} value={s.spaceId}>
                    <span className="mr-1.5 inline-block size-2 rounded-full" style={{ background: spaceColor(i) }} />
                    {s.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <Label>On</Label>
            <Select value={toNode || "any"} onValueChange={(v) => (setToNode(v === "any" ? "" : v), setToDrive(""))} disabled={ownNodeOnly}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="any">wherever the Space places it</SelectItem>
                {nodeChoices.map((n) => (
                  <SelectItem key={n.nodeId} value={n.nodeId}>
                    {n.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {ownNodeOnly && <p className="text-xs text-muted-foreground">Files on a Drive move to a Space on {nodeName(scope.nodeId)} first; from there, anywhere.</p>}
          </div>
          {driveChoices.length > 1 && (
            <div className="space-y-1.5">
              <Label>Drive</Label>
              <Select value={toDrive || "any"} onValueChange={(v) => setToDrive(v === "any" ? "" : v)}>
                <SelectTrigger>
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
            <Label htmlFor="mv-path">Into folder</Label>
            <Input id="mv-path" value={toPath} onChange={(e) => setToPath(e.target.value)} placeholder="top of the Space" className="font-mono text-xs" />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={busy || !toSpace || names.length === 0}
            onClick={async () => {
              setBusy(true);
              try {
                await onMove({ toSpace, toPath: toPath.trim().replace(/^\/+|\/+$/g, ""), toNodeId: toNode, toDriveId: toDrive });
                onClose();
              } catch (e) {
                toast.error(String((e as Error).message ?? e));
              } finally {
                setBusy(false);
              }
            }}
          >
            Move
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
        <span className="font-medium">Moves</span>
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
                    <span className="font-medium text-foreground">{j.request?.path}</span> → {snap?.spaces.find((s) => s.spaceId === j.request?.toSpace)?.name ?? j.request?.toSpace}
                    {j.request?.toNodeId && ` on ${nodeName(j.request.toNodeId)}`} <span className="text-muted-foreground">· from {nodeName(nodeId)}</span>
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
