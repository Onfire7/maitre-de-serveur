// The Storage tab (decisions 121-128, 201, docs/ui-needs.md): Storage
// Spaces, and every Node's Drives with Adopt, Format and Remove.
// Spaces and Drive records come from the anchor; a Node's live Drive list
// is a direct call to it (decision 160).
import { useMemo, useState } from "react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { create, toBinary } from "@bufbuild/protobuf";
import { toast } from "sonner";
import { Check, ChevronRight, HardDrive, Lock, Pencil, Plus, Trash2 } from "lucide-react";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
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
import { StorageService, SpaceSchema, DriveRecordSchema, type Drive, type DriveRecord, type Space } from "@/gen/maitre/v1/storage_pb";
import { FilesService } from "@/gen/maitre/v1/files_pb";
import { transportFor, type Target } from "@/transport";
import type { FleetSnapshot } from "@/fleet/useFleet";
import type { FleetNodeStatus } from "@/gen/maitre/v1/node_pb";
import { nodeDown, nodeWord } from "@/fleet/useFleet";
import { targetFor } from "@/fleet/actions";
import { currentRecord, putSigned } from "@/fleet/sign";
import type { ConsoleState } from "@/state";
import { nextOwnerId } from "@/fleet/owners";
import { spacesOf } from "@/spaces";
import { useRoute } from "./routes";
// ADR 0009: a Space's protection on its card and the Parity Drive switch
// on each Drive row; the Protection tab has the rest.
import { coverage, roleOf } from "@/protection";
import { CopiesControl, LevelControl, MakeParityDialog, ParityOffDialog, ParitySwitch, WordPill } from "./ProtectionControls";

// How a Space fills its Drives: decision 201 adds the least free ones (the
// fullest Drive first); 202 makes local Drives first a switch of its own.
const PLACEMENTS: Record<string, string> = { most_free: "most free space", most_free_percent: "most free share", least_free: "least free space", least_free_percent: "least free share" };

/** A Space's fill and whether it prefers local Drives; "local_first" from before is most free with local preferred. */
export function fillOf(s: Space): { fill: string; preferLocal: boolean } {
  return s.placement === "local_first" ? { fill: "most_free", preferLocal: true } : { fill: s.placement in PLACEMENTS ? s.placement : "most_free", preferLocal: s.preferLocal };
}

/** Writes them back. Most free with local preferred stays "local_first", which older Nodes know. */
export function withFill(s: Space, fill: string, preferLocal: boolean): Space {
  return create(SpaceSchema, { ...s, placement: fill === "most_free" && preferLocal ? "local_first" : fill, preferLocal });
}

/** One colour per Space, by position (highest priority first). */
export function spaceColor(i: number): string {
  const palette = ["oklch(0.62 0.14 250)", "oklch(0.72 0.17 55)", "oklch(0.65 0.15 145)", "oklch(0.65 0.15 320)", "oklch(0.62 0.2 25)", "oklch(0.7 0.1 195)", "oklch(0.8 0.15 95)", "oklch(0.75 0.12 350)"];
  return palette[Math.max(0, i) % palette.length];
}

/** A Space's colour: the one picked for it (204), else by its place among `spaces` (highest priority first). */
export function colorOf(s: Space | undefined, spaces: Space[]): string {
  if (!s) return "var(--border)";
  return s.color || spaceColor(spaces.findIndex((x) => x.spaceId === s.spaceId));
}

/** Colours offered for a Space (204), as #rrggbb, which is what the Node accepts. */
const SWATCHES = ["#3b82f6", "#0ea5e9", "#14b8a6", "#22c55e", "#84cc16", "#eab308", "#f59e0b", "#f97316", "#ef4444", "#ec4899", "#a855f7", "#6366f1", "#78716c", "#64748b"];

