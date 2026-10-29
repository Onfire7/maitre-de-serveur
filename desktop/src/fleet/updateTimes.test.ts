import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { NodeSchema, UpdateRun_Outcome } from "@/gen/maitre/v1/fleet_pb";
import { FleetNodeStatusSchema } from "@/gen/maitre/v1/node_pb";
import { nodeWord, UPDATE_LIMIT_MS } from "./useFleet";
import { averageUpdateMs, formatDuration, updatingLine } from "./updateTimes";

const now = 1_800_000_000_000;

describe("updating from the Node's own record (decision 214)", () => {
  it("is Updating while it cannot be reached, for up to the limit", () => {
    const gone = create(FleetNodeStatusSchema, { nodeId: "a", reachable: false, lastSeenUnixMs: BigInt(now - 5 * 60_000) });
    expect(nodeWord(gone, now)).toBe("Unreachable");
    expect(nodeWord({ ...gone, updatingSince: now - 4 * 60_000 }, now)).toBe("Updating");
    expect(nodeWord({ ...gone, updatingSince: now - UPDATE_LIMIT_MS - 1 }, now)).toBe("Unreachable");
    // Reachable and saying so too.
    const up = create(FleetNodeStatusSchema, { nodeId: "a", reachable: true, lastSeenUnixMs: BigInt(now) });
    expect(nodeWord({ ...up, updatingSince: now - 1000 }, now)).toBe("Updating");
    expect(nodeWord(up, now)).toBe("Healthy");
  });

  it("averages the last five updates that reached the new image", () => {
    const run = (secs: number, outcome = UpdateRun_Outcome.UPDATED) => ({ startedUnixMs: 0n, endedUnixMs: BigInt(secs * 1000), outcome });
    const node = create(NodeSchema, { updates: [run(900), run(60, UpdateRun_Outcome.FAILED), run(240), run(300), run(240), run(300), run(120), run(30, UpdateRun_Outcome.ROLLED_BACK)] });
    expect(averageUpdateMs(node)).toBe(240_000);
    expect(averageUpdateMs(create(NodeSchema, {}))).toBeUndefined();
    expect(updatingLine(node, now - 90_000, now)).toBe("for 2 min · usually about 4 min");
    expect(updatingLine(create(NodeSchema, {}), now - 30_000, now)).toBe("for 30 s");
  });

  it("formats durations", () => {
    expect(formatDuration(45_000)).toBe("45 s");
    expect(formatDuration(4 * 60_000)).toBe("4 min");
    expect(formatDuration(65 * 60_000)).toBe("1 h 5 min");
    expect(formatDuration(120 * 60_000)).toBe("2 h");
  });
});
