// The demo Fleet's storage protection (ADR 0009): Parity Drives, Drains,
// copies and Mirrored Drives, simulated well enough for the Console to be
// built and screenshotted against it. `?scenario=held` holds Alder's last
// parity update; `?scenario=failed` takes Atlas away.
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { DriveRecordSchema, type CheckParityDriveResponse, type DrainJob, type DrainPlan, type Drive, type MirrorStatus, type ParityStatus, type CopiesStatus } from "@/gen/maitre/v1/storage_pb";
import { spacesOf } from "@/spaces";
import type { DemoDrive, DemoFleet, DemoNode } from "./fleet";

export interface DemoDrain extends Omit<DrainJob, "$typeName" | "plan"> {
  nodeId: string;
  plan?: DrainPlan;
}

export interface NodeParity {
  lastSync: number;
  lastScrub: number;
  job: "" | "sync" | "scrub" | "fix";
  progress: number;
  jobDriveId: string;
  held: string;
  unprotected: number;
  missing: string[];
}

export const scenario = typeof window !== "undefined" ? new URLSearchParams(window.location.search).get("scenario") ?? "" : "";

const fail = (code: Code, msg: string) => new ConnectError(msg, code);

export function newParity(f: DemoFleet, nodeId: string): NodeParity {
  const has = f.drives.some((d) => d.nodeId === nodeId && d.record?.role === "parity");
  return { lastSync: has ? f.now - 9 * 3600_000 : 0, lastScrub: has ? f.now - 3 * 86400_000 : 0, job: "", progress: 0, jobDriveId: "", held: "", unprotected: has ? 214 : 0, missing: [] };
}

export function parityOf(f: DemoFleet, nodeId: string): NodeParity {
  let p = f.parity.get(nodeId);
  if (!p) {
    p = newParity(f, nodeId);
    f.parity.set(nodeId, p);
  }
  return p;
}

const parityDrives = (f: DemoFleet, nodeId: string) => f.drives.filter((d) => d.nodeId === nodeId && d.record?.role === "parity" && !d.record.replacedBy && !d.missing).sort((a, b) => Number(a.record!.addedUnixMs - b.record!.addedUnixMs));

export function parityStatus(f: DemoFleet, n: DemoNode): ParityStatus {
  const p = parityOf(f, n.nodeId);
  const pds = parityDrives(f, n.nodeId);
  const arrays = [1, 2]
    .map((level) => {
      const spaces = f.spaces.filter((s) => s.parityLevel === level && !s.mirrored);
      const drives = f.drives.filter((d) => d.nodeId === n.nodeId && d.record && !d.record.role && spaces.some((s) => spacesOf(d.record!, f.spaces).includes(s.spaceId)));
      return { level, levelGot: Math.min(level, pds.length), spaceIds: spaces.map((s) => s.spaceId), driveIds: drives.map((d) => d.driveId) };
    })
    .filter((a) => a.spaceIds.length && a.driveIds.length && a.levelGot);
  const next = new Date(f.now);
  next.setHours(3, 0, 0, 0);
  if (next.getTime() <= f.now) next.setDate(next.getDate() + 1);
  return { parityDriveIds: pds.map((d) => d.driveId), lastSyncUnixMs: BigInt(p.lastSync), lastScrubUnixMs: BigInt(p.lastScrub), job: p.job, progress: p.progress, jobDriveId: p.jobDriveId, unprotectedFiles: p.unprotected, held: p.held, nextSyncUnixMs: BigInt(next.getTime()), missingDriveIds: p.missing, arrays, lastError: "" } as ParityStatus;
}

export function copiesStatuses(f: DemoFleet, n: DemoNode): CopiesStatus[] {
  return f.spaces
    .filter((s) => s.copies > 1)
    .map((s) => {
      const mine = f.drives.filter((d) => d.nodeId === n.nodeId && d.record && d.record.role !== "parity" && !d.missing && spacesOf(d.record, f.spaces).includes(s.spaceId));
      const disks = new Set(mine.map((d) => d.disk));
      return { spaceId: s.spaceId, shortFiles: disks.size >= s.copies ? 0 : 37, maxCopies: disks.size, lastPassUnixMs: BigInt(f.now - 20 * 60_000), running: false } as CopiesStatus;
    });
}

export function mirrorStatuses(f: DemoFleet, n: DemoNode): MirrorStatus[] {
  return f.drives.filter((d) => d.nodeId === n.nodeId && d.record?.role === "mirrored").map((d) => ({ driveId: d.driveId, missingDisk: scenario === "mirror" ? d.record!.disks[1] : "", errors: 0n, replacing: -1 }) as MirrorStatus);
}