export function StorageTab({ snap, state }: { snap: FleetSnapshot | undefined; state: ConsoleState }) {
  const qc = useQueryClient();
  const { go } = useRoute();
  // Colours follow priority, as on the Files tab; the list is by name (201).
  const spaces = useMemo(() => [...(snap?.spaces ?? [])].sort((a, b) => b.priority - a.priority), [snap?.spaces]);
  const listed = useMemo(() => [...spaces].sort((a, b) => a.name.localeCompare(b.name)), [spaces]);
  const [editingTags, setEditingTags] = useState<Space | null>(null);
  const drives = snap?.drives ?? [];
  const nodes = snap?.nodes ?? [];
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<Space | null>(null);
  const nodeName = (id: string) => nodes.find((n) => n.nodeId === id)?.name ?? id.slice(0, 8);
  const members = (s: Space) => drives.filter((d) => spacesOf(d, spaces).includes(s.spaceId));

  async function put(s: Space, del = false) {
    if (!snap) throw new Error("no Node of the Fleet can be reached");
    // Decision 182: every Space the Console writes has an owner.
    if (!s.ownerId && !del) s = create(SpaceSchema, { ...s, ownerId: nextOwnerId(spaces) });
    await putSigned({ addr: snap.anchor.addr, fingerprint: snap.anchor.nodeId }, { kind: "space", id: s.spaceId, body: toBinary(SpaceSchema, s), deleted: del }, currentRecord(snap.records, "space", s.spaceId));
    await qc.invalidateQueries({ queryKey: ["fleet"] });
  }
  // An older anchor refuses a placement it does not know (201).
  const tryPut = (s: Space, del = false) => put(s, del).catch((e) => toast.error(/placement must be local_first, most_free or most_free_percent/.test(String((e as Error).message)) ? `Filling the fullest Drive first needs a newer Maître Server on ${nodeName(snap!.anchor.nodeId)}. Update it on the Nodes tab.` : String((e as Error).message ?? e)));
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
          {listed.map((s) => {
            const cap = capacity(s);
            const color = colorOf(s, spaces);
            return (
              <li key={s.spaceId} className="relative flex items-start gap-3 overflow-hidden rounded-xl border bg-card p-3 pl-5">
                <ColorEdge space={s} color={color} onPick={(c) => tryPut(create(SpaceSchema, { ...s, color: c }))} />
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-semibold">{s.name}</span>
                    {s.requireTags.length === 0 && s.nodeIds.length === 0 && (
                      <Badge variant="outline" className="text-muted-foreground" title={s.mirrored ? "No tag rule: it takes every Mirrored Drive" : "No tag rule: it takes every plain Drive"}>
                        {s.mirrored ? "any Mirrored Drive" : "any Drive"}
                      </Badge>
                    )}
                    {s.mirrored && (
                      <Badge variant="outline" title="Only Mirrored Drives, which belong to no plain Space (decision 255)">
                        Mirrored Drives only
                      </Badge>
                    )}
                    {s.requireTags.map((t) => (
                      <Badge key={t} variant="secondary" title={`Takes Drives tagged ${t}`}>
                        {t}
                      </Badge>
                    ))}
                    {s.nodeIds.length > 0 && <Badge variant="outline">on {s.nodeIds.map(nodeName).join(", ")}</Badge>}
                    {s.exclusive && (
                      <Badge variant="outline" title="Its Drives belong to this Space only">
                        <Lock className="size-3" /> exclusive
                      </Badge>
                    )}
                    <Button size="icon" variant="ghost" className="size-6" aria-label={`Change the tags ${s.name} takes`} title="Change which tags it takes" onClick={() => setEditingTags(s)}>
                      <Pencil className="size-3.5" />
                    </Button>
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
                    <RadioGroup value={s.defaultForNew ? s.spaceId : ""} onValueChange={() => makeDefault(s)} className="flex">
                      <label className="flex items-center gap-1.5" title="New Services put their volumes here unless told otherwise. One Space is the default.">
                        <RadioGroupItem value={s.spaceId} aria-label={`${s.name} is the default for new volumes`} /> Default
                      </label>
                    </RadioGroup>
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <label className="flex items-center gap-1.5">
                          <Switch checked={s.spanNodes} onCheckedChange={(v) => tryPut(create(SpaceSchema, { ...s, spanNodes: v }))} /> Spans Nodes
                        </label>
                      </TooltipTrigger>
                      <TooltipContent className="max-w-xs">On: one Pool across every Node with a Drive of this Space, shared over the network, so every Node sees the same files. Off: each Node has its own Pool of its own Drives.</TooltipContent>
                    </Tooltip>
                    <label className="flex items-center gap-1.5">
                      New files go to
                      <Select value={fillOf(s).fill} onValueChange={(v) => tryPut(withFill(s, v, fillOf(s).preferLocal))}>
                        <SelectTrigger size="sm" className="h-7 text-xs" aria-label={`Where new files in ${s.name} go`}>
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
                    </label>
                    {s.spanNodes && (
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <label className="flex items-center gap-1.5">
                            <Switch checked={fillOf(s).preferLocal} onCheckedChange={(v) => tryPut(withFill(s, fillOf(s).fill, v))} aria-label={`${s.name} prefers local Drives`} /> Prefer local Drives
                          </label>
                        </TooltipTrigger>
                        <TooltipContent className="max-w-xs">On: a Node puts new files on its own Drives of this Space, in the order below, and on other Nodes' only once its own are full. Faster, and no network needed. Off: every Node's Drives are filled in the order below, wherever they are.</TooltipContent>
                      </Tooltip>
                    )}
                  </div>
                  <ProtectionRow space={s} snap={snap} />
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
        <h2 className="text-base font-semibold">Drives</h2>
        {nodes.map((n) => (
          <NodeDrives key={n.nodeId} allDrives={drives} nodeId={n.nodeId} name={n.name} target={targetFor(snap, state.nodes, n.nodeId)} unreachable={nodeDown(nodeWord(snap?.status.get(n.nodeId), Date.now()))} spaces={spaces} status={snap?.status.get(n.nodeId)} />
        ))}
      </section>

      <AddSpaceDialog open={adding} onClose={() => setAdding(false)} spaces={spaces} drives={drives} nodeName={nodeName} onAdd={(s) => put(s)} />
      {editingTags && <SpaceTagsDialog space={editingTags} spaces={spaces} drives={drives} nodeName={nodeName} onClose={() => setEditingTags(null)} onSave={(s) => put(s)} />}
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

function AddSpaceDialog({ open, onClose, spaces, drives, nodeName, onAdd }: { open: boolean; onClose: () => void; spaces: Space[]; drives: DriveRecord[]; nodeName: (id: string) => string; onAdd: (s: Space) => Promise<void> }) {
  const [name, setName] = useState("");
  const [tags, setTags] = useState("");
  const [placement, setPlacement] = useState("most_free");
  const [preferLocal, setPreferLocal] = useState(true);
  const [spanning, setSpanning] = useState(false);
  const [exclusive, setExclusive] = useState(false);
  const [mirrored, setMirrored] = useState(false);
  const [busy, setBusy] = useState(false);
  const tagList = tags.split(/[,\s]+/).filter(Boolean);
  const preview = drives.filter((d) => (d.role === "mirrored") === mirrored && d.role !== "parity" && tagList.every((x) => [...d.autoTags, ...d.tags].includes(x)));
  const allTags = Array.from(new Set(drives.flatMap((d) => [...d.autoTags, ...d.tags]))).sort();
  const reset = () => (setName(""), setTags(""), setPlacement("most_free"), setPreferLocal(true), setSpanning(false), setExclusive(false), setMirrored(false));
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
          {spanning && (
            <label className="flex items-center justify-between text-sm">
              <span>
                Prefer local Drives
                <span className="block text-xs text-muted-foreground">Each Node fills its own Drives first and other Nodes' only once its own are full.</span>
              </span>
              <Switch checked={preferLocal} onCheckedChange={setPreferLocal} />
            </label>
          )}
          <label className="flex items-center justify-between text-sm">
            <span>
              Exclusive
              <span className="block text-xs text-muted-foreground">Its Drives belong to this Space only. Needs a tag rule.</span>
            </span>
            <Switch checked={exclusive} onCheckedChange={setExclusive} />
          </label>
          <label className="flex items-center justify-between text-sm">
            <span>
              Mirrored Drives only
              <span className="block text-xs text-muted-foreground">Takes Mirrored Drives and nothing else, so every file in it is on two disks at once. For app data and databases. A plain Space never takes a Mirrored Drive.</span>
            </span>
            <Switch checked={mirrored} onCheckedChange={setMirrored} />
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
                await onAdd(withFill(create(SpaceSchema, { spaceId: id, name: name.trim(), placement, requireTags: tagList, spanNodes: spanning, exclusive, mirrored, parityLevel: mirrored || spaces.length === 0 ? 0 : 1, defaultForNew: spaces.length === 0, priority: spaces.length, createdUnixMs: BigInt(Date.now()) }), placement, spanning && preferLocal));
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

function NodeDrives({ nodeId, name, target, unreachable, spaces, allDrives, status }: { nodeId: string; name: string; target: Target | undefined; unreachable: boolean; spaces: Space[]; allDrives: DriveRecord[]; status: FleetNodeStatus | undefined }) {
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
  const [tagging, setTagging] = useState<Drive | null>(null);
  const [ask, setAsk] = useState<{ on: boolean; drive: Drive } | null>(null);
  const allTags = [...new Set(allDrives.flatMap((d) => [...d.autoTags, ...d.tags]))];
  const openDrive = (d: Drive) => go({ name: "fleet", tab: "files", files: { nodeId, driveId: d.driveId } });
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
              {d.adopted ? (
                <button className="rounded p-0.5 hover:bg-accent" title="Open in Files" aria-label={`Open ${d.record?.name || d.model} in Files`} onClick={() => openDrive(d)}>
                  <HardDrive className="size-5 shrink-0 text-foreground" />
                </button>
              ) : (
                <HardDrive className="size-5 shrink-0 text-muted-foreground" />
              )}
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2 text-sm">
                  {d.adopted ? (
                    <button className="font-medium hover:underline" title="Open in Files" onClick={() => openDrive(d)}>
                      {d.record?.name || d.label || d.model}
                    </button>
                  ) : (
                    <span className="font-medium">{d.record?.name || d.label || d.model}</span>
                  )}
                  {d.record && roleOf(d.record) === "parity" && <Badge variant="secondary">Parity Drive</Badge>}
                  {d.record && roleOf(d.record) === "mirrored" && <Badge variant="outline">Mirrored Drive</Badge>}
                  {d.record?.draining && <Badge variant="outline">emptying</Badge>}
                  {d.adopted && d.record && roleOf(d.record) !== "parity" && idx.length === 0 && <Badge variant="outline">no Space</Badge>}
                  {d.adopted &&
                    idx.map((i) => (
                      <Badge key={i} variant="secondary">
                        <span className="size-2 rounded-full" style={{ background: colorOf(spaces[i], spaces) }} /> {spaces[i].name}
                      </Badge>
                    ))}
                  {!d.adopted && <Badge variant="outline">not used</Badge>}
                  {d.record?.origin === "internal" && <span className="text-xs text-muted-foreground">built in</span>}
                </div>
                <div className="flex flex-wrap items-center gap-x-1 gap-y-0.5 text-xs text-muted-foreground">
                  <span>
                    {d.adopted && d.freeBytes > 0n ? `${formatBytes(d.freeBytes)} free / ${formatBytes(d.sizeBytes - d.freeBytes)} used / ${formatBytes(d.sizeBytes)}` : formatBytes(d.sizeBytes)}
                    {" · "}
                    {d.fstype || "no filesystem"}
                    {d.device && <> · <span className="font-mono">{d.device}</span></>}
                    {d.model && ` · ${d.model}`}
                    {d.serial && <> · <span className="font-mono">{d.serial}</span></>}
                  </span>
                  {d.autoTags.map((t) => (
                    <Badge key={`a-${t}`} variant="outline" className="h-4 px-1.5 text-[10px] text-muted-foreground" title="Found by the Node">
                      {t}
                    </Badge>
                  ))}
                  {(d.record?.tags ?? []).map((t) => (
                    <Badge key={`u-${t}`} variant="secondary" className="h-4 px-1.5 text-[10px]" title="Your tag">
                      {t}
                    </Badge>
                  ))}
                  {d.adopted && d.record && (
                    <Button size="icon" variant="ghost" className="size-5" aria-label={`Change ${d.record.name}'s tags`} title="Add or change your tags" onClick={() => setTagging(d)}>
                      <Pencil className="size-3" />
                    </Button>
                  )}
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
              <div className="flex items-center gap-1">
                {d.adopted && <ParitySwitch drive={d} status={status} onAsk={(on) => setAsk({ on, drive: d })} />}
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
      {client && tagging?.record && (
        <TagsDialog
          title={`Tags for ${tagging.record.name}`}
          description="Spaces take Drives by tag. The Node's own tags stay; add yours."
          initial={tagging.record.tags}
          fixed={tagging.autoTags}
          suggestions={allTags}
          preview={(tags) => {
            const rec = create(DriveRecordSchema, { ...tagging.record!, tags });
            const ids = spacesOf(rec, spaces);
            return `It would be in: ${ids.length ? ids.map((id) => spaces.find((s) => s.spaceId === id)?.name ?? id).join(", ") : "no Space"}.`;
          }}
          onSave={async (tags) => {
            await client.setDriveTags({ driveId: tagging.driveId, tags, name: "" });
            toast.success(`${tagging.record!.name}'s tags saved`);
            refresh();
          }}
          onClose={() => setTagging(null)}
        />
      )}
      {target && ask?.on && <MakeParityDialog target={target} nodeName={name} drive={ask.drive} spaces={spaces} onDone={() => (setAsk(null), refresh())} />}
      {target && ask && !ask.on && ask.drive.record && <ParityOffDialog target={target} nodeName={name} drive={ask.drive.record} losing={spaces.filter((s) => s.parityLevel > 0 && !s.mirrored && allDrives.some((x) => x.nodeId === nodeId && spacesOf(x, spaces).includes(s.spaceId)))} onDone={() => (setAsk(null), refresh())} />}
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

/**
 * Edit a list of tags (decision 201): chips for the tags the Fleet's Drives
 * have, toggled with a click, and a field for new ones. `preview` says what
 * the tags would mean.
 */
function TagsDialog({ title, description, initial, suggestions, fixed = [], preview, problem, onSave, onClose }: { title: string; description: string; initial: string[]; suggestions: string[]; fixed?: string[]; preview?: (tags: string[]) => React.ReactNode; problem?: (tags: string[]) => string; onSave: (tags: string[]) => Promise<void>; onClose: () => void }) {
  const [tags, setTags] = useState<string[]>(initial);
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const add = (raw: string) => {
    const t = raw.trim().toLowerCase().replace(/[^a-z0-9._-]+/g, "-").replace(/^-|-$/g, "");
    if (t && !tags.includes(t) && !fixed.includes(t)) setTags([...tags, t]);
    setTyped("");
  };
  const all = [...new Set([...suggestions, ...tags])].filter((t) => !fixed.includes(t)).sort();
  const why = problem?.(tags) ?? "";
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          {fixed.length > 0 && (
            <div className="flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
              Automatic:
              {fixed.map((t) => (
                <Badge key={t} variant="outline" className="text-muted-foreground">
                  {t}
                </Badge>
              ))}
            </div>
          )}
          {all.length > 0 && (
            <div className="flex flex-wrap gap-1">
              {all.map((t) => (
                <button key={t} type="button" aria-pressed={tags.includes(t)} className={cn("rounded-full border px-2 py-0.5 text-xs", tags.includes(t) ? "border-primary bg-primary text-primary-foreground" : "hover:bg-accent")} onClick={() => setTags(tags.includes(t) ? tags.filter((x) => x !== t) : [...tags, t])}>
                  {t}
                </button>
              ))}
            </div>
          )}
          <form
            className="flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              add(typed);
            }}
          >
            <Input value={typed} onChange={(e) => setTyped(e.target.value)} placeholder="A new tag, such as backups" aria-label="New tag" />
            <Button type="submit" variant="outline" disabled={!typed.trim()}>
              Add
            </Button>
          </form>
          {preview && <p className="text-xs text-muted-foreground">{preview(tags)}</p>}
          {why && <p className="text-xs text-destructive">{why}</p>}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={busy || !!why || JSON.stringify([...tags].sort()) === JSON.stringify([...initial].sort())}
            onClick={async () => {
              setBusy(true);
              try {
                await onSave(tags);
                onClose();
              } catch (e) {
                toast.error(String((e as Error).message ?? e));
              } finally {
                setBusy(false);
              }
            }}
          >
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Which tags a Space takes its Drives by. */
function SpaceTagsDialog({ space, spaces, drives, nodeName, onClose, onSave }: { space: Space; spaces: Space[]; drives: DriveRecord[]; nodeName: (id: string) => string; onClose: () => void; onSave: (s: Space) => Promise<void> }) {
  // The same rule the Nodes apply, with the tags changed.
  const taking = (tags: string[]) => {
    const next = spaces.map((x) => (x.spaceId === space.spaceId ? create(SpaceSchema, { ...space, requireTags: tags }) : x));
    return drives.filter((d) => spacesOf(d, next).includes(space.spaceId));
  };
  return (
    <TagsDialog
      title={`Drives for ${space.name}`}
      description="It takes every Drive that has all of these tags, on any Node. No tags: every Drive."
      initial={space.requireTags}
      suggestions={[...new Set(drives.flatMap((d) => [...d.autoTags, ...d.tags]))]}
      preview={(tags) => {
        const ds = taking(tags);
        return `Would take: ${ds.length === 0 ? "no Drive" : ds.map((d) => `${d.name} on ${nodeName(d.nodeId)}`).join(", ")}.`;
      }}
      problem={(tags) => (space.exclusive && tags.length === 0 && space.nodeIds.length === 0 ? "An exclusive Space needs a tag; otherwise it would take every Drive from the others." : "")}
      onSave={(tags) => onSave(create(SpaceSchema, { ...space, requireTags: tags }))}
      onClose={onClose}
    />
  );
}

/**
 * A Space's coloured left edge, which opens a choice of colours (decision
 * 204): swatches, any colour, or back to the one its place gives it.
 */
function ColorEdge({ space, color, onPick }: { space: Space; color: string; onPick: (c: string) => void }) {
  const [open, setOpen] = useState(false);
  const pick = (c: string) => {
    onPick(c);
    setOpen(false);
  };
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button className="absolute inset-y-0 left-0 w-2 cursor-pointer transition-[width] hover:w-3 focus-visible:w-3 focus-visible:outline-none" style={{ background: color }} aria-label={`Change ${space.name}'s colour`} title="Change its colour" />
      </PopoverTrigger>
      <PopoverContent align="start" side="right" className="w-auto p-3">
        <div className="mb-2 text-xs font-medium text-muted-foreground">{space.name}'s colour</div>
        <div className="grid grid-cols-7 gap-1.5">
          {SWATCHES.map((c) => (
            <button key={c} className="flex size-6 items-center justify-center rounded-full ring-offset-2 ring-offset-popover hover:ring-2 hover:ring-ring" style={{ background: c }} aria-label={c} aria-pressed={space.color.toLowerCase() === c} onClick={() => pick(c)}>
              {space.color.toLowerCase() === c && <Check className="size-3.5 text-white" />}
            </button>
          ))}
        </div>
        <div className="mt-3 flex items-center gap-2 text-xs">
          <label className="flex items-center gap-1.5">
            <input type="color" className="size-6 cursor-pointer rounded border-0 bg-transparent p-0" value={/^#[0-9a-f]{6}$/i.test(space.color) ? space.color : "#3b82f6"} onChange={(e) => onPick(e.target.value)} aria-label="Any colour" /> Any colour
          </label>
          {space.color && (
            <Button size="sm" variant="ghost" className="ml-auto h-7 text-xs" onClick={() => pick("")}>
              Automatic
            </Button>
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}

/** A Space's protection on its card (ADR 0009), with the Protection tab one click away. */
function ProtectionRow({ space, snap }: { space: Space; snap: FleetSnapshot | undefined }) {
  const { go } = useRoute();
  const spaces = snap?.spaces ?? [];
  const protectedSomehow = space.mirrored || space.parityLevel > 0 || space.copies > 1;
  return (
    <div className="mt-2 flex flex-wrap items-center gap-3 text-xs">
      <span className="flex items-center gap-1.5">
        Parity <LevelControl space={space} snap={snap} size="xs" />
      </span>
      <span className="flex items-center gap-1.5">
        Copies <CopiesControl space={space} snap={snap} size="xs" />
      </span>
      {protectedSomehow &&
        (snap?.nodes ?? []).map((n) => {
          const c = coverage(space, n.nodeId, spaces, snap?.drives ?? [], snap?.status.get(n.nodeId), n.name);
          return c ? (
            <span key={n.nodeId} className="flex items-center gap-1" title={c.why}>
              {n.name} <WordPill word={c.word} />
            </span>
          ) : null;
        })}
      <button className="text-muted-foreground underline-offset-2 hover:underline" onClick={() => go({ name: "fleet", tab: "protection" })}>
        Protection tab
      </button>
    </div>
  );
}
