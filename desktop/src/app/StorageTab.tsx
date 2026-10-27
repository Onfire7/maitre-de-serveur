// The Storage tab (decisions 121-128, docs/ui-needs.md): Storage Spaces,
// the Fleet map, and every Node's Drives with Adopt, Format and Remove.
// Spaces and Drive records come from the anchor; a Node's live Drive list
// is a direct call to it (decision 160).
import { useMemo, useState } from "react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { create, toBinary } from "@bufbuild/protobuf";
import { toast } from "sonner";
import { ChevronRight, HardDrive, Lock, Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { HoldButton } from "@/components/hold-button";
import { AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { formatBytes } from "@/lib/format";
import { StorageService, SpaceSchema, type Drive, type DriveRecord, type Space } from "@/gen/maitre/v1/storage_pb";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { FilesService } from "@/gen/maitre/v1/files_pb";
import { transportFor, type Target } from "@/transport";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { nodeDown, nodeWord } from "@/fleet/useFleet";
import { targetFor } from "@/fleet/actions";
import type { ConsoleState } from "@/state";
import { spacesOf } from "@/spaces";
import { useRoute } from "./routes";

const PLACEMENTS: Record<string, string> = { local_first: "local Drives first", most_free: "most free space", most_free_percent: "highest free share" };

/** One colour per Space, by position (highest priority first); the Fleet map's stripes. */
export function spaceColor(i: number): string {
  const palette = ["oklch(0.62 0.14 250)", "oklch(0.72 0.17 55)", "oklch(0.65 0.15 145)", "oklch(0.65 0.15 320)", "oklch(0.62 0.2 25)", "oklch(0.7 0.1 195)", "oklch(0.8 0.15 95)", "oklch(0.75 0.12 350)"];
  return palette[i % palette.length];
}

export function StorageTab({ snap, state }: { snap: FleetSnapshot | undefined; state: ConsoleState }) {
  const qc = useQueryClient();
  const { go } = useRoute();
  const spaces = useMemo(() => [...(snap?.spaces ?? [])].sort((a, b) => b.priority - a.priority), [snap?.spaces]);
  const drives = snap?.drives ?? [];
  const nodes = snap?.nodes ?? [];
  const [hover, setHover] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<Space | null>(null);
  const [previewTags, setPreviewTags] = useState<string[] | null>(null);
  const nodeName = (id: string) => nodes.find((n) => n.nodeId === id)?.name ?? id.slice(0, 8);
  const members = (s: Space) => drives.filter((d) => spacesOf(d, spaces).includes(s.spaceId));
  const lit = previewTags ? new Set(drives.filter((d) => previewTags.every((x) => [...d.autoTags, ...d.tags].includes(x))).map((d) => d.driveId + d.nodeId)) : hover ? new Set(members(spaces.find((s) => s.spaceId === hover)!).map((d) => d.driveId + d.nodeId)) : null;

  async function put(s: Space, del = false) {
    if (!snap) throw new Error("no Node of the Fleet can be reached");
    await createClient(NodeService, transportFor({ addr: snap.anchor.addr, fingerprint: snap.anchor.nodeId })).putRecord({ record: { kind: "space", id: s.spaceId, body: toBinary(SpaceSchema, s), deleted: del } });
    await qc.invalidateQueries({ queryKey: ["fleet"] });
  }
  const tryPut = (s: Space, del = false) => put(s, del).catch((e) => toast.error(String((e as Error).message ?? e)));
  // Decision 123: exactly one Space is the default; making one so clears the others.
  const makeDefault = async (s: Space) => {
    try {
      for (const other of spaces.filter((o) => o.defaultForNew && o.spaceId !== s.spaceId)) await put(create(SpaceSchema, { ...other, defaultForNew: false }));
      await put(create(SpaceSchema, { ...s, defaultForNew: true }));
      toast.success(`New volumes go to ${s.name}`);
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    }
  };
  /** Member Drives grouped by Node, Nodes by letter, Drives by name. */
  const byNode = (ds: DriveRecord[]): [string, DriveRecord[]][] => {
    const m = new Map<string, DriveRecord[]>();
    for (const d of ds) m.set(d.nodeId, [...(m.get(d.nodeId) ?? []), d]);
    const letter = (id: string) => nodes.find((n) => n.nodeId === id)?.letter ?? "~";
    return [...m.entries()].sort((a, b) => letter(a[0]).localeCompare(letter(b[0]))).map(([id, list]) => [id, [...list].sort((a, b) => a.name.localeCompare(b.name))]);
  };

  // Capacity (decision 125): a Space's unique Drives' free space; Drives shared with other Spaces are shared capacity.
  const capacity = (s: Space) => {
    const mine = members(s);
    const free = mine.reduce((sum, d) => sum + Number(liveFree.get(d.driveId + d.nodeId) ?? 0), 0);
    const shared = mine.some((d) => spacesOf(d, spaces).length > 1);
    return { count: mine.length, free, shared };
  };
  const liveFree = useLiveFree(snap, state);
  const used = useSpaceUsed(snap, state, spaces);

  return (
    <div className="space-y-8">
      <section className="space-y-3">
        <div className="flex items-center justify-between">
          <div>
            <h2 className="text-base font-semibold">Storage Spaces</h2>
            <p className="text-sm text-muted-foreground">A Space is a place for one kind of data: the Drives its rule selects, on any Node. Services put each volume in a Space.</p>
          </div>
          <Button size="sm" onClick={() => setAdding(true)}>
            <Plus /> Add a Space
          </Button>
        </div>
        {spaces.length === 0 && (
          <div className="flex flex-wrap items-center gap-4 rounded-xl border border-status-attention/60 bg-status-attention/10 p-4">
            <div className="min-w-0 flex-1 text-sm">
              <div className="font-medium">Create your first Space</div>
              <div className="text-muted-foreground">
                Services cannot be installed until one exists. "General" takes every Drive{drives.length ? ` (${drives.length} now: ${drives.map((d) => d.name).join(", ")})` : ""}, fills each Node's own Drives first, and becomes the default for new volumes. Make more specific Spaces later.
              </div>
            </div>
            <Button
              onClick={async () => {
                try {
                  await put(create(SpaceSchema, { spaceId: "general", name: "General", placement: "local_first", requireTags: [], spanNodes: false, exclusive: false, defaultForNew: true, priority: 0, createdUnixMs: BigInt(Date.now()) }));
                  toast.success("General created; Services can be installed now");
                } catch (e) {
                  toast.error(String((e as Error).message ?? e));
                }
              }}
            >
              Create General
            </Button>
            <Button variant="outline" onClick={() => setAdding(true)}>
              Choose myself…
            </Button>
          </div>
        )}
        {spaces.length > 0 && !spaces.some((s) => s.defaultForNew) && <p className="rounded-lg border border-status-attention/60 bg-status-attention/10 p-3 text-sm">No Space is marked as the default for new volumes; installing a Service will fail until one is.</p>}
        <ul className="space-y-2">
          {spaces.map((s, i) => {
            const cap = capacity(s);
            return (
              <li key={s.spaceId} className={cn("flex items-start gap-3 rounded-xl border bg-card p-3 transition-colors", hover === s.spaceId && "bg-accent/40")} style={{ borderLeftWidth: 4, borderLeftColor: spaceColor(i) }} onMouseEnter={() => setHover(s.spaceId)} onMouseLeave={() => setHover(null)}>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-semibold">{s.name}</span>
                    {s.defaultForNew && <Badge>default for new volumes</Badge>}
                    {s.exclusive && (
                      <Badge variant="outline">
                        <Lock className="size-3" /> exclusive
                      </Badge>
                    )}
                    {s.spanNodes && <Badge variant="outline">spans Nodes</Badge>}
                  </div>
                  <div className="text-xs text-muted-foreground">
                    {s.requireTags.length ? `Drives tagged ${s.requireTags.join(" + ")}` : "every Drive"}
                    {s.nodeIds.length ? ` on ${s.nodeIds.map(nodeName).join(", ")}` : ""} · {PLACEMENTS[s.placement] ?? s.placement}
                  </div>
                  {cap.count === 0 ? (
                    <div className="mt-1 text-sm text-muted-foreground">no Drives yet</div>
                  ) : (
                    <details className="group mt-1 text-sm">
                      <summary className="flex cursor-pointer list-none items-center gap-1 [&::-webkit-details-marker]:hidden">
                        <ChevronRight className="size-3.5 text-muted-foreground transition-transform group-open:rotate-90" />
                        {cap.count} {cap.count === 1 ? "Drive" : "Drives"} on {byNode(members(s)).length} {byNode(members(s)).length === 1 ? "Node" : "Nodes"}
                        <span className="text-muted-foreground">
                          {" · "}
                          {used.get(s.spaceId) === undefined ? "used: counting…" : `${formatBytes(used.get(s.spaceId)!)} used`}
                          {" · "}
                          {formatBytes(cap.free)} free{cap.shared ? ", shared with other Spaces" : ""}
                        </span>
                      </summary>
                      <ul className="ml-5 mt-1 space-y-0.5 text-sm">
                        {byNode(members(s)).map(([nid, ds]) => (
                          <li key={nid}>
                            <span className="font-medium">{nodeName(nid)}:</span> {ds.map((d) => d.name).join(", ")}
                          </li>
                        ))}
                      </ul>
                    </details>
                  )}
                  <div className="mt-2 flex flex-wrap items-center gap-4 text-xs">
                    <label className="flex items-center gap-1.5">
                      <Switch checked={s.spanNodes} onCheckedChange={(v) => tryPut(create(SpaceSchema, { ...s, spanNodes: v }))} /> spans Nodes
                    </label>
                    {!s.defaultForNew && (
                      <Button size="sm" variant="outline" className="h-7 text-xs" onClick={() => makeDefault(s)}>
                        Make default for new volumes
                      </Button>
                    )}
                    <Select value={s.placement} onValueChange={(v) => tryPut(create(SpaceSchema, { ...s, placement: v }))}>
                      <SelectTrigger size="sm" className="h-7 text-xs">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {Object.entries(PLACEMENTS).map(([k, v]) => (
                          <SelectItem key={k} value={k}>
                            new files go to {v}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                </div>
                <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" onClick={() => setRemoving(s)}>
                  <Trash2 /> Remove
                </Button>
              </li>
            );
          })}
        </ul>
      </section>

      <section className="space-y-3">
        <h2 className="text-base font-semibold">Fleet map</h2>
        <FleetMap snap={snap} spaces={spaces} lit={lit} />
      </section>

      <section className="space-y-3">
        <h2 className="text-base font-semibold">Drives</h2>
        {nodes.map((n) => (
          <NodeDrives key={n.nodeId} nodeId={n.nodeId} name={n.name} target={targetFor(snap, state.nodes, n.nodeId)} unreachable={nodeDown(nodeWord(snap?.status.get(n.nodeId), Date.now()))} spaces={spaces} />
        ))}
      </section>

      <AddSpaceDialog open={adding} onClose={() => (setAdding(false), setPreviewTags(null))} spaces={spaces} drives={drives} nodeName={nodeName} onPreview={setPreviewTags} onAdd={(s) => put(s)} />
      <AlertDialog open={!!removing} onOpenChange={(o) => !o && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove the {removing?.name} Space?</AlertDialogTitle>
            <AlertDialogDescription>Its folders stay on the Drives, shown as folders of a removed Space in Files; nothing is deleted. Services with a volume in it cannot start until the volume is moved.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <HoldButton
              variant="destructive"
              onConfirm={() => {
                if (removing) {
                  const gone = removing;
                  const holder = drives.find((d) => spacesOf(d, spaces).includes(gone.spaceId));
                  put(gone, true)
                    .then(() =>
                      toast(`${gone.name} was removed`, {
                        description: "Its folders stay on the Drives as folders of a removed Space.",
                        action: holder ? { label: "Open in Files", onClick: () => go({ name: "fleet", tab: "files", files: { nodeId: holder.nodeId, driveId: holder.driveId, path: gone.spaceId } }) } : undefined,
                      }),
                    )
                    .catch((e) => toast.error(String((e as Error).message ?? e)));
                }
                setRemoving(null);
              }}
            >
              Hold to remove
            </HoldButton>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

/** Free bytes per Drive from each reachable Node's live Drive list. */
/**
 * Used bytes per Space (decision 125): the size of the Space's folders,
 * counted by the file manager's Usage on the Nodes that hold it. A spanning
 * Space's merged Pool already includes its peers, so it is counted once, on
 * the anchor if it holds it, else on its first reachable holder; any other
 * Space is counted on each holder and summed. Usage is computed in the
 * background and cached by the Node, so the first answer may be partial.
 */
function useSpaceUsed(snap: FleetSnapshot | undefined, state: ConsoleState, spaces: Space[]) {
  const nodes = snap?.nodes ?? [];
  const drives = snap?.drives ?? [];
  const asks: { spaceId: string; nodeId: string }[] = [];
  for (const s of spaces) {
    const holders = nodes.filter((n) => drives.some((d) => d.nodeId === n.nodeId && spacesOf(d, spaces).includes(s.spaceId)));
    const up = holders.filter((n) => !nodeDown(nodeWord(snap?.status.get(n.nodeId), Date.now())));
    if (s.spanNodes) {
      const via = up.find((n) => n.nodeId === snap?.anchor.nodeId) ?? up[0];
      if (via) asks.push({ spaceId: s.spaceId, nodeId: via.nodeId });
    } else for (const n of up) asks.push({ spaceId: s.spaceId, nodeId: n.nodeId });
  }
  const results = useQueries({
    queries: asks.map((a) => {
      const t = targetFor(snap, state.nodes, a.nodeId);
      return {
        queryKey: ["space-used", a.spaceId, a.nodeId, t?.addr],
        enabled: !!t,
        retry: false,
        refetchInterval: (q: { state: { data?: { inProgress: boolean } } }) => (q.state.data?.inProgress ? 3000 : 60_000),
        queryFn: () => createClient(FilesService, transportFor(t!)).usage({ scope: { spaceId: a.spaceId, driveId: "" }, path: "" }),
      };
    }),
  });
  const out = new Map<string, bigint>();
  asks.forEach((a, i) => {
    const r = results[i]?.data;
    if (!r || r.inProgress) return;
    out.set(a.spaceId, (out.get(a.spaceId) ?? 0n) + r.bytes);
  });
  // A Space counts as known only once every holder answered.
  for (const s of spaces) {
    const mine = asks.map((a, i) => ({ a, r: results[i]?.data })).filter((x) => x.a.spaceId === s.spaceId);
    if (mine.some((x) => !x.r || x.r.inProgress)) out.delete(s.spaceId);
  }
  return out;
}

function useLiveFree(snap: FleetSnapshot | undefined, state: ConsoleState) {
  const nodes = snap?.nodes ?? [];
  const q = useQuery({
    queryKey: ["drives-free", nodes.map((n) => n.nodeId).join(",")],
    enabled: nodes.length > 0,
    refetchInterval: 10_000,
    queryFn: async () => {
      const out = new Map<string, bigint>();
      await Promise.all(
        nodes.map(async (n) => {
          const t = targetFor(snap, state.nodes, n.nodeId);
          if (!t) return;
          try {
            for (const d of (await createClient(StorageService, transportFor(t)).listDrives({})).drives) if (d.adopted) out.set(d.driveId + n.nodeId, d.freeBytes);
          } catch {
            /* unreachable */
          }
        }),
      );
      return out;
    },
  });
  return q.data ?? new Map<string, bigint>();
}

function FleetMap({ snap, spaces, lit }: { snap: FleetSnapshot | undefined; spaces: Space[]; lit: Set<string> | null }) {
  const nodes = [...(snap?.nodes ?? [])].sort((a, b) => a.letter.localeCompare(b.letter));
  const drives = snap?.drives ?? [];
  return (
    <div className="flex flex-wrap gap-3">
      {nodes.map((n) => {
        const mine = drives.filter((d) => d.nodeId === n.nodeId).sort((a, b) => a.name.localeCompare(b.name));
        return (
          <div key={n.nodeId} className="min-w-44 rounded-2xl border-2 bg-card p-3">
            <div className="mb-2 flex items-center gap-2">
              <span className="inline-flex size-6 items-center justify-center rounded-full bg-primary text-xs font-bold text-primary-foreground">{n.letter}</span>
              <span className="font-semibold">{n.name}</span>
            </div>
            <div className="flex flex-wrap gap-2">
              {mine.length === 0 && <span className="py-1 text-xs text-muted-foreground">no Drives</span>}
              {mine.map((d) => {
                const on = lit === null || lit.has(d.driveId + d.nodeId);
                const idx = spacesOf(d, spaces)
                  .map((id) => spaces.findIndex((s) => s.spaceId === id))
                  .filter((i) => i >= 0);
                const colors = idx.map(spaceColor);
                const exclusive = idx.some((i) => spaces[i].exclusive);
                return (
                  <Tooltip key={d.driveId}>
                    <TooltipTrigger asChild>
                      <div className={cn("relative min-w-24 rounded-xl border-2 px-2.5 py-1.5 text-xs transition-opacity", !on && "opacity-30")} style={{ borderColor: colors[0] ?? "var(--border)", background: colors[0] ? `color-mix(in oklch, ${colors[0]} 12%, transparent)` : undefined }}>
                        {colors.length > 1 && <div className="absolute inset-x-1 top-0 h-1 rounded-b" style={{ background: `linear-gradient(90deg, ${colors.map((c, k) => `${c} ${(k * 100) / colors.length}% ${((k + 1) * 100) / colors.length}%`).join(", ")})` }} />}
                        <div className="flex items-center gap-1 font-semibold">
                          {d.name}
                          {exclusive && <Lock className="size-3 text-muted-foreground" />}
                        </div>
                        <div className="text-muted-foreground">{formatBytes(d.sizeBytes)}</div>
                      </div>
                    </TooltipTrigger>
                    <TooltipContent>
                      {d.model} · {[...d.autoTags, ...d.tags].join(", ") || "no tags"} · {idx.length ? idx.map((i) => spaces[i].name).join(", ") : "no Space"}
                    </TooltipContent>
                  </Tooltip>
                );
              })}
            </div>
          </div>
        );
      })}
    </div>
  );
}

function AddSpaceDialog({ open, onClose, spaces, drives, nodeName, onPreview, onAdd }: { open: boolean; onClose: () => void; spaces: Space[]; drives: DriveRecord[]; nodeName: (id: string) => string; onPreview: (tags: string[] | null) => void; onAdd: (s: Space) => Promise<void> }) {
  const [name, setName] = useState("");
  const [tags, setTags] = useState("");
  const [placement, setPlacement] = useState("local_first");
  const [spanning, setSpanning] = useState(false);
  const [exclusive, setExclusive] = useState(false);
  const [busy, setBusy] = useState(false);
  const tagList = tags.split(/[,\s]+/).filter(Boolean);
  const preview = drives.filter((d) => tagList.every((x) => [...d.autoTags, ...d.tags].includes(x)));
  const allTags = Array.from(new Set(drives.flatMap((d) => [...d.autoTags, ...d.tags]))).sort();
  const reset = () => (setName(""), setTags(""), setPlacement("local_first"), setSpanning(false), setExclusive(false));
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o) {
          onClose();
          reset();
        }
      }}
    >
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Add a Storage Space</DialogTitle>
          <DialogDescription>Give it a name and say which Drives it takes.</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="space-name">Name</Label>
            <Input id="space-name" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="Media" />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="space-tags">Drives tagged</Label>
            <Input
              id="space-tags"
              value={tags}
              onChange={(e) => {
                setTags(e.target.value);
                onPreview(e.target.value.split(/[,\s]+/).filter(Boolean));
              }}
              placeholder="empty = every Drive"
            />
            {allTags.length > 0 && (
              <div className="flex flex-wrap gap-1">
                {allTags.map((t) => (
                  <button
                    key={t}
                    type="button"
                    className={cn("rounded-full border px-2 py-0.5 text-xs", tagList.includes(t) ? "border-primary bg-primary text-primary-foreground" : "hover:bg-accent")}
                    onClick={() => {
                      const next = tagList.includes(t) ? tagList.filter((x) => x !== t) : [...tagList, t];
                      setTags(next.join(", "));
                      onPreview(next);
                    }}
                  >
                    {t}
                  </button>
                ))}
              </div>
            )}
            <p className="text-xs text-muted-foreground">Would take: {preview.length === 0 ? "no Drive matches" : preview.map((d) => `${d.name} on ${nodeName(d.nodeId)}`).join(", ")}</p>
          </div>
          <div className="space-y-1.5">
            <Label>New files go to</Label>
            <Select value={placement} onValueChange={setPlacement}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {Object.entries(PLACEMENTS).map(([k, v]) => (
                  <SelectItem key={k} value={k}>
                    {v}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <label className="flex items-center justify-between text-sm">
            <span>
              Spans Nodes
              <span className="block text-xs text-muted-foreground">One Pool across every Node that has a Drive of this Space, over the network.</span>
            </span>
            <Switch checked={spanning} onCheckedChange={setSpanning} />
          </label>
          <label className="flex items-center justify-between text-sm">
            <span>
              Exclusive
              <span className="block text-xs text-muted-foreground">Its Drives belong to this Space only. Needs a tag rule.</span>
            </span>
            <Switch checked={exclusive} onCheckedChange={setExclusive} />
          </label>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => (onClose(), reset())}>
            Cancel
          </Button>
          <Button
            disabled={busy || !name.trim() || (exclusive && tagList.length === 0)}
            onClick={async () => {
              setBusy(true);
              const id = name.trim().toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
              try {
                await onAdd(create(SpaceSchema, { spaceId: id, name: name.trim(), placement, requireTags: tagList, spanNodes: spanning, exclusive, defaultForNew: spaces.length === 0, priority: spaces.length, createdUnixMs: BigInt(Date.now()) }));
                toast.success(`${name.trim()} added${spaces.length === 0 ? " and marked as the default" : ""}`);
                onClose();
                reset();
              } catch (e) {
                toast.error(String((e as Error).message ?? e));
              } finally {
                setBusy(false);
              }
            }}
          >
            Add
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function NodeDrives({ nodeId, name, target, unreachable, spaces }: { nodeId: string; name: string; target: Target | undefined; unreachable: boolean; spaces: Space[] }) {
  const qc = useQueryClient();
  const { go } = useRoute();
  const client = useMemo(() => (target ? createClient(StorageService, transportFor(target)) : undefined), [target?.addr, target?.fingerprint]);
  const q = useQuery({
    queryKey: ["drives", nodeId, target?.addr],
    enabled: !!client && !unreachable,
    refetchInterval: 5000,
    retry: false,
    queryFn: async () => (await client!.listDrives({})).drives.filter((d) => !d.system),
  });
  const [dialog, setDialog] = useState<{ kind: "adopt" | "format" | "forget"; drive: Drive } | null>(null);
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["drives", nodeId] });
    qc.invalidateQueries({ queryKey: ["fleet"] });
    qc.invalidateQueries({ queryKey: ["drives-free"] });
  };
  return (
    <div className="rounded-xl border bg-card">
      <div className="flex items-center gap-2 border-b px-4 py-2.5">
        <span className="font-semibold">{name}</span>
        {unreachable && <Badge variant="outline">unreachable</Badge>}
        {q.error && <span className="text-xs text-destructive">{String((q.error as Error).message ?? q.error)}</span>}
      </div>
      <ul className="divide-y">
        {unreachable && <li className="px-4 py-3 text-sm text-muted-foreground">Its Drives show when it is back.</li>}
        {!unreachable && q.data?.length === 0 && <li className="px-4 py-3 text-sm text-muted-foreground">No disks besides the system disk. Plug one in and it appears here.</li>}
        {(q.data ?? []).map((d) => {
          const idx = d.record ? spacesOf(d.record, spaces).map((id) => spaces.findIndex((s) => s.spaceId === id)) : [];
          return (
            <li key={d.device} className="flex items-center gap-3 px-4 py-2.5">
              <HardDrive className={cn("size-5 shrink-0", d.adopted ? "text-foreground" : "text-muted-foreground")} />
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2 text-sm">
                  <span className="font-medium">{d.record?.name || d.label || d.model}</span>
                  {d.adopted ? idx.length ? idx.map((i) => (
                        <Badge key={i} variant="secondary">
                          <span className="size-2 rounded-full" style={{ background: spaceColor(i) }} /> {spaces[i].name}
                        </Badge>
                      )) : <Badge variant="outline">no Space</Badge> : <Badge variant="outline">not used</Badge>}
                  {d.record?.origin === "internal" && <span className="text-xs text-muted-foreground">built in</span>}
                </div>
                <div className="truncate text-xs text-muted-foreground">
                  {d.model} · {formatBytes(d.sizeBytes)} · {d.fstype || "no filesystem"} · {d.device}
                  {d.adopted && d.freeBytes > 0n && ` · ${formatBytes(d.freeBytes)} free`}
                  {[...d.autoTags, ...(d.record?.tags ?? [])].length > 0 && ` · ${[...d.autoTags, ...(d.record?.tags ?? [])].join(", ")}`}
                </div>
                {d.folders.some((f) => f.kind !== "space") && (
                  <div className="flex flex-wrap gap-1 pt-0.5 text-xs">
                    {d.folders
                      .filter((f) => f.kind !== "space")
                      .map((f) => (
                        <button key={f.name} className="rounded-full border border-status-attention/60 bg-status-attention/10 px-2 py-0.5 hover:bg-status-attention/20" title="Open in Files" onClick={() => go({ name: "fleet", tab: "files", files: { nodeId, driveId: d.driveId, path: f.name } })}>
                          {f.name} · {f.kind === "orphan" ? "removed Space" : "unsorted"}
                        </button>
                      ))}
                  </div>
                )}
              </div>
              <div className="flex gap-1">
                {!d.adopted && d.fstype && (
                  <Button size="sm" variant="outline" onClick={() => setDialog({ kind: "adopt", drive: d })}>
                    Adopt
                  </Button>
                )}
                {!d.adopted && (
                  <Button size="sm" variant="outline" onClick={() => setDialog({ kind: "format", drive: d })}>
                    Format
                  </Button>
                )}
                {d.adopted && d.record?.origin !== "internal" && (
                  <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" onClick={() => setDialog({ kind: "forget", drive: d })}>
                    Remove
                  </Button>
                )}
              </div>
            </li>
          );
        })}
      </ul>
      {client && dialog && <DriveDialog kind={dialog.kind} drive={dialog.drive} client={client} onDone={() => (setDialog(null), refresh())} />}
    </div>
  );
}

function DriveDialog({ kind, drive, client, onDone }: { kind: "adopt" | "format" | "forget"; drive: Drive; client: ReturnType<typeof createClient<typeof StorageService>>; onDone: () => void }) {
  const [name, setName] = useState(drive.record?.name || drive.suggestedName || drive.label || drive.model);
  const [tags, setTags] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const expected = kind === "format" ? (drive.model.endsWith(" disk") && !drive.serial ? drive.device : drive.model) : (drive.record?.name ?? "");
  const contents = useQuery({ queryKey: ["drive-contents", drive.device], enabled: kind !== "forget" && !!drive.fstype, queryFn: () => client.driveContents({ device: drive.device }) });
  async function go() {
    setBusy(true);
    try {
      const tagList = tags.split(/[,\s]+/).filter(Boolean);
      if (kind === "adopt") await client.adoptDrive({ device: drive.device, name, tags: tagList });
      if (kind === "format") await client.formatDrive({ device: drive.device, name, tags: tagList, confirm });
      if (kind === "forget") await client.forgetDrive({ driveId: drive.driveId, confirm });
      toast.success(kind === "adopt" ? `${name} adopted` : kind === "format" ? `${name} formatted` : `${drive.record?.name} removed from its Spaces`);
      onDone();
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    } finally {
      setBusy(false);
    }
  }
  const guarded = kind !== "adopt";
  return (
    <Dialog open onOpenChange={(o) => !o && onDone()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {kind === "adopt" && `Adopt ${drive.model}`}
            {kind === "format" && `Format ${drive.model}`}
            {kind === "forget" && `Remove ${drive.record?.name}`}
          </DialogTitle>
          <DialogDescription>
            {kind === "adopt" && "The disk becomes a Drive of this Node with everything on it kept. Its files show up in Files as unsorted."}
            {kind === "format" && "Everything on this disk will be erased. It becomes a Drive of this Node."}
            {kind === "forget" && "The Drive leaves its Spaces. Files on it stay on the disk, but Services can no longer reach them until it is adopted again."}
          </DialogDescription>
        </DialogHeader>
        {contents.data && kind !== "forget" && (
          <div className="rounded-md bg-muted p-3 text-sm">
            {contents.data.empty ? "The disk is empty." : `On the disk now (${formatBytes(contents.data.usedBytes)} used):`}
            {!contents.data.empty && (
              <ul className="mt-1 list-inside list-disc text-xs text-muted-foreground">
                {contents.data.entries.slice(0, 12).map((e) => (
                  <li key={e}>{e}</li>
                ))}
                {contents.data.entries.length > 12 && <li>and {contents.data.entries.length - 12} more</li>}
              </ul>
            )}
          </div>
        )}
        {kind !== "forget" && (
          <div className="space-y-3">
            <div className="space-y-1.5">
              <Label>Name</Label>
              <Input value={name} onChange={(e) => setName(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label>Tags</Label>
              <Input value={tags} onChange={(e) => setTags(e.target.value)} placeholder="media, backups" />
              <p className="text-xs text-muted-foreground">Spaces pick Drives by tag. Automatic tags: {drive.autoTags.join(", ") || "none"}.</p>
            </div>
          </div>
        )}
        {guarded && (
          <div className="space-y-1.5">
            <Label>
              Type <code className="rounded bg-muted px-1">{expected}</code> to confirm
            </Label>
            <Input value={confirm} onChange={(e) => setConfirm(e.target.value)} />
          </div>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onDone}>
            Cancel
          </Button>
          <Button variant={kind === "adopt" ? "default" : "destructive"} disabled={busy || (guarded && confirm.trim() !== expected)} onClick={go}>
            {kind === "adopt" ? "Adopt" : kind === "format" ? "Erase and format" : "Remove"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