export function drainsOf(f: DemoFleet, nodeId: string): DrainJob[] {
  return f.drains.filter((j) => j.nodeId === nodeId).map(({ nodeId: _n, ...j }) => j as DrainJob);
}

/** One second: Drains move, parity builds, rebuilds finish. */
export function tickProtection(f: DemoFleet) {
  for (const j of f.drains) {
    if (j.state === "queued") j.state = "running";
    if (j.state !== "running") continue;
    j.bytesDone = j.bytesDone + j.bytesTotal / 25n > j.bytesTotal ? j.bytesTotal : j.bytesDone + j.bytesTotal / 25n;
    j.current = j.bytesDone < j.bytesTotal ? ["media/Movies/Dune.mkv", "media/Shows/S01E03.mkv", "Downloads/fedora.iso"][Number(j.bytesDone % 3n)] : "";
    if (j.bytesDone >= j.bytesTotal) {
      j.state = "done";
      j.finishedUnixMs = BigInt(f.now);
      const d = f.drives.find((x) => x.nodeId === j.nodeId && x.driveId === j.driveId);
      if (d?.record) {
        d.record.draining = false;
        d.folders = d.folders.filter((x) => !(j.spaceIds.length === 0 || j.spaceIds.includes(x.spaceId)));
        if (j.purpose === "parity") {
          d.record = create(DriveRecordSchema, { ...d.record, role: "parity", addedUnixMs: BigInt(f.now) });
          d.spaces = [];
          d.folders = [];
          d.freeBytes = d.sizeBytes;
          const p = parityOf(f, j.nodeId);
          p.job = "sync";
          p.progress = 0;
        }
        f.rebuildRecords();
      }
    }
  }
  for (const n of f.nodes) {
    const p = parityOf(f, n.nodeId);
    if (p.job === "") continue;
    p.progress = Math.min(1, p.progress + 0.05);
    if (p.progress >= 1) {
      if (p.job === "scrub") p.lastScrub = f.now;
      else p.lastSync = f.now;
      if (p.job === "fix") p.missing = p.missing.filter((id) => id !== p.jobDriveId);
      p.job = "";
      p.progress = 0;
      p.jobDriveId = "";
      p.held = "";
      p.unprotected = 0;
    }
  }
}

function planFor(f: DemoFleet, d: DemoDrive, looseTo: string, spaceIds: string[]): DrainPlan {
  const plan = { moves: [], warnings: [], blockers: [], bytes: 0n } as unknown as DrainPlan;
  const used = d.sizeBytes - d.freeBytes;
  const spaceFolders = d.folders.filter((x) => x.kind === "space" && (spaceIds.length === 0 || spaceIds.includes(x.spaceId)));
  const loose = d.folders.filter((x) => x.kind !== "space");
  const share = BigInt(spaceFolders.length + (loose.length ? 1 : 0) || 1);
  for (const x of spaceFolders) {
    const s = f.spaces.find((s) => s.spaceId === x.spaceId);
    if (!s) continue;
    const others = f.drives.filter((o) => o.nodeId === d.nodeId && o.driveId !== d.driveId && o.record && !o.record.role && !o.missing && spacesOf(o.record, f.spaces).includes(s.spaceId));
    const free = others.reduce((a, o) => a + o.freeBytes, 0n);
    const bytes = used / share;
    const mv = { spaceId: s.spaceId, toSpace: s.spaceId, bytes, toDriveIds: others.map((o) => o.driveId), toNodeId: "" };
    if (free < bytes) {
      if (s.spanNodes) {
        const peer = f.nodes.find((n) => n.nodeId !== d.nodeId && f.drives.some((o) => o.nodeId === n.nodeId && o.record && spacesOf(o.record, f.spaces).includes(s.spaceId)));
        if (peer) {
          mv.toNodeId = peer.nodeId;
          plan.warnings.push(`${s.name} has no room on this Node's other Drives, so ${fmt(bytes - free)} moves over the network to ${peer.name}'s Pool. That takes a while, and Services here then read those files over the network.`);
        } else plan.blockers.push(`${d.record!.name}'s ${s.name} has no room on this Node's other Drives, and no other Node's Pool of ${s.name} is reachable now.`);
      } else if (others.length === 0) plan.blockers.push(`${d.record!.name} is the only Drive of ${s.name} on this Node, and ${s.name} does not span Nodes. Add a Drive to ${s.name}, or move its files elsewhere first.`);
      else plan.blockers.push(`${s.name}'s other Drives have ${fmt(free)} free, but ${d.record!.name} holds ${fmt(bytes)} of it. Add a Drive to ${s.name} or free some space first.`);
    }
    plan.moves.push(mv as DrainPlan["moves"][number]);
    plan.bytes += bytes;
  }
  if (loose.length && spaceIds.length === 0) {
    const bytes = used / share;
    const mv = { spaceId: "", toSpace: looseTo, bytes, toDriveIds: [] as string[], toNodeId: "" };
    if (!looseTo) plan.blockers.push(`${d.record!.name} holds ${fmt(bytes)} of files that belong to no Space (${loose.map((x) => x.name).join(", ")}). Pick a Space to move them into.`);
    else {
      const s = f.spaces.find((s) => s.spaceId === looseTo);
      if (!s) plan.blockers.push(`There is no Space "${looseTo}" for the loose files.`);
      else {
        mv.toDriveIds = f.drives.filter((o) => o.nodeId === d.nodeId && o.driveId !== d.driveId && o.record && !o.record.role && spacesOf(o.record, f.spaces).includes(looseTo)).map((o) => o.driveId);
        if (mv.toDriveIds.length === 0) plan.blockers.push(`${s.name} has no other Drive on this Node to take the loose files.`);
      }
    }
    plan.moves.push(mv as DrainPlan["moves"][number]);
    plan.bytes += bytes;
  }
  return plan;
}

