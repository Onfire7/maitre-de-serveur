// The Console's queue of Maître Server updates (decision 215): Update, Roll
// back and Restart now each take a Node down, so they run one at a time.
// The next starts once no Node is updating, ours or an automatic rollout,
// and a failure holds the rest until it is dismissed. The queue lives here,
// not in a card, so it carries on whichever tab is open.
import { UpdateStatus_State, type UpdateStatus } from "@/gen/maitre/v1/node_pb";
import type { FleetSnapshot } from "./useFleet";
import { nodeWord } from "./useFleet";

export type Maintenance = { kind: "update"; target: string; reboot: boolean } | { kind: "rollback"; reboot: boolean } | { kind: "reboot" };

/** waiting in the queue, working (downloading, staging), down (restarting), back, failed. */
export type Phase = "waiting" | "working" | "down" | "back" | "failed";

export interface Job {
  id: number;
  nodeId: string;
  name: string;
  m: Maintenance;
  phase: Phase;
  progress: number;
  message: string;
  started: number;
}

interface Calls {
  /** Asks the Node to update, roll back or restart. */
  start: () => Promise<unknown>;
  /** The Node's update status; throws while it is down. */
  status: () => Promise<UpdateStatus | undefined>;
}

let jobs: Job[] = [];
const calls = new Map<number, Calls>();
const listeners = new Set<() => void>();
let nextId = 1;
let lastSnap: FleetSnapshot | undefined;
/** How often a running job asks its Node; tests make it short. */
export const timing = { tick: 1500, settle: 1500 };

function set(next: Job[]) {
  // A new array each time: cards read it with useSyncExternalStore.
  jobs = next;
  listeners.forEach((f) => f());
}

function patch(id: number, p: Partial<Job>) {
  set(jobs.map((j) => (j.id === id ? { ...j, ...p } : j)));
}

export function queued(): Job[] {
  return jobs;
}

export function onQueue(f: () => void) {
  listeners.add(f);
  return () => void listeners.delete(f);
}

export function describe(m: Maintenance) {
  return m.kind === "update" ? `update to ${m.target}` : m.kind === "rollback" ? "roll back" : "restart";
}

/** Adds a job for a Node at the back of the queue; it starts at once when nothing is updating. */
export function enqueue(nodeId: string, name: string, m: Maintenance, c: Calls) {
  if (jobs.some((j) => j.nodeId === nodeId && j.phase !== "failed")) return;
  const id = nextId++;
  calls.set(id, c);
  set([...jobs, { id, nodeId, name, m, phase: "waiting", progress: 0, message: "", started: 0 }]);
  pump(lastSnap);
}

/** Takes a job off: a waiting one is cancelled, a failed one dismissed. */
export function remove(id: number) {
  const j = jobs.find((x) => x.id === id);
  if (!j || j.phase === "working" || j.phase === "down") return;
  calls.delete(id);
  set(jobs.filter((x) => x.id !== id));
  pump(lastSnap);
}

/** Whether a Node is busy updating: one of ours running or failed, or one the Fleet reports. */
export function busy(snap: FleetSnapshot | undefined, now = Date.now()) {
  if (jobs.some((j) => j.phase !== "waiting")) return true;
  if (!snap) return false;
  for (const st of snap.status.values()) if (nodeWord(st, now) === "Updating") return true;
  return false;
}

/** Starts the next waiting job when no Node is updating. Called on every Fleet snapshot. */
export function pump(snap: FleetSnapshot | undefined) {
  lastSnap = snap;
  if (busy(snap)) return;
  const next = jobs.find((j) => j.phase === "waiting");
  if (next) run(next);
}

async function run(j: Job) {
  const c = calls.get(j.id);
  if (!c) return;
  patch(j.id, { phase: "working", started: Date.now(), message: "starting" });
  try {
    await c.start();
  } catch (e) {
    patch(j.id, { phase: "failed", message: String((e as Error).message ?? e) });
    return;
  }
  watch(j.id, j.m, c);
}

// Polls the Node, treats connection failures as "restarting", and finishes
// once the Node answers again (or the update failed without a restart).
function watch(id: number, m: Maintenance, c: Calls) {
  const expectReboot = m.kind === "reboot" || m.reboot;
  let wentDown = false;
  const done = () => {
    patch(id, { phase: "back" });
    setTimeout(() => {
      calls.delete(id);
      set(jobs.filter((x) => x.id !== id));
      pump(lastSnap);
    }, timing.settle);
  };
  const tick = async () => {
    if (!jobs.some((x) => x.id === id)) return;
    try {
      const st = await c.status();
      if (st) {
        // Done once it is idle: back from its restart, or staged without one.
        // A Node back from a restart may still be busy, e.g. downloading.
        if (st.state === UpdateStatus_State.FAILED) return patch(id, { phase: "failed", message: st.message });
        if (st.state === UpdateStatus_State.REBOOTING) patch(id, { phase: "down" });
        else if (st.state !== UpdateStatus_State.UPDATING && (wentDown || !expectReboot)) return done();
        else patch(id, { phase: "working", progress: st.progress, message: st.message || "starting" });
      }
    } catch {
      wentDown = true;
      patch(id, { phase: "down" });
    }
    setTimeout(tick, timing.tick);
  };
  tick();
}

/** For tests: empties the queue. */
export function resetQueue() {
  calls.clear();
  lastSnap = undefined;
  set([]);
}
