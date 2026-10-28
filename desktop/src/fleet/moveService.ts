// Moving a Service to another Node (decision 191), done by the Console with
// calls every Node already has: stop it, move the folders the new Node
// cannot reach, remove it from the old Node keeping its data, wait until
// the new Node has heard, and install the same Service there. A Node may
// not take over another Node's live Service of the same name (189), hence
// the order. Anything that fails puts it back where it was.
import { create } from "@bufbuild/protobuf";
import { createClient } from "@connectrpc/connect";
import { FilesService } from "@/gen/maitre/v1/files_pb";
import { ServiceSpecSchema, type ServiceSpec } from "@/gen/maitre/v1/service_pb";
import { spacesOn } from "@/files/sources";
import type { KnownNode } from "@/state";
import { transportFor, type Target } from "@/transport";
import { controlService, node, removeService, services, targetFor } from "./actions";
import type { FleetService, FleetSnapshot } from "./useFleet";

export interface VolumePlan {
  volume: string;
  fromSpace: string;
  toSpace: string;
  /** A folder the user picked (170): it stays, and must be reachable. */
  picked: boolean;
  /** Its folder is copied to the new Node (then removed from the old). */
  copy: boolean;
}

export interface MovePlan {
  volumes: VolumePlan[];
  /** Reasons it cannot move as things stand. */
  blockers: string[];
  /** What changes for the people using it. */
  warnings: string[];
}

/**
 * What moving `s` to `toNodeId` would do. `choice` overrides the Space a
 * volume with its own folder lands in. A spanning Space is the same Pool on
 * every Node, so its folders stay; any other Space is a different Pool on
 * each Node, so they are copied.
 */
export function planMove(snap: FleetSnapshot | undefined, s: FleetService, toNodeId: string, choice: Record<string, string> = {}): MovePlan {
  const spaces = snap?.spaces ?? [];
  const reach = new Set(spacesOn(snap, toNodeId).map((sp) => sp.spaceId));
  const name = (id: string) => spaces.find((sp) => sp.spaceId === id)?.name ?? id;
  const spans = (id: string) => !!spaces.find((sp) => sp.spaceId === id)?.spanNodes;
  const target = snap?.nodes.find((n) => n.nodeId === toNodeId)?.name ?? "that Node";
  const from = snap?.nodes.find((n) => n.nodeId === s.spec.nodeId)?.name ?? "its Node";
  const def = spaces.find((sp) => sp.defaultForNew && reach.has(sp.spaceId))?.spaceId;
  const plan: MovePlan = { volumes: [], blockers: [], warnings: [] };

  for (const v of s.spec.volumes) {
    const fromSpace = s.spec.volumeSpaces[v.name] ?? "";
    if (v.name in s.spec.volumeFolders) {
      const folder = s.spec.volumeFolders[v.name] || "its top";
      if (!reach.has(fromSpace)) plan.blockers.push(`${v.name} uses the folder ${folder} in ${name(fromSpace)}, which ${target} cannot reach. Point it at a folder ${target} can reach in Storage first.`);
      else if (!spans(fromSpace)) plan.warnings.push(`${v.name} uses the folder ${folder} in ${name(fromSpace)}. ${name(fromSpace)} is not shared between Nodes, so on ${target} that folder holds ${target}'s own files, not ${from}'s.`);
      plan.volumes.push({ volume: v.name, fromSpace, toSpace: fromSpace, picked: true, copy: false });
      continue;
    }
    const toSpace = choice[v.name] ?? (reach.has(fromSpace) ? fromSpace : (def ?? [...reach][0] ?? ""));
    if (!toSpace) plan.blockers.push(`${target} has no Storage Space for ${v.name}.`);
    plan.volumes.push({ volume: v.name, fromSpace, toSpace, picked: false, copy: toSpace !== fromSpace || !spans(fromSpace) });
  }
  for (const m of s.spec.mounts) {
    if (!reach.has(m.spaceId)) plan.blockers.push(`The extra folder ${m.folder || "(top)"} in ${name(m.spaceId)} is out of ${target}'s reach. Remove it in Storage first.`);
    else if (!spans(m.spaceId)) plan.warnings.push(`The extra folder ${m.folder || "(top)"} is in ${name(m.spaceId)}, which is not shared between Nodes: on ${target} it holds ${target}'s own files.`);
  }
  if (s.spec.infrastructure) plan.warnings.push(`${s.spec.name} is network infrastructure: devices that use it at ${from}'s address must be pointed at ${target}'s afterwards.`);
  if (s.spec.forwardPorts || s.spec.exposure === "direct") plan.warnings.push(`Your router forwards to ${from} for ${s.spec.name}; ${target} asks the router to forward to it instead, and a router that does not answer needs changing by hand.`);
  return plan;
}

export type StepId = "stop" | "copy" | "remove" | "wait" | "install" | "start";
export interface Step {
  id: StepId;
  label: string;
  state: "todo" | "doing" | "done" | "failed" | "skipped";
  detail?: string;
  /** 0..1 while copying. */
  progress?: number;
}