const fmt = (b: bigint) => (b >= 1n << 40n ? `${(Number(b) / 2 ** 40).toFixed(1)} TB` : `${Math.round(Number(b) / 2 ** 30)} GB`);

export function planDrain(f: DemoFleet, n: DemoNode, driveId: string, looseTo: string): DrainPlan {
  const d = f.drives.find((x) => x.nodeId === n.nodeId && x.driveId === driveId);
  if (!d?.record) throw fail(Code.FailedPrecondition, `no Drive ${driveId}`);
  return planFor(f, d, looseTo, []);
}

export function startDrain(f: DemoFleet, n: DemoNode, driveId: string, looseTo: string, confirm: string, purpose: string): DrainJob {
  const d = f.drives.find((x) => x.nodeId === n.nodeId && x.driveId === driveId);
  if (!d?.record) throw fail(Code.FailedPrecondition, `no Drive ${driveId}`);
  if (confirm.trim() !== d.record.name) throw fail(Code.FailedPrecondition, `to empty it, repeat exactly: ${d.record.name}`);
  if (f.drains.some((j) => j.driveId === driveId && (j.state === "queued" || j.state === "running"))) throw fail(Code.FailedPrecondition, `${d.record.name} is already being emptied`);
  const plan = planFor(f, d, looseTo, []);
  if (plan.blockers.length) throw fail(Code.FailedPrecondition, plan.blockers.join(" "));
  const job: DemoDrain = { nodeId: n.nodeId, jobId: `drain-${f.now}-${f.drains.length + 1}`, driveId, state: "queued", message: "", bytesDone: 0n, bytesTotal: plan.bytes || 1n, current: "", purpose, notes: [...plan.warnings], startedUnixMs: BigInt(f.now), finishedUnixMs: 0n, spaceIds: [], plan } as DemoDrain;
  d.record.draining = true;
  f.drains.push(job);
  f.rebuildRecords();
  const { nodeId: _n, ...out } = job;
  return out as DrainJob;
}

export function cancelDrain(f: DemoFleet, jobId: string) {
  const j = f.drains.find((x) => x.jobId === jobId);
  if (!j) throw fail(Code.NotFound, "no such job");
  if (j.state === "queued" || j.state === "running") {
    j.state = "cancelled";
    j.message = "stopped; files already moved stay where they went, the rest are still on the Drive";
    const d = f.drives.find((x) => x.nodeId === j.nodeId && x.driveId === j.driveId);
    if (d?.record) d.record.draining = false;
    f.rebuildRecords();
  }
}

