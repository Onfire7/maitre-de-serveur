// The Protection tab (ADR 0009, decision 257): a table of Spaces with
// Parity Level, Copies and a status per Node; a card per Node with its
// Parity Drives, schedule, "Protect now", the Drives that could become
// Parity Drives and its Mirrored Drives; then running jobs. A held update
// or a failed Drive shows as a banner above, with its action.
import { useMemo, useState } from "react";
import { useQueries, useQueryClient } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { toast } from "sonner";
import { HardDrive, Plus, Shield } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { create } from "@bufbuild/protobuf";
import { formatBytes } from "@/lib/format";
import { StorageService, SpaceSchema, type Drive, type DriveRecord, type Space } from "@/gen/maitre/v1/storage_pb";
import { transportFor, type Target } from "@/transport";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { nodeDown, nodeWord } from "@/fleet/useFleet";
import { targetFor } from "@/fleet/actions";
import type { ConsoleState } from "@/state";
import { spacesOf } from "@/spaces";
import { coverage, parityDrivesOf, roleOf } from "@/protection";
import { colorOf } from "./StorageTab";
import { COPIES_HELP, CopiesControl, DrainRow, LevelControl, MakeParityDialog, MirroredDialog, PARITY_HELP, ParityOffDialog, ParitySwitch, ReplaceDialog, ScheduleEditor, SpaceEdge, SyncLine, WordPill, needsUpdate, useSpacePut } from "./ProtectionControls";

type Ask = { kind: "parity-on"; nodeId: string; drive: Drive } | { kind: "parity-off"; nodeId: string; drive: DriveRecord } | { kind: "mirror"; nodeId: string } | { kind: "replace"; nodeId: string; failed: DriveRecord };

