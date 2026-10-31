// Storage protection's controls (ADR 0009), shared by the Protection tab
// and the Storage tab: a Space's Parity Level and Copies, a Drive's Parity
// Drive switch with its confirmation, the schedule, a Mirrored Drive from
// two disks, Replace, and the running jobs.
import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { toast } from "sonner";
import { AlertTriangle, Check, Clock, Layers, ShieldCheck, ShieldOff, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";
import { ago, formatBytes } from "@/lib/format";
import { StorageService, SpaceSchema, ProtectionScheduleSchema, type Drive, type DriveRecord, type ProtectionSchedule, type Space, type DrainJob } from "@/gen/maitre/v1/storage_pb";
import type { FleetNodeStatus } from "@/gen/maitre/v1/node_pb";
import { transportFor, type Target } from "@/transport";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { currentRecord, putSigned } from "@/fleet/sign";
import { nextOwnerId } from "@/fleet/owners";
import { COPIES_HELP, DAY, PARITY_HELP, maxCopies, nextSync, scheduleWords, type Word } from "@/protection";

/** A Node that lacks a call answers Unimplemented: say what to do instead. */
export function needsUpdate(e: unknown, nodeName: string): Error {
  if (e instanceof ConnectError && e.code === Code.Unimplemented) return new Error(`${nodeName} runs a Maître Server from before storage protection. Update it on the Nodes tab.`);
  return e instanceof Error ? e : new Error(String(e));
}

/** Writes a Space record through the anchor, giving it an owner id if it lacks one (decision 182). */
export function useSpacePut(snap: FleetSnapshot | undefined) {
  const qc = useQueryClient();
  return async (s: Space) => {
    if (!snap) throw new Error("no Node of the Fleet can be reached");
    if (!s.ownerId) s = create(SpaceSchema, { ...s, ownerId: nextOwnerId(snap.spaces) });
    try {
      await putSigned({ addr: snap.anchor.addr, fingerprint: snap.anchor.nodeId }, { kind: "space", id: s.spaceId, body: toBinary(SpaceSchema, s) }, currentRecord(snap.records, "space", s.spaceId));
    } catch (e) {
      throw /Parity Level|copies|Mirrored Space/.test(String((e as Error).message)) ? e : needsUpdate(e, snap.nodes.find((n) => n.nodeId === snap.anchor.nodeId)?.name ?? "the Node");
    }
    await qc.invalidateQueries({ queryKey: ["fleet"] });
  };
}

/** A row of buttons for a small choice: Off/1/2 or 1/2/3. */
export function Segmented<T extends string | number>({ value, options, onChange, label, size = "sm" }: { value: T; options: { v: T; text: string; off?: string }[]; onChange: (v: T) => void; label: string; size?: "sm" | "xs" }) {
  return (
    <div role="radiogroup" aria-label={label} className={cn("inline-flex rounded-md border p-0.5", size === "xs" && "text-xs")}>
      {options.map((o) => (
        <button key={String(o.v)} role="radio" aria-checked={o.v === value} disabled={!!o.off} title={o.off} onClick={() => onChange(o.v)} className={cn("rounded px-2.5 py-0.5 text-sm transition-colors disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent", size === "xs" && "px-2 text-xs", o.v === value ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:bg-accent")}>
          {o.text}
        </button>
      ))}
    </div>
  );
}

/** A Space's Parity Level (decision 237); a Mirrored Space shows "Mirrored" instead (256). */
export function LevelControl({ space, snap, size }: { space: Space; snap: FleetSnapshot | undefined; size?: "sm" | "xs" }) {
  const put = useSpacePut(snap);
  if (space.mirrored)
    return (
      <span className={cn("inline-flex items-center gap-1 text-status-online", size === "xs" ? "text-xs" : "text-sm")} title="Every Drive of this Space is a Mirrored Drive: each file is on two disks at once. Parity is not needed.">
        <ShieldCheck className="size-3.5" /> Mirrored
      </span>
    );
  return <Segmented label={`Parity Level of ${space.name}`} size={size} value={space.parityLevel} onChange={(v) => put(create(SpaceSchema, { ...space, parityLevel: v })).catch((e) => toast.error(String((e as Error).message ?? e)))} options={[{ v: 0, text: "Off" }, { v: 1, text: "1" }, { v: 2, text: "2" }]} />;
}

/** A Space's Copies (decision 253), no higher than its Drives on separate disks (259). */
export function CopiesControl({ space, snap, size }: { space: Space; snap: FleetSnapshot | undefined; size?: "sm" | "xs" }) {
  const put = useSpacePut(snap);
  const max = snap ? maxCopies(space, snap.spaces, snap.drives, snap.status) : 3;
  const current = Math.max(1, space.copies);
  const off = (n: number) => (n > max && n !== current ? `${space.name} has ${max} Drive${max === 1 ? "" : "s"}${space.spanNodes ? "" : " on any one Node"}. Add Drives to it for more copies.` : undefined);
  return <Segmented label={`Copies of ${space.name}`} size={size} value={current} onChange={(v) => put(create(SpaceSchema, { ...space, copies: v })).catch((e) => toast.error(String((e as Error).message ?? e)))} options={[1, 2, 3].map((v) => ({ v, text: String(v), off: off(v) }))} />;
}

const WORD_STYLE: Record<Word, string> = {
  Protected: "bg-status-online/15 text-status-online border-status-online/40",
  "Partly protected": "bg-status-attention/15 text-status-attention border-status-attention/50",
  "Not protected": "bg-status-attention/15 text-status-attention border-status-attention/50",
  Off: "text-muted-foreground",
  Held: "bg-status-attention/15 text-status-attention border-status-attention/50",
  "Drive missing": "bg-destructive/15 text-destructive border-destructive/40",
  "Building parity": "bg-status-starting/15 text-status-starting border-status-starting/40",
  Rebuilding: "bg-status-starting/15 text-status-starting border-status-starting/40",
};

export function WordPill({ word, className }: { word: Word; className?: string }) {
  const Icon = word === "Protected" ? ShieldCheck : word === "Off" ? ShieldOff : word === "Building parity" || word === "Rebuilding" ? Clock : AlertTriangle;
  return (
    <span className={cn("inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium whitespace-nowrap", WORD_STYLE[word], className)}>
      <Icon className="size-3" /> {word}
    </span>
  );
}

/** A Space's colour as a left edge, as on the Storage tab's Space cards. Its parent must be `relative`. */
export function SpaceEdge({ color }: { color: string }) {
  return <span aria-hidden className="absolute inset-y-0 left-0 w-1.5" style={{ background: color }} />;
}

/** The schedule a Node follows: its own record, else the Fleet's, else the defaults (decision 248). */
export function scheduleFor(snap: FleetSnapshot | undefined, nodeId?: string): { schedule: ProtectionSchedule; own: boolean } {
  const read = (id: string) => {
    const r = currentRecord(snap?.records, "protection", id);
    return r && !r.deleted ? fromBinary(ProtectionScheduleSchema, r.body) : undefined;
  };
  const own = nodeId ? read(nodeId) : undefined;
  if (own) return { schedule: own, own: true };
  return { schedule: read("fleet") ?? create(ProtectionScheduleSchema, {}), own: false };
}

/** The Fleet's schedule, or one Node's own (decisions 230, 248). */
export function ScheduleEditor({ snap, nodeId, nodeName }: { snap: FleetSnapshot | undefined; nodeId?: string; nodeName?: string }) {
  const qc = useQueryClient();
  const { schedule: s, own } = scheduleFor(snap, nodeId);
  const inherits = !!nodeId && !own;
  const save = async (next: ProtectionSchedule | undefined) => {
    if (!snap) return;
    const id = nodeId ?? "fleet";
    try {
      await putSigned({ addr: snap.anchor.addr, fingerprint: snap.anchor.nodeId }, { kind: "protection", id, body: next ? toBinary(ProtectionScheduleSchema, next) : new Uint8Array(), deleted: !next }, currentRecord(snap.records, "protection", id));
      await qc.invalidateQueries({ queryKey: ["fleet"] });
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    }
  };
  const days = [...s.syncDays];
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button size="sm" variant="outline" className="h-7 text-xs">
          <Clock className="size-3.5" /> {scheduleWords(s)}
          {inherits && <span className="text-muted-foreground"> (Fleet)</span>}
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-80 space-y-3 text-sm">
        <div className="font-medium">{nodeId ? `${nodeName}'s parity schedule` : "Fleet parity schedule"}</div>
        {nodeId && (
          <label className="flex items-center gap-2">
            <Switch checked={!inherits} onCheckedChange={(v) => save(v ? create(ProtectionScheduleSchema, { ...scheduleFor(snap).schedule }) : undefined)} /> Its own schedule
          </label>
        )}
        <fieldset disabled={inherits} className={cn("space-y-3", inherits && "opacity-50")}>
          <label className="flex items-center gap-2">
            Update at <Input type="time" value={s.syncTime || "03:00"} onChange={(e) => save(create(ProtectionScheduleSchema, { ...s, syncTime: e.target.value }))} className="h-7 w-28" />
          </label>
          <div className="flex gap-1" role="group" aria-label="Days">
            {DAY.map((d, i) => {
              const on = days.length === 0 || days.includes(i);
              return (
                <button key={d} aria-pressed={on} onClick={() => save(create(ProtectionScheduleSchema, { ...s, syncDays: (days.length === 0 ? [0, 1, 2, 3, 4, 5, 6] : days).includes(i) ? (days.length === 0 ? [0, 1, 2, 3, 4, 5, 6] : days).filter((x) => x !== i) : [...days, i].sort() }))} className={cn("rounded border px-1.5 py-0.5 text-xs", on ? "bg-primary text-primary-foreground" : "text-muted-foreground")}>
                  {d}
                </button>
              );
            })}
          </div>
          <label className="flex items-center gap-2">
            Check files for damage every
            <Select value={String(s.scrubEveryDays || 7)} onValueChange={(v) => save(create(ProtectionScheduleSchema, { ...s, scrubEveryDays: parseInt(v, 10) }))}>
              <SelectTrigger size="sm" className="h-7 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="7">week</SelectItem>
                <SelectItem value="14">two weeks</SelectItem>
                <SelectItem value="28">four weeks</SelectItem>
              </SelectContent>
            </Select>
          </label>
        </fieldset>
        <p className="text-xs text-muted-foreground">Updates run at low priority so Services stay quick. An update is held, not run, when a Drive is missing or many files were deleted.</p>
      </PopoverContent>
    </Popover>
  );
}

/** "Protected as of 9 h ago · 214 newer files wait for Sun 03:00". */
export function SyncLine({ snap, nodeId, status }: { snap: FleetSnapshot | undefined; nodeId: string; status: FleetNodeStatus | undefined }) {
  const p = status?.parity;
  if (!p || p.parityDriveIds.length === 0) return <span className="text-muted-foreground">No parity yet</span>;
  if (p.job === "sync") return <span>Updating parity, {Math.round(p.progress * 100)}%</span>;
  if (p.job === "scrub") return <span>Checking files for damage, {Math.round(p.progress * 100)}%</span>;
  if (p.job === "fix") return <span>Rebuilding {snap?.drives.find((d) => d.driveId === p.jobDriveId)?.name ?? "a Drive"}, {Math.round(p.progress * 100)}%</span>;
  if (!p.lastSyncUnixMs) return <span className="text-muted-foreground">Waiting for the first parity update</span>;
  const next = p.nextSyncUnixMs ? new Date(Number(p.nextSyncUnixMs)) : nextSync(scheduleFor(snap, nodeId).schedule, new Date());
  return (
    <span>
      Protected as of {ago(Number(p.lastSyncUnixMs))}
      {p.unprotectedFiles > 0 && <span className="text-muted-foreground"> · {p.unprotectedFiles} newer files wait for {next.toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" })}</span>}
    </span>
  );
}

/** The Parity Drive switch on a Drive row (decision 225). */
export function ParitySwitch({ drive, status, onAsk }: { drive: Drive; status: FleetNodeStatus | undefined; onAsk: (on: boolean) => void }) {
  const rec = drive.record;
  if (!rec || rec.origin === "internal" || rec.role === "mirrored") return null;
  const draining = rec.draining || status?.drains.some((j) => j.driveId === drive.driveId && j.purpose === "parity");
  return (
    <label className="flex items-center gap-1.5 text-xs" title="A Parity Drive protects this Node's other Drives">
      <Switch checked={rec.role === "parity" || !!draining} disabled={!!draining} onCheckedChange={onAsk} aria-label={`${rec.name} is a Parity Drive`} /> Parity Drive
    </label>
  );
}

function humanTB(b: bigint | number) {
  return formatBytes(b);
}

/**
 * The Parity Drive toggle's confirmation (decisions 225, 238-241, 246):
 * what the Node's checks say, what moves where, a Space for loose files,
 * and the Drive's name typed.
 */
export function MakeParityDialog({ target, nodeName, drive, spaces, onDone }: { target: Target; nodeName: string; drive: Drive; spaces: Space[]; onDone: () => void }) {
  const client = useMemo(() => createClient(StorageService, transportFor(target)), [target.addr, target.fingerprint]);
  const name = drive.record?.name || drive.suggestedName || drive.model;
  const [dest, setDest] = useState("");
  const [typed, setTyped] = useState("");
  const [newName, setNewName] = useState(name);
  const [busy, setBusy] = useState(false);
  const check = useQuery({
    queryKey: ["parity-check", target.addr, drive.driveId || drive.device, dest],
    queryFn: () => client.checkParityDrive({ driveId: drive.record ? drive.driveId : "", device: drive.record ? "" : drive.device, looseToSpace: dest }),
    retry: false,
  });
  const res = check.data;
  const loose = res?.plan?.moves.filter((m) => !m.spaceId) ?? [];
  const blocked = !res || res.blockers.length > 0;
  const expected = drive.record ? drive.record.name : drive.model.endsWith(" disk") && !drive.serial ? drive.device : drive.model;
  const spaceName = (id: string) => spaces.find((s) => s.spaceId === id)?.name ?? id;
  const go = async () => {
    setBusy(true);
    try {
      const r = await client.setParityDrive({ driveId: drive.record ? drive.driveId : "", device: drive.record ? "" : drive.device, name: drive.record ? "" : newName, parity: true, looseToSpace: dest, confirm: typed });
      toast.success(r.job ? `${name}'s files are moving; it becomes a Parity Drive once they have.` : `${newName} is now a Parity Drive; the first parity update is running.`);
      onDone();
    } catch (e) {
      toast.error(needsUpdate(e, nodeName).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onDone()}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>Make {name} a Parity Drive?</DialogTitle>
          <DialogDescription>
            A Parity Drive holds no files of its own. It keeps the information that rebuilds a failed Drive on {nodeName}, for every Space with a Parity Level. {drive.record ? `${name}'s files move to other Drives first; then it is erased and the first parity update runs. Services keep their files where they see them throughout.` : "The disk is erased and the first parity update runs."}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3 text-sm">
          {check.isLoading && <p className="text-muted-foreground">Checking…</p>}
          {check.error && <p className="text-destructive">{needsUpdate(check.error, nodeName).message}</p>}
          {!drive.record && (
            <div className="space-y-1">
              <Label htmlFor="pd-newname">Name</Label>
              <Input id="pd-newname" value={newName} onChange={(e) => setNewName(e.target.value)} />
            </div>
          )}
          {res && res.plan && res.plan.moves.some((m) => m.spaceId) && (
            <div>
              <div className="mb-1 font-medium">What moves</div>
              <ul className="space-y-0.5">
                {res.plan.moves
                  .filter((m) => m.spaceId)
                  .map((m) => (
                    <li key={m.spaceId} className="flex gap-2">
                      <span className="w-16 shrink-0 text-right tabular-nums text-muted-foreground">{humanTB(m.bytes)}</span>
                      <span>
                        {spaceName(m.spaceId)} → {m.toNodeId ? `its Pool on another Node` : `${spaceName(m.toSpace)}'s other Drives here`}
                      </span>
                    </li>
                  ))}
              </ul>
            </div>
          )}
          {(loose.length > 0 || (res && res.blockers.some((b) => b.includes("Pick a Space")))) && (
            <label className="flex flex-wrap items-center gap-2">
              <span>Files that belong to no Space move into</span>
              <Select value={dest} onValueChange={setDest}>
                <SelectTrigger size="sm" className="h-7">
                  <SelectValue placeholder="choose a Space" />
                </SelectTrigger>
                <SelectContent>
                  {spaces
                    .filter((s) => !s.mirrored)
                    .map((s) => (
                      <SelectItem key={s.spaceId} value={s.spaceId}>
                        {s.name}
                      </SelectItem>
                    ))}
                </SelectContent>
              </Select>
              <span className="text-xs text-muted-foreground">in a folder "From {name}"</span>
            </label>
          )}
          {res && (
            <ul className="space-y-1">
              {res.blockers.map((b) => (
                <li key={b} className="flex gap-2">
                  <X className="mt-0.5 size-4 shrink-0 text-destructive" />
                  <span className="text-destructive">{b}</span>
                </li>
              ))}
              {res.warnings.map((w) => (
                <li key={w} className="flex gap-2">
                  <AlertTriangle className="mt-0.5 size-4 shrink-0 text-status-attention" />
                  <span>{w}</span>
                </li>
              ))}
              {res.blockers.length === 0 && (
                <li className="flex gap-2">
                  <Check className="mt-0.5 size-4 shrink-0 text-status-online" />
                  <span>
                    {res.protects.length ? `Protects ${res.protects.map(spaceName).join(", ")} on ${nodeName}. ` : ""}
                    {res.largestProtectedBytes > 0n ? `Large enough: the most protected data on one Drive is ${humanTB(res.largestProtectedBytes)}, and ${name} holds ${humanTB(drive.sizeBytes)}.` : `${name} holds ${humanTB(drive.sizeBytes)}.`}
                  </span>
                </li>
              )}
            </ul>
          )}
          {!blocked && (
            <div className="space-y-1">
              <Label htmlFor="pd-name">Type {expected} to erase it{drive.record ? " once its files have moved" : ""}</Label>
              <Input id="pd-name" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" />
            </div>
          )}
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onDone}>
            Cancel
          </Button>
          <Button disabled={blocked || busy || typed.trim() !== expected || (!drive.record && !newName.trim())} onClick={go}>
            {drive.record && res?.plan?.moves.length ? "Move files and make it a Parity Drive" : "Make it a Parity Drive"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Turning a Parity Drive off (decision 245). */
export function ParityOffDialog({ target, nodeName, drive, losing, onDone }: { target: Target; nodeName: string; drive: DriveRecord; losing: Space[]; onDone: () => void }) {
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <Dialog open onOpenChange={(o) => !o && onDone()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Stop using {drive.name} for parity?</DialogTitle>
          <DialogDescription>
            Its parity is deleted and it joins the Spaces its tags match, as an empty Drive. {losing.length ? `${losing.map((s) => s.name).join(", ")} on ${nodeName} lose${losing.length === 1 ? "s" : ""} a level of protection.` : ""}
          </DialogDescription>
        </DialogHeader>
        <Label htmlFor="po-name">Type {drive.name}</Label>
        <Input id="po-name" value={typed} onChange={(e) => setTyped(e.target.value)} />
        <DialogFooter>
          <Button variant="ghost" onClick={onDone}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            disabled={busy || typed.trim() !== drive.name}
            onClick={async () => {
              setBusy(true);
              try {
                await createClient(StorageService, transportFor(target)).setParityDrive({ driveId: drive.driveId, parity: false, confirm: typed });
                toast.success(`${drive.name} is a data Drive again`);
                onDone();
              } catch (e) {
                toast.error(needsUpdate(e, nodeName).message);
              } finally {
                setBusy(false);
              }
            }}
          >
            Stop parity on {drive.name}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Two blank disks become a Mirrored Drive in a Mirrored Space (decisions 254, 255). */
export function MirroredDialog({ target, nodeName, blank, spaces, snap, onDone }: { target: Target; nodeName: string; blank: Drive[]; spaces: Space[]; snap: FleetSnapshot | undefined; onDone: () => void }) {
  const put = useSpacePut(snap);
  const qc = useQueryClient();
  const mirrored = spaces.filter((s) => s.mirrored);
  const [picked, setPicked] = useState<string[]>(blank.slice(0, 2).map((d) => d.device));
  const [name, setName] = useState(blank[0]?.suggestedName ?? "");
  const [space, setSpace] = useState(mirrored[0]?.spaceId ?? "new");
  const [newSpace, setNewSpace] = useState("App data");
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const chosen = picked.map((p) => blank.find((d) => d.device === p)!).filter(Boolean);
  const expected = chosen[0] ? (chosen[0].model.endsWith(" disk") && !chosen[0].serial ? chosen[0].device : chosen[0].model) : "";
  const size = chosen.length === 2 ? (chosen[0].sizeBytes < chosen[1].sizeBytes ? chosen[0].sizeBytes : chosen[1].sizeBytes) : 0n;
  const go = async () => {
    setBusy(true);
    try {
      let tags: string[] = [];
      if (space === "new") {
        const id = newSpace.trim().toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
        if (spaces.some((s) => s.spaceId === id)) throw new Error(`there is a Space called ${newSpace} already`);
        await put(create(SpaceSchema, { spaceId: id, name: newSpace.trim(), placement: "most_free", mirrored: true, priority: spaces.length, createdUnixMs: BigInt(Date.now()) }));
      } else {
        tags = spaces.find((s) => s.spaceId === space)?.requireTags ?? [];
      }
      await createClient(StorageService, transportFor(target)).makeMirroredDrive({ devices: picked, name: name.trim(), tags, confirm: typed });
      toast.success(`${name.trim()} is a Mirrored Drive on ${nodeName}`);
      await qc.invalidateQueries({ queryKey: ["drives"] });
      await qc.invalidateQueries({ queryKey: ["fleet"] });
      onDone();
    } catch (e) {
      toast.error(needsUpdate(e, nodeName).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onDone()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Make a Mirrored Drive on {nodeName}</DialogTitle>
          <DialogDescription>Two blank disks become one Drive that writes every file to both at once and repairs damage from the other copy. Best for app data and databases. You get the space of the smaller disk.</DialogDescription>
        </DialogHeader>
        <div className="space-y-2 text-sm">
          {blank.length < 2 && <p className="text-status-attention">{nodeName} has {blank.length === 0 ? "no" : "one"} blank disk; a Mirrored Drive needs two whole disks that hold nothing.</p>}
          {blank.map((d) => (
            <label key={d.device} className="flex items-center gap-2">
              <input type="checkbox" checked={picked.includes(d.device)} onChange={(e) => setPicked(e.target.checked ? [...picked, d.device].slice(-2) : picked.filter((p) => p !== d.device))} /> {d.model} · <span className="font-mono">{d.device}</span> · {formatBytes(d.sizeBytes)}
            </label>
          ))}
          {chosen.length === 2 && <p className="text-muted-foreground">Gives {formatBytes(size)}.</p>}
          <div className="space-y-1">
            <Label htmlFor="mi-name">Name</Label>
            <Input id="mi-name" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <label className="flex flex-wrap items-center gap-2 pt-1">
            It goes to
            <Select value={space} onValueChange={setSpace}>
              <SelectTrigger size="sm" className="h-7">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {mirrored.map((s) => (
                  <SelectItem key={s.spaceId} value={s.spaceId}>
                    {s.name}
                  </SelectItem>
                ))}
                <SelectItem value="new">a new Mirrored Space…</SelectItem>
              </SelectContent>
            </Select>
            {space === "new" && <Input value={newSpace} onChange={(e) => setNewSpace(e.target.value)} className="h-7 w-36" aria-label="New Space's name" />}
          </label>
          <p className="text-xs text-muted-foreground">Mirrored Drives go only to Mirrored Spaces, and those take nothing else, so every file in a Space is protected the same way.</p>
          <Label htmlFor="mi-confirm">Type {expected || "the model"} to erase both</Label>
          <Input id="mi-confirm" value={typed} onChange={(e) => setTyped(e.target.value)} />
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onDone}>
            Cancel
          </Button>
          <Button disabled={busy || chosen.length !== 2 || !name.trim() || typed.trim() !== expected || (space === "new" && !newSpace.trim())} onClick={go}>
            Make the Mirrored Drive
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** A new disk in a failed Drive's place (decision 232). */
export function ReplaceDialog({ target, nodeName, failed, blank, onDone }: { target: Target; nodeName: string; failed: DriveRecord; blank: Drive[]; onDone: () => void }) {
  const [device, setDevice] = useState(blank.find((d) => d.sizeBytes >= failed.sizeBytes)?.device ?? "");
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const chosen = blank.find((d) => d.device === device);
  const expected = chosen ? (chosen.model.endsWith(" disk") && !chosen.serial ? chosen.device : chosen.model) : "";
  return (
    <Dialog open onOpenChange={(o) => !o && onDone()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Replace {failed.name}</DialogTitle>
          <DialogDescription>
            The new disk is erased and takes {failed.name}'s place: its name, its tags and its Spaces. {failed.role === "parity" ? "Parity is built onto it again." : `Its files are rebuilt from parity; until that finishes, ${nodeName}'s other Drives are not protected.`} At least {formatBytes(failed.sizeBytes)}.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-2 text-sm">
          {blank.length === 0 && <p className="text-status-attention">{nodeName} has no blank disk. Plug one in.</p>}
          {blank.map((d) => (
            <label key={d.device} className={cn("flex items-center gap-2", d.sizeBytes < failed.sizeBytes && "text-muted-foreground")}>
              <input type="radio" name="replace-with" checked={device === d.device} onChange={() => setDevice(d.device)} /> {d.model} · <span className="font-mono">{d.device}</span> · {formatBytes(d.sizeBytes)}
              {d.sizeBytes < failed.sizeBytes && " · too small"}
            </label>
          ))}
          <Label htmlFor="rp-confirm">Type {expected || "the model"} to erase it</Label>
          <Input id="rp-confirm" value={typed} onChange={(e) => setTyped(e.target.value)} />
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onDone}>
            Cancel
          </Button>
          <Button
            disabled={busy || !chosen || typed.trim() !== expected}
            onClick={async () => {
              setBusy(true);
              try {
                await createClient(StorageService, transportFor(target)).replaceDrive({ failedDriveId: failed.driveId, device, confirm: typed });
                toast.success(`${failed.name} is being rebuilt onto the new disk`);
                onDone();
              } catch (e) {
                toast.error(needsUpdate(e, nodeName).message);
              } finally {
                setBusy(false);
              }
            }}
          >
            Replace {failed.name}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** A Drain under way (decision 247), with its notes and a Cancel. */
export function DrainRow({ job, nodeName, driveName, target }: { job: DrainJob; nodeName: string; driveName: string; target: Target | undefined }) {
  const pct = job.bytesTotal > 0n ? Math.min(100, Math.round((Number(job.bytesDone) * 100) / Number(job.bytesTotal))) : 0;
  const what = job.purpose === "parity" ? `${driveName} becoming a Parity Drive` : job.purpose === "rule" ? `${driveName} leaving ${job.spaceIds.join(", ")}` : `Emptying ${driveName}`;
  return (
    <div className="space-y-1 rounded-lg border p-3 text-sm">
      <div className="flex items-center gap-2">
        <Layers className="size-4 text-status-starting" />
        <span className="font-medium">{what}</span>
        <span className="text-muted-foreground">
          · {nodeName} · {job.state === "queued" ? "waiting" : job.current || "moving files"}
        </span>
        <span className="ml-auto tabular-nums">{pct}%</span>
        {target && (
          <Button
            size="sm"
            variant="ghost"
            className="h-6"
            onClick={() =>
              createClient(StorageService, transportFor(target))
                .cancelDrain({ jobId: job.jobId })
                .then(() => toast(`Stopped. Files already moved stay where they went.`))
                .catch((e) => toast.error(String((e as Error).message ?? e)))
            }
          >
            Cancel
          </Button>
        )}
      </div>
      <div className="h-1.5 overflow-hidden rounded bg-muted">
        <div className="h-full bg-status-starting transition-all" style={{ width: `${pct}%` }} />
      </div>
      {job.notes.map((n) => (
        <div key={n} className="text-xs text-muted-foreground">
          {n}
        </div>
      ))}
    </div>
  );
}

export { PARITY_HELP, COPIES_HELP };