export function stepsFor(s: FleetService, plan: MovePlan, fromName: string, toName: string, running: boolean): Step[] {
  const copies = plan.volumes.filter((v) => v.copy);
  return [
    { id: "stop", label: `Turn off ${s.spec.name}`, state: running ? "todo" : "skipped" },
    { id: "copy", label: copies.length ? `Move ${copies.map((v) => v.volume).join(", ")} to ${toName}` : "No data to move", state: copies.length ? "todo" : "skipped" },
    { id: "remove", label: `Remove it from ${fromName}, keeping its data`, state: "todo" },
    { id: "wait", label: `Wait for ${toName} to hear of it`, state: "todo" },
    { id: "install", label: `Install it on ${toName}`, state: "todo" },
    { id: "start", label: running ? "Start it" : "Leave it off, as it was", state: "todo" },
  ];
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** Move each folder with a file manager job on `from`, one after another. */
async function copyFolders(from: Target, name: string, list: { fromSpace: string; toSpace: string; volume: string }[], toNodeId: string, onProgress: (p: number, current: string) => void): Promise<number> {
  const files = createClient(FilesService, transportFor(from));
  let moved = 0;
  for (const v of list) {
    const path = `${name}/${v.volume}`;
    const { jobId } = await files.move({ from: { spaceId: v.fromSpace, driveId: "" }, path, toSpace: v.toSpace, toPath: path, toNodeId });
    for (;;) {
      await sleep(1000);
      const job = (await files.listJobs({})).jobs.find((j) => j.jobId === jobId);
      if (!job) throw new Error(`the move of ${v.volume} went missing`);
      const total = Number(job.bytesTotal) || 1;
      onProgress((moved + Math.min(1, Number(job.bytesDone) / total)) / list.length, v.volume);
      if (job.state === "done") break;
      if (job.state === "failed" || job.state === "cancelled") throw new Error(`moving ${v.volume}: ${job.message || job.state}`);
    }
    moved++;
  }
  return moved;
}

/** Until `t` holds no live record of the Service, at most a minute. */
async function waitFreed(t: Target, name: string) {
  const until = Date.now() + 60_000;
  while (Date.now() < until) {
    const recs = (await node(t).listRecords({ kind: "service" })).records;
    if (!recs.some((r) => r.id === name && !r.deleted)) return;
    await sleep(1000);
  }
  throw new Error("the Node did not hear that it was removed within a minute");
}

/** The spec to install on the new Node: the same Service, with the Node's own parts cleared. */
export function specFor(spec: ServiceSpec, plan: MovePlan): ServiceSpec {
  const volumeSpaces = { ...spec.volumeSpaces };
  for (const v of plan.volumes) volumeSpaces[v.volume] = v.toSpace;
  return create(ServiceSpecSchema, { ...spec, nodeId: "", webHostPort: 0, createdUnixMs: 0n, volumeSpaces });
}

export async function moveService(
  snap: FleetSnapshot | undefined,
  known: KnownNode[],
  s: FleetService,
  toNodeId: string,
  plan: MovePlan,
  set: (id: StepId, patch: Partial<Step>) => void,
): Promise<void> {
  const name = s.spec.name;
  const from = targetFor(snap, known, s.spec.nodeId);
  const to = targetFor(snap, known, toNodeId);
  if (!from || !to) throw new Error("a Node has no known address");
  const running = s.status?.state === 2 || s.status?.state === 3;
  const original = create(ServiceSpecSchema, { ...s.spec });
  const copies = plan.volumes.filter((v) => v.copy);
  let step: StepId = "stop";
  let moved = 0;
  let removed = false;

  // Back to how it was, as far as it got: folders back, Service back, on again.
  const undo = async (why: unknown) => {
    set(step, { state: "failed", detail: String((why as Error)?.message ?? why) });
    const notes: string[] = [];
    try {
      if (moved > 0) await copyFolders(to, name, copies.slice(0, moved).map((v) => ({ volume: v.volume, fromSpace: v.toSpace, toSpace: v.fromSpace })), s.spec.nodeId, () => {});
    } catch (e) {
      notes.push(`its data could not be moved back: ${String((e as Error).message ?? e)}`);
    }
    try {
      if (removed) await services(from).installService({ spec: create(ServiceSpecSchema, { ...original, webHostPort: 0, createdUnixMs: 0n, nodeId: "" }) });
      else if (running) await controlService(from, name, "start");
    } catch (e) {
      notes.push(`it could not be put back: ${String((e as Error).message ?? e)}`);
    }
    if (removed && !running) await controlService(from, name, "stop").catch(() => {});
    throw new Error(`${String((why as Error)?.message ?? why)}${notes.length ? `; ${notes.join("; ")}` : `. ${name} is back as it was.`}`);
  };

  try {
    if (running) {
      set("stop", { state: "doing" });
      await controlService(from, name, "stop");
      set("stop", { state: "done" });
    }
    if (copies.length) {
      step = "copy";
      set("copy", { state: "doing", progress: 0 });
      // Counted one by one so a failure part-way moves back only what went.
      for (const v of copies) {
        await copyFolders(from, name, [v], toNodeId, (p, current) => set("copy", { progress: (moved + p) / copies.length, detail: current }));
        moved++;
      }
      set("copy", { state: "done", progress: 1, detail: undefined });
    }
    step = "remove";
    set("remove", { state: "doing" });
    await removeService(from, name, false);
    removed = true;
    set("remove", { state: "done" });
    step = "wait";
    set("wait", { state: "doing" });
    await waitFreed(to, name);
    set("wait", { state: "done" });
    step = "install";
    set("install", { state: "doing" });
    await services(to).installService({ spec: specFor(original, plan) });
    set("install", { state: "done" });
  } catch (e) {
    await undo(e);
  }
  step = "start";
  if (running) set("start", { state: "done", detail: "starting" });
  else {
    // Installing starts it; it was off, so it goes off again.
    await controlService(to, name, "stop").catch(() => {});
    set("start", { state: "done" });
  }
}
