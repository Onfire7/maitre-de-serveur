import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { UpdateStatus_State, type UpdateStatus } from "@/gen/maitre/v1/node_pb";
import type { FleetSnapshot } from "./useFleet";
import { enqueue, pump, queued, remove, resetQueue, timing } from "./updateQueue";

const idle = { state: UpdateStatus_State.IDLE, progress: 0, message: "" } as UpdateStatus;
const wait = (ms = 30) => new Promise((r) => setTimeout(r, ms));

function snapWith(updating: string[]): FleetSnapshot {
  const status = new Map(["a", "b", "c"].map((id) => [id, { nodeId: id, reachable: true, lastSeenUnixMs: BigInt(Date.now()), updateState: updating.includes(id) ? 2 : 0, problems: [] }]));
  return { status } as unknown as FleetSnapshot;
}

/** A Node that stages an update without restarting: idle again when its update is done. */
function node(log: string[], id: string, ok = true) {
  let done = false;
  return {
    start: async () => {
      log.push(`start ${id}`);
      if (!ok) throw new Error("no space left");
      setTimeout(() => (done = true), 20);
    },
    status: async () => (done ? idle : ({ state: UpdateStatus_State.UPDATING, progress: 0.5, message: "downloading" } as UpdateStatus)),
  };
}

const update = { kind: "update" as const, target: "img", reboot: false };

beforeEach(() => {
  timing.tick = 5;
  timing.settle = 5;
  resetQueue();
});
afterEach(() => resetQueue());

describe("the update queue", () => {
  it("runs one Node at a time, in the order asked", async () => {
    const log: string[] = [];
    pump(snapWith([]));
    enqueue("a", "A", update, node(log, "a"));
    enqueue("b", "B", update, node(log, "b"));
    expect(log).toEqual(["start a"]);
    expect(queued().map((j) => j.phase)).toEqual(["working", "waiting"]);
    await wait(80);
    expect(log).toEqual(["start a", "start b"]);
    await wait(80);
    expect(queued()).toEqual([]);
  });

  it("waits while the Fleet reports a Node updating", async () => {
    const log: string[] = [];
    pump(snapWith(["c"]));
    enqueue("a", "A", update, node(log, "a"));
    expect(log).toEqual([]);
    pump(snapWith([]));
    expect(log).toEqual(["start a"]);
  });

  it("holds the rest after a failure until it is dismissed", async () => {
    const log: string[] = [];
    pump(snapWith([]));
    enqueue("a", "A", update, node(log, "a", false));
    enqueue("b", "B", update, node(log, "b"));
    await wait();
    expect(queued().map((j) => j.phase)).toEqual(["failed", "waiting"]);
    expect(queued()[0].message).toBe("no space left");
    pump(snapWith([]));
    expect(log).toEqual(["start a"]);
    remove(queued()[0].id);
    expect(log).toEqual(["start a", "start b"]);
  });

  it("keeps the Node's typical times, asked before it goes down, on its job", () => {
    pump(snapWith(["c"]));
    enqueue("a", "A", { kind: "reboot" }, node([], "a"), { updateMs: 262_000, downtimeMs: 95_000 });
    enqueue("b", "B", update, node([], "b"));
    expect(queued().map((j) => j.typical)).toEqual([{ updateMs: 262_000, downtimeMs: 95_000 }, { updateMs: 0, downtimeMs: 0 }]);
  });

  it("cancels a waiting job and asks a Node only once", async () => {
    const log: string[] = [];
    pump(snapWith(["c"]));
    enqueue("a", "A", update, node(log, "a"));
    enqueue("a", "A", update, node(log, "a"));
    expect(queued()).toHaveLength(1);
    remove(queued()[0].id);
    pump(snapWith([]));
    expect(log).toEqual([]);
  });
});

describe("a Node back from its restart", () => {
  it("is done only once it is no longer updating", async () => {
    const log: string[] = [];
    let calls = 0;
    // Down for two polls, then back but still downloading for a while, then idle.
    const steps = ["down", "down", ...Array(8).fill("updating"), "idle"];
    pump(snapWith([]));
    enqueue("a", "A", { kind: "update", target: "img", reboot: true }, {
      start: async () => void log.push("start a"),
      status: async () => {
        const s = steps[Math.min(calls++, steps.length - 1)];
        if (s === "down") throw new Error("connection refused");
        return s === "idle" ? idle : ({ state: UpdateStatus_State.UPDATING, progress: 0.4, message: "downloading" } as UpdateStatus);
      },
    });
    enqueue("b", "B", update, node(log, "b"));
    while (calls < 4) await wait(2);
    expect(queued()[0].phase).toBe("working");
    expect(log).toEqual(["start a"]);
    await wait(150);
    expect(log).toEqual(["start a", "start b"]);
  });
});

describe("the restart's start", () => {
  it("is stamped on the job when the Node first says it is restarting, and kept while it is down", async () => {
    let calls = 0;
    // Staging for two polls, then restarting, then unreachable for a few polls, then idle.
    const steps = ["updating", "updating", "rebooting", "down", "down", "down", "idle"];
    pump(snapWith([]));
    enqueue("a", "A", { kind: "update", target: "img", reboot: true }, {
      start: async () => {},
      status: async () => {
        const s = steps[Math.min(calls++, steps.length - 1)];
        if (s === "down") throw new Error("connection refused");
        if (s === "rebooting") return { state: UpdateStatus_State.REBOOTING, progress: 1, message: "" } as UpdateStatus;
        return s === "idle" ? idle : ({ state: UpdateStatus_State.UPDATING, progress: 0.4, message: "downloading" } as UpdateStatus);
      },
    });
    expect(queued()[0].restarted).toBeNull();
    while (calls < 2) await wait(2);
    expect(queued()[0].restarted).toBeNull();
    const before = Date.now();
    while (calls < 4) await wait(2);
    const stamp = queued()[0].restarted!;
    expect(stamp).toBeGreaterThanOrEqual(before - 50);
    expect(stamp).toBeLessThanOrEqual(Date.now());
    while (calls < 6) await wait(2);
    expect(queued()[0].restarted).toBe(stamp);
  });

  it("is stamped when the Node stops answering without saying so first", async () => {
    let calls = 0;
    pump(snapWith([]));
    enqueue("a", "A", { kind: "update", target: "img", reboot: true }, {
      start: async () => {},
      status: async () => {
        if (calls++ < 3) throw new Error("connection refused");
        return idle;
      },
    });
    while (calls < 1) await wait(2);
    await wait(2);
    expect(queued()[0].restarted).toEqual(expect.any(Number));
  });
});