export function checkParity(f: DemoFleet, n: DemoNode, driveId: string, device: string, looseTo: string): CheckParityDriveResponse {
  const d = f.drives.find((x) => x.nodeId === n.nodeId && (driveId ? x.driveId === driveId : x.device === device));
  if (!d) throw fail(Code.FailedPrecondition, "no such Drive or disk on this Node");
  const res = { blockers: [] as string[], warnings: [] as string[], plan: undefined as DrainPlan | undefined, largestProtectedBytes: 0n, protects: [] as string[] };
  const name = d.record?.name || d.model;
  if (d.record?.role === "parity") res.blockers.push(`${name} is a Parity Drive already.`);
  if (d.record?.role === "mirrored") res.blockers.push(`${name} is a Mirrored Drive; it already survives a failed disk.`);
  if (parityDrives(f, n.nodeId).length >= 2) res.blockers.push("This Node has two Parity Drives already, the most a Node takes.");
  const others = f.drives.filter((o) => o.nodeId === n.nodeId && o.driveId !== d.driveId && o.record && !o.record.role && !o.missing);
  if (others.length === 0) res.blockers.push("This Node would have no other Drive to protect.");
  for (const o of f.drives) if (o.nodeId === n.nodeId && o.driveId !== d.driveId && o.record && o.disk === d.disk) res.blockers.push(`${name} shares its disk with ${o.record.name}. A Parity Drive must be a disk of its own; otherwise one failure takes both.`);
  const protects = new Set<string>();
  for (const o of others) {
    const covered = spacesOf(o.record!, f.spaces).filter((id) => f.spaces.some((s) => s.spaceId === id && s.parityLevel > 0 && !s.mirrored));
    if (covered.length) {
      covered.forEach((id) => protects.add(id));
      const used = o.sizeBytes - o.freeBytes;
      if (used > res.largestProtectedBytes) res.largestProtectedBytes = used;
    }
  }
  res.protects = [...protects].sort();
  if (d.sizeBytes < res.largestProtectedBytes) res.blockers.push(`${name} holds ${fmt(d.sizeBytes)}; a Drive it would protect holds ${fmt(res.largestProtectedBytes)}. A Parity Drive must be at least as large as the most protected data on any one Drive.`);
  if (protects.size === 0) res.warnings.push("No Space on this Node has a Parity Level yet; the Parity Drive protects nothing until one does.");
  if (d.record && d.folders.length) {
    const plan = planFor(f, d, looseTo, []);
    res.plan = plan;
    res.blockers.push(...plan.blockers);
    res.warnings.push(...plan.warnings);
  }
  return res as CheckParityDriveResponse;
}

export function setParity(f: DemoFleet, n: DemoNode, i: { driveId?: string; device?: string; name?: string; parity?: boolean; looseToSpace?: string; confirm?: string }) {
  const d = f.drives.find((x) => x.nodeId === n.nodeId && (i.driveId ? x.driveId === i.driveId : x.device === i.device));
  if (!d) throw fail(Code.FailedPrecondition, "no such Drive or disk on this Node");
  const out = () => {
    const { nodeId: _n, ...rest } = d;
    return rest as Drive;
  };
  if (!i.parity) {
    if (d.record?.role !== "parity") throw fail(Code.FailedPrecondition, `${d.record?.name} is not a Parity Drive`);
    if ((i.confirm ?? "").trim() !== d.record.name) throw fail(Code.FailedPrecondition, `repeat exactly: ${d.record.name}`);
    d.record = create(DriveRecordSchema, { ...d.record, role: "" });
    d.spaces = spacesOf(d.record, f.spaces);
    d.folders = d.spaces.map((id) => ({ name: id, kind: "space" as const, spaceId: id }));
    f.rebuildRecords();
    return { drive: out() };
  }
  const check = checkParity(f, n, i.driveId ?? "", i.device ?? "", i.looseToSpace ?? "");
  if (check.blockers.length) throw fail(Code.FailedPrecondition, check.blockers.join(" "));
  if (!d.record) {
    if ((i.confirm ?? "").trim() !== d.model) throw fail(Code.FailedPrecondition, `to format, repeat exactly: ${d.model}`);
    d.record = create(DriveRecordSchema, { driveId: d.driveId, nodeId: n.nodeId, name: i.name || d.suggestedName || d.model, model: d.model, serial: d.serial, sizeBytes: d.sizeBytes, fstype: "btrfs", autoTags: d.autoTags, origin: "formatted", addedUnixMs: BigInt(f.now), role: "parity" });
    d.adopted = true;
    d.fstype = "btrfs";
    d.freeBytes = d.sizeBytes;
    const p = parityOf(f, n.nodeId);
    p.job = "sync";
    p.progress = 0;
    f.rebuildRecords();
    return { drive: out() };
  }
  if ((i.confirm ?? "").trim() !== d.record.name) throw fail(Code.FailedPrecondition, `repeat exactly: ${d.record.name}`);
  if (d.folders.length) return { job: startDrain(f, n, d.driveId, i.looseToSpace ?? "", i.confirm ?? "", "parity") };
  d.record = create(DriveRecordSchema, { ...d.record, role: "parity", addedUnixMs: BigInt(f.now) });
  d.spaces = [];
  d.freeBytes = d.sizeBytes;
  const p = parityOf(f, n.nodeId);
  p.job = "sync";
  p.progress = 0;
  f.rebuildRecords();
  return { drive: out() };
}

