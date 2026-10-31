import { describe, expect, it } from "vitest";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { FleetNodeStatusSchema, type FleetNodeStatus } from "@/gen/maitre/v1/node_pb";
import { DriveRecordSchema, SpaceSchema } from "@/gen/maitre/v1/storage_pb";
import { coverage, maxCopies, nextSync, scheduleWords } from "./protection";

const spaces = [
  create(SpaceSchema, { spaceId: "media", name: "Media", requireTags: ["hdd"], parityLevel: 1, spanNodes: true, priority: 10 }),
  create(SpaceSchema, { spaceId: "general", name: "General", priority: 0 }),
  create(SpaceSchema, { spaceId: "apps", name: "Apps", mirrored: true, priority: 5 }),
];
const drives = [
  create(DriveRecordSchema, { driveId: "a1", nodeId: "A", name: "Atlas", autoTags: ["hdd"] }),
  create(DriveRecordSchema, { driveId: "a2", nodeId: "A", name: "Aspen", autoTags: ["hdd"] }),
  create(DriveRecordSchema, { driveId: "ap", nodeId: "A", name: "Arbor", role: "parity" }),
  create(DriveRecordSchema, { driveId: "b1", nodeId: "B", name: "Basin", autoTags: ["hdd"] }),
  create(DriveRecordSchema, { driveId: "bm", nodeId: "B", name: "Bluff", role: "mirrored", autoTags: ["ssd"] }),
];
const st = (init: MessageInitShape<typeof FleetNodeStatusSchema>) => create(FleetNodeStatusSchema, init);

describe("coverage", () => {
  it("reads parity from the gossip and the records", () => {
    const a = coverage(spaces[0], "A", spaces, drives, st({ parity: { parityDriveIds: ["ap"], lastSyncUnixMs: 5n, job: "", progress: 0, missingDriveIds: [], held: "" } }), "Alder")!;
    expect(a.word).toBe("Protected");
    expect(a.why).toBe("survives 1 failed Drive");
    // Without gossip, the records say Arbor is a Parity Drive.
    expect(coverage(spaces[0], "A", spaces, drives, undefined, "Alder")!.word).toBe("Protected");
    const b = coverage(spaces[0], "B", spaces, drives, st({}), "Basalt")!;
    expect(b.word).toBe("Not protected");
    expect(b.why).toBe("parity 0 of 1: no Parity Drive on Basalt. New files go to Nodes that have one");
  });
  it("tells held, building, rebuilding and missing apart", () => {
    const held = st({ parity: { parityDriveIds: ["ap"], lastSyncUnixMs: 5n, held: "6% deleted", job: "", progress: 0, missingDriveIds: [] } });
    expect(coverage(spaces[0], "A", spaces, drives, held, "Alder")!.word).toBe("Held");
    const building = st({ parity: { parityDriveIds: ["ap"], lastSyncUnixMs: 0n, job: "sync", progress: 0.4, held: "", missingDriveIds: [] } });
    expect(coverage(spaces[0], "A", spaces, drives, building, "Alder")!.why).toBe("First parity update, 40%");
    const fixing = st({ parity: { parityDriveIds: ["ap"], lastSyncUnixMs: 5n, job: "fix", jobDriveId: "a1", progress: 0.1, held: "", missingDriveIds: [] } });
    expect(coverage(spaces[0], "A", spaces, drives, fixing, "Alder")!.word).toBe("Rebuilding");
    const missing = st({ parity: { parityDriveIds: ["ap"], lastSyncUnixMs: 5n, job: "", progress: 0, held: "", missingDriveIds: ["a1"] } });
    const m = coverage(spaces[0], "A", spaces, drives, missing, "Alder")!;
    expect(m.word).toBe("Drive missing");
    expect(m.why).toContain("Atlas stopped answering");
  });
  it("treats a Mirrored Space by its disks, and Off as off", () => {
    expect(coverage(spaces[2], "B", spaces, drives, st({}), "Basalt")!.word).toBe("Protected");
    const lost = st({ mirrors: [{ driveId: "bm", missingDisk: "S2", errors: 0n, replacing: -1 }] });
    expect(coverage(spaces[2], "B", spaces, drives, lost, "Basalt")!.why).toContain("S2 is gone");
    expect(coverage(spaces[2], "A", spaces, drives, st({}), "Alder")).toBeUndefined();
    expect(coverage(spaces[1], "A", spaces, drives, st({}), "Alder")!.word).toBe("Off");
  });
  it("counts copies from the gossip", () => {
    const two = create(SpaceSchema, { ...spaces[1], copies: 2 });
    const ok = st({ copies: [{ spaceId: "general", shortFiles: 0, maxCopies: 2, lastPassUnixMs: 5n, running: false }] });
    expect(coverage(two, "A", spaces, drives, ok, "Alder")!.why).toBe("2 copies");
    const short = st({ copies: [{ spaceId: "general", shortFiles: 12, maxCopies: 1, lastPassUnixMs: 5n, running: false }] });
    expect(coverage(two, "A", spaces, drives, short, "Alder")!.why).toBe("12 files have fewer than 2 copies: 1 Drive on separate disks");
  });
});

describe("maxCopies", () => {
  it("counts separate disks per Node, across the Fleet when spanning", () => {
    const status = new Map<string, FleetNodeStatus>();
    expect(maxCopies(spaces[0], spaces, drives, status)).toBe(3); // a1, a2 and b1 across Nodes
    expect(maxCopies(spaces[1], spaces, drives, status)).toBe(2); // best Node: A with a1, a2
    status.set("A", st({ copies: [{ spaceId: "general", shortFiles: 0, maxCopies: 1, lastPassUnixMs: 0n, running: false }] }));
    expect(maxCopies(spaces[1], spaces, drives, status)).toBe(1); // A says its two Drives share a disk
  });
});

describe("schedule", () => {
  it("speaks and computes", () => {
    expect(scheduleWords({ syncTime: "", syncDays: [], scrubEveryDays: 0 })).toBe("every night at 03:00, scrub every week");
    expect(scheduleWords({ syncTime: "02:30", syncDays: [1, 4], scrubEveryDays: 14 })).toBe("on Mon, Thu at 02:30, scrub every two weeks");
    const sat = new Date(2026, 9, 31, 12, 0);
    expect(nextSync({ syncTime: "", syncDays: [] }, sat).getTime()).toBe(new Date(2026, 10, 1, 3, 0).getTime());
    expect(nextSync({ syncTime: "02:00", syncDays: [1, 4] }, sat).getTime()).toBe(new Date(2026, 10, 2, 2, 0).getTime());
  });
});