export function ProtectionTab({ snap, state }: { snap: FleetSnapshot | undefined; state: ConsoleState }) {
  const qc = useQueryClient();
  const put = useSpacePut(snap);
  const spaces = useMemo(() => [...(snap?.spaces ?? [])].sort((a, b) => b.priority - a.priority), [snap?.spaces]);
  const listed = useMemo(() => [...spaces].sort((a, b) => a.name.localeCompare(b.name)), [spaces]);
  const nodes = snap?.nodes ?? [];
  const drives = snap?.drives ?? [];
  const nodeName = (id: string) => nodes.find((n) => n.nodeId === id)?.name ?? id.slice(0, 8);
  const [ask, setAsk] = useState<Ask | null>(null);
  // Each reachable Node's live disks: blank ones can become Parity or
  // Mirrored Drives, and the switch needs the Drive as the Node sees it.
  const live = useQueries({
    queries: nodes.map((n) => {
      const t = targetFor(snap, state.nodes, n.nodeId);
      const down = nodeDown(nodeWord(snap?.status.get(n.nodeId), Date.now()));
      return { queryKey: ["drives", n.nodeId, t?.addr], enabled: !!t && !down, refetchInterval: 5000, retry: false, queryFn: async () => (await createClient(StorageService, transportFor(t!)).listDrives({})).drives.filter((d) => !d.system) };
    }),
  });
  const liveOf = (nodeId: string): Drive[] => live[nodes.findIndex((n) => n.nodeId === nodeId)]?.data ?? [];
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["drives"] });
    qc.invalidateQueries({ queryKey: ["fleet"] });
  };
  const target = (nodeId: string) => targetFor(snap, state.nodes, nodeId);
  const running = nodes.flatMap((n) => (snap?.status.get(n.nodeId)?.drains ?? []).map((j) => ({ n, j })));

  return (
    <div className="space-y-8">
      <Banners snap={snap} onReplace={(nodeId, failed) => setAsk({ kind: "replace", nodeId, failed })} onProtectNow={(nodeId) => protectNow(target(nodeId), nodeName(nodeId))} />
      <section className="space-y-3">
        <div>
          <h2 className="text-base font-semibold">Spaces</h2>
          <p className="text-sm text-muted-foreground">How many failed Drives each Space survives, and how many copies of each file it keeps. The same settings are on each Space in Storage.</p>
        </div>
        {listed.length === 0 && <p className="rounded-xl border bg-card p-4 text-sm text-muted-foreground">No Spaces yet. Make one on the Storage tab.</p>}
        {listed.length > 0 && (
          <div className="overflow-hidden rounded-xl border bg-card">
            <table className="w-full text-sm">
              <thead className="border-b text-left text-xs text-muted-foreground">
                <tr>
                  <th className="px-4 py-2 font-normal">Space</th>
                  <th className="px-2 py-2 font-normal" title={PARITY_HELP}>
                    Parity Level
                  </th>
                  <th className="px-2 py-2 font-normal" title={COPIES_HELP}>
                    Copies
                  </th>
                  <th className="px-4 py-2 font-normal">On each Node</th>
                </tr>
              </thead>
              <tbody className="divide-y">
                {listed.map((s) => {
                  // A Service's own volume (not a picked shared folder) in the Space makes it app data.
                  const appData = snap?.specs.some((sp) => Object.entries(sp.volumeSpaces).some(([vol, id]) => id === s.spaceId && !(vol in sp.volumeFolders)));
                  return (
                    <tr key={s.spaceId} className="align-top">
                      <td className="relative px-4 py-3">
                        <SpaceEdge color={colorOf(s, spaces)} />
                        <div className="font-medium">{s.name}</div>
                        <div className="text-xs text-muted-foreground">{[s.mirrored ? "Mirrored Drives only" : "", appData ? "app data" : "", s.spanNodes ? "spans Nodes" : ""].filter(Boolean).join(" · ")}</div>
                      </td>
                      <td className="px-2 py-3">
                        <LevelControl space={s} snap={snap} />
                        {appData && !s.mirrored && s.parityLevel > 0 && <div className="mt-1 max-w-48 text-xs text-status-attention">App data changes constantly; parity covers it poorly.</div>}
                      </td>
                      <td className="px-2 py-3">
                        <CopiesControl space={s} snap={snap} />
                        {s.spanNodes && s.copies > 1 && (
                          <label className="mt-1 flex items-center gap-1.5 text-xs">
                            <Switch checked={s.copiesOnDifferentNodes} onCheckedChange={(v) => put(create(SpaceSchema, { ...s, copiesOnDifferentNodes: v })).catch((e) => toast.error(String((e as Error).message ?? e)))} /> on different Nodes
                          </label>
                        )}
                      </td>
                      <td className="space-y-1 px-4 py-3">
                        {nodes.map((n) => {
                          const cov = coverage(s, n.nodeId, spaces, drives, snap?.status.get(n.nodeId), n.name);
                          if (!cov) return null;
                          return (
                            <div key={n.nodeId} className="flex flex-wrap items-center gap-2">
                              <span className="w-14 font-medium">{n.name}</span>
                              <WordPill word={cov.word} />
                              <span className="text-xs text-muted-foreground">{cov.why}</span>
                            </div>
                          );
                        })}
                        {!nodes.some((n) => coverage(s, n.nodeId, spaces, drives, snap?.status.get(n.nodeId), n.name)) && <span className="text-xs text-muted-foreground">no Drives yet</span>}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        <details className="text-sm text-muted-foreground">
          <summary className="cursor-pointer">What parity and copies protect, and what they don't</summary>
          <p className="mt-1">{PARITY_HELP}</p>
          <p className="mt-1">{COPIES_HELP}</p>
        </details>
      </section>

      <section className="space-y-3">
        <div className="flex items-center justify-between">
          <div>
            <h2 className="text-base font-semibold">Parity Drives</h2>
            <p className="text-sm text-muted-foreground">Each Node protects its own Drives. A Space can't get more levels than its Node has Parity Drives.</p>
          </div>
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            Fleet schedule <ScheduleEditor snap={snap} />
          </div>
        </div>
        {nodes.map((n) => (
          <NodeParityCard key={n.nodeId} snap={snap} nodeId={n.nodeId} name={n.name} spaces={spaces} live={liveOf(n.nodeId)} target={target(n.nodeId)} onAsk={setAsk} />
        ))}
      </section>

      {running.length > 0 && (
        <section className="space-y-3">
          <h2 className="text-base font-semibold">Running</h2>
          <div className="space-y-2">
            {running.map(({ n, j }) => (
              <DrainRow key={j.jobId} job={j} nodeName={n.name} driveName={drives.find((d) => d.driveId === j.driveId)?.name ?? j.driveId} target={target(n.nodeId)} />
            ))}
          </div>
        </section>
      )}

      {ask?.kind === "parity-on" && target(ask.nodeId) && <MakeParityDialog target={target(ask.nodeId)!} nodeName={nodeName(ask.nodeId)} drive={ask.drive} spaces={spaces} onDone={() => (setAsk(null), refresh())} />}
      {ask?.kind === "parity-off" && target(ask.nodeId) && <ParityOffDialog target={target(ask.nodeId)!} nodeName={nodeName(ask.nodeId)} drive={ask.drive} losing={spaces.filter((s) => s.parityLevel > 0 && !s.mirrored && drives.some((d) => d.nodeId === ask.nodeId && spacesOf(d, spaces).includes(s.spaceId)))} onDone={() => (setAsk(null), refresh())} />}
      {ask?.kind === "mirror" && target(ask.nodeId) && <MirroredDialog target={target(ask.nodeId)!} nodeName={nodeName(ask.nodeId)} blank={liveOf(ask.nodeId).filter((d) => !d.adopted && !d.fstype && d.device === d.disk)} spaces={spaces} snap={snap} onDone={() => (setAsk(null), refresh())} />}
      {ask?.kind === "replace" && target(ask.nodeId) && <ReplaceDialog target={target(ask.nodeId)!} nodeName={nodeName(ask.nodeId)} failed={ask.failed} blank={liveOf(ask.nodeId).filter((d) => !d.adopted)} onDone={() => (setAsk(null), refresh())} />}
    </div>
  );
}

async function protectNow(target: Target | undefined, nodeName: string) {
  if (!target) return;
  try {
    await createClient(StorageService, transportFor(target)).protectNow({});
    toast.success(`${nodeName} is updating its parity`);
  } catch (e) {
    toast.error(needsUpdate(e, nodeName).message);
  }
}

function NodeParityCard({ snap, nodeId, name, spaces, live, target, onAsk }: { snap: FleetSnapshot | undefined; nodeId: string; name: string; spaces: Space[]; live: Drive[]; target: Target | undefined; onAsk: (a: Ask) => void }) {
  const status = snap?.status.get(nodeId);
  const drives = snap?.drives ?? [];
  const parityIds = parityDrivesOf(nodeId, drives, status);
  const pds = parityIds.map((id) => drives.find((d) => d.driveId === id)).filter((d): d is DriveRecord => !!d);
  const mirrors = drives.filter((d) => d.nodeId === nodeId && roleOf(d) === "mirrored" && !d.replacedBy);
  const candidates = live.filter((d) => (d.adopted && d.record && roleOf(d.record) === "data" && d.record.origin !== "internal" && !d.record.draining) || (!d.adopted && !d.fstype));
  const blank = live.filter((d) => !d.adopted && !d.fstype && d.device === d.disk);
  const busy = status?.parity?.job !== "" && status?.parity?.job !== undefined;
  const liveOf = (id: string) => live.find((d) => d.driveId === id);
  return (
    <div className="rounded-xl border bg-card">
      <div className="flex flex-wrap items-center gap-3 border-b px-4 py-2.5 text-sm">
        <span className="font-semibold">{name}</span>
        <SyncLine snap={snap} nodeId={nodeId} status={status} />
        <div className="ml-auto flex items-center gap-2">
          {pds.length > 0 && <ScheduleEditor snap={snap} nodeId={nodeId} nodeName={name} />}
          {pds.length > 0 && (
            <Button size="sm" variant="outline" className="h-7 text-xs" disabled={busy} onClick={() => protectNow(target, name)}>
              Protect now
            </Button>
          )}
        </div>
      </div>
      <ul className="divide-y text-sm">
        {pds.map((d, i) => (
          <li key={d.driveId} className="flex items-center gap-3 px-4 py-2">
            <Shield className="size-4 text-status-online" />
            <span className="font-medium">{d.name}</span>
            <span className="text-xs text-muted-foreground">
              Parity Drive {i + 1} · {formatBytes(d.sizeBytes)}
            </span>
            {status?.parity?.missingDriveIds.includes(d.driveId) && <Badge variant="destructive">missing</Badge>}
            <div className="ml-auto">{liveOf(d.driveId) ? <ParitySwitch drive={liveOf(d.driveId)!} status={status} onAsk={() => onAsk({ kind: "parity-off", nodeId, drive: d })} /> : null}</div>
          </li>
        ))}
        {mirrors.map((d) => {
          const health = status?.mirrors.find((m) => m.driveId === d.driveId);
          return (
            <li key={d.driveId} className="flex flex-wrap items-center gap-3 px-4 py-2">
              <HardDrive className="size-4 text-status-online" />
              <span className="font-medium">{d.name}</span>
              <span className="text-xs text-muted-foreground">
                Mirrored Drive · {d.disks.join(" + ") || d.model} · in {spacesOf(d, spaces).map((id) => spaces.find((s) => s.spaceId === id)?.name ?? id).join(", ") || "no Space"}
              </span>
              {health?.missingDisk && <Badge variant="destructive">{health.missingDisk} missing</Badge>}
              {health && health.errors > 0n && <Badge variant="outline">{String(health.errors)} errors</Badge>}
              {health && health.replacing >= 0 && <Badge variant="outline">replacing, {Math.round(health.replacing * 100)}%</Badge>}
            </li>
          );
        })}
        {pds.length < 2 && candidates.length > 0 && (
          <li className="flex flex-wrap items-center gap-2 px-4 py-2 text-xs text-muted-foreground">
            {pds.length === 0 ? "No Parity Drive. Turn one of these into one:" : "Add a second Parity Drive for Spaces at level 2:"}
            {candidates.map((d) => (
              <Button key={d.device} size="sm" variant="outline" className="h-6 text-xs" onClick={() => onAsk({ kind: "parity-on", nodeId, drive: d })}>
                {d.record?.name || d.model} · {formatBytes(d.sizeBytes)}
                {!d.adopted && " · blank"}
              </Button>
            ))}
          </li>
        )}
        {pds.length === 0 && candidates.length === 0 && mirrors.length === 0 && <li className="px-4 py-2 text-xs text-muted-foreground">{live.length === 0 ? "Its disks show when it can be reached." : "No Drive here could become a Parity Drive."}</li>}
        <li className="px-4 py-2">
          <Button size="sm" variant="ghost" className="h-6 text-xs" disabled={!target} onClick={() => onAsk({ kind: "mirror", nodeId })}>
            <Plus className="size-3" /> Make a Mirrored Drive from two blank disks{blank.length < 2 ? ` (${blank.length} here)` : ""}
          </Button>
        </li>
      </ul>
    </div>
  );
}

/** A held update or a failed Drive, with what to do (decisions 231, 232). */
function Banners({ snap, onReplace, onProtectNow }: { snap: FleetSnapshot | undefined; onReplace: (nodeId: string, failed: DriveRecord) => void; onProtectNow: (nodeId: string) => void }) {
  const out: React.ReactNode[] = [];
  for (const n of snap?.nodes ?? []) {
    const st = snap?.status.get(n.nodeId);
    const p = st?.parity;
    for (const id of p?.missingDriveIds ?? []) {
      const d = snap?.drives.find((x) => x.driveId === id);
      if (!d || d.replacedBy) continue;
      out.push(
        <div key={`missing-${id}`} className="flex flex-wrap items-center gap-3 rounded-xl border border-destructive/60 bg-destructive/10 p-4 text-sm">
          <div className="min-w-0 flex-1">
            <div className="font-medium">
              {d.name} on {n.name} stopped answering
            </div>
            <div className="text-muted-foreground">{roleOf(d) === "parity" ? `It held ${n.name}'s parity. Until it is replaced, the Node's Drives are not protected.` : `Its files are missing from the Pool. Parity can rebuild them onto a new disk of at least ${formatBytes(d.sizeBytes)}. Updates are held until then.`}</div>
          </div>
          <Button size="sm" onClick={() => onReplace(n.nodeId, d)}>
            Replace {d.name}…
          </Button>
        </div>,
      );
    }
    if (p?.held && !(p.missingDriveIds.length > 0))
      out.push(
        <div key={`held-${n.nodeId}`} className="flex flex-wrap items-center gap-3 rounded-xl border border-status-attention/60 bg-status-attention/10 p-4 text-sm">
          <div className="min-w-0 flex-1">
            <div className="font-medium">{n.name}'s parity update was held</div>
            <div className="text-muted-foreground">{p.held}</div>
          </div>
          <Button size="sm" onClick={() => onProtectNow(n.nodeId)}>
            It was me, update now
          </Button>
        </div>,
      );
    for (const m of st?.mirrors ?? []) {
      const d = snap?.drives.find((x) => x.driveId === m.driveId);
      if (!d || !m.missingDisk) continue;
      out.push(
        <div key={`mirror-${m.driveId}`} className="flex flex-wrap items-center gap-3 rounded-xl border border-destructive/60 bg-destructive/10 p-4 text-sm">
          <div className="min-w-0 flex-1">
            <div className="font-medium">
              {d.name} on {n.name} lost a disk
            </div>
            <div className="text-muted-foreground">{m.missingDisk} is gone. {d.name} runs on its other disk; another failure would lose its files. Replace the disk from the Nodes tab's Drive row once a blank one is plugged in.</div>
          </div>
        </div>,
      );
    }
  }
  return out.length ? <div className="space-y-2">{out}</div> : null;
}
