import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { FleetNodeStatusSchema, NodeMetricsSchema } from "@/gen/maitre/v1/node_pb";
import { recordMetrics, samples } from "./history";
import type { FleetSnapshot } from "./useFleet";

const snap = (t: number, cpu: number) => ({ status: new Map([["n1", create(FleetNodeStatusSchema, { nodeId: "n1", metricsUnixMs: BigInt(t), metrics: create(NodeMetricsSchema, { cpuPercent: cpu, memoryTotalBytes: 1000n, memoryUsedBytes: 250n }) })]]) }) as unknown as FleetSnapshot;

describe("metrics history (decision 209)", () => {
  it("keeps one sample per report, replaces one whose numbers changed, and hands out a new list each time", () => {
    recordMetrics(snap(1000, 10));
    const first = samples("n1");
    recordMetrics(snap(1000, 10));
    expect(samples("n1")).toBe(first);
    recordMetrics(snap(1000, 30));
    expect(samples("n1")).not.toBe(first);
    expect(samples("n1")).toEqual([{ t: 1000, cpu: 30, memory: 25 }]);
    recordMetrics(snap(4000, 50));
    recordMetrics(snap(2000, 99)); // older than the last: ignored
    expect(samples("n1").map((s) => s.cpu)).toEqual([30, 50]);
  });
});