export function protectNow(f: DemoFleet, n: DemoNode) {
  const p = parityOf(f, n.nodeId);
  if (p.job) throw fail(Code.FailedPrecondition, "parity is busy: a sync, scrub or rebuild is running");
  if (p.missing.length) throw fail(Code.FailedPrecondition, `${p.missing.map((id) => f.drives.find((d) => d.driveId === id)?.record?.name ?? id).join(", ")} is not connected. A sync now would record its files as deleted.`);
  p.job = "sync";
  p.progress = 0;
}

export function replaceDrive(f: DemoFleet, n: DemoNode, failedId: string, device: string, confirm: string): Drive {
  const old = f.drives.find((x) => x.nodeId === n.nodeId && x.driveId === failedId);
  if (!old?.record) throw fail(Code.FailedPrecondition, `no Drive ${failedId}`);
  if (!old.missing) throw fail(Code.FailedPrecondition, `${old.record.name} is still connected and mounted; a Drive is replaced once it has failed`);
  const blank = f.drives.find((x) => x.nodeId === n.nodeId && x.device === device && !x.adopted);
  if (!blank) throw fail(Code.FailedPrecondition, `no such disk ${device}`);
  if (confirm.trim() !== blank.model) throw fail(Code.FailedPrecondition, `to format, repeat exactly: ${blank.model}`);
  blank.record = create(DriveRecordSchema, { ...old.record, driveId: blank.driveId, model: blank.model, serial: blank.serial, sizeBytes: blank.sizeBytes, fstype: "btrfs", autoTags: blank.autoTags, origin: "formatted", addedUnixMs: BigInt(f.now), snapraidName: old.record.snapraidName || old.driveId, draining: false });
  blank.adopted = true;
  blank.fstype = "btrfs";
  blank.spaces = spacesOf(blank.record, f.spaces);
  blank.folders = blank.spaces.map((id) => ({ name: id, kind: "space" as const, spaceId: id }));
  blank.freeBytes = blank.sizeBytes - (old.sizeBytes - old.freeBytes);
  old.record = create(DriveRecordSchema, { ...old.record, replacedBy: blank.driveId });
  const p = parityOf(f, n.nodeId);
  p.job = old.record.role === "parity" ? "sync" : "fix";
  p.jobDriveId = blank.driveId;
  p.progress = 0;
  p.missing = p.missing.filter((id) => id !== failedId);
  p.held = "";
  f.rebuildRecords();
  const { nodeId: _n, ...out } = blank;
  return out as Drive;
}

export function makeMirrored(f: DemoFleet, n: DemoNode, devices: string[], name: string, tags: string[], confirm: string): Drive {
  if (devices.length !== 2 || devices[0] === devices[1]) throw fail(Code.FailedPrecondition, "a Mirrored Drive is made from exactly two disks");
  const pair = devices.map((dev) => f.drives.find((x) => x.nodeId === n.nodeId && x.device === dev));
  if (pair.some((d) => !d)) throw fail(Code.FailedPrecondition, "no such disk");
  if (pair.some((d) => d!.adopted)) throw fail(Code.FailedPrecondition, "a disk is a Drive of this Node already; forget it first");
  if (confirm.trim() !== pair[0]!.model) throw fail(Code.FailedPrecondition, `to format both, repeat exactly: ${pair[0]!.model}`);
  const [a, b] = pair as DemoDrive[];
  const size = a.sizeBytes < b.sizeBytes ? a.sizeBytes : b.sizeBytes;
  a.record = create(DriveRecordSchema, { driveId: a.driveId, nodeId: n.nodeId, name: name || a.suggestedName || a.model, model: `2 × ${a.model}`, serial: a.serial, sizeBytes: size, fstype: "btrfs", autoTags: a.autoTags, tags, origin: "formatted", addedUnixMs: BigInt(f.now), role: "mirrored", disks: [a.serial, b.serial] });
  a.adopted = true;
  a.fstype = "btrfs";
  a.sizeBytes = size;
  a.freeBytes = size;
  a.model = `2 × ${a.model}`;
  a.device = `${a.device} + ${b.device}`;
  a.spaces = spacesOf(a.record, f.spaces);
  a.folders = a.spaces.map((id) => ({ name: id, kind: "space" as const, spaceId: id }));
  f.drives = f.drives.filter((d) => d !== b);
  f.rebuildRecords();
  const { nodeId: _n, ...out } = a;
  return out as Drive;
}
