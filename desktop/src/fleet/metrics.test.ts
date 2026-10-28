import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, type Transport } from "@connectrpc/connect";
import { MetricSeriesSchema } from "@/gen/maitre/v1/node_pb";
import { DemoFleet } from "@/demo/fleet";
import { createDemoTransport } from "@/demo/transport";
import { bandPath, hasReadings, linePath, niceMax, pointsOf, rangeQuery, readHistory, refetchMs, RANGES } from "./metrics";

// A Node from before decision 210: everything but the history calls.
function older(addr: string, fleet: DemoFleet, code = Code.Unimplemented): Transport {
  const t = createDemoTransport(addr, fleet);
  return {
    ...t,
    unary: (method, ...rest) => {
      if (method.name === "GetMetricsHistory") return Promise.reject(new ConnectError(`procedure "/maitre.v1.NodeService/${method.name}" not registered`, code));
      return t.unary(method, ...rest);
    },
  } as Transport;
}

describe("metrics history ranges (decision 210)", () => {
  it("asks from the range's start up to now, about a point per pixel", () => {
    const now = 1_000_000_000_000;
    expect(rangeQuery("1h", now)).toEqual({ fromUnixMs: BigInt(now - 3600_000), toUnixMs: 0n, points: 300 });
    expect(rangeQuery("7d", now, 120).fromUnixMs).toBe(BigInt(now - 7 * 86400_000));
    expect(rangeQuery("1y", now).fromUnixMs).toBe(BigInt(now - 365 * 86400_000));
    expect(RANGES.map((r) => r.label)).toEqual(["1 hour", "1 day", "7 days", "30 days", "1 year"]);
  });

  it("refreshes every 10 s for the last hour and every minute beyond", () => {
    expect(refetchMs("1h")).toBe(10_000);
    for (const r of ["1d", "7d", "30d", "1y"] as const) expect(refetchMs(r)).toBe(60_000);
  });
});

describe("drawing history", () => {
  it("breaks the line where there is no reading instead of dropping to zero", () => {
    const d = linePath([
      { x: 0, y: 10 },
      { x: 1, y: 20 },
      { x: 2, y: NaN },
      { x: 3, y: 30 },
      { x: 4, y: 40 },
    ]);
    expect(d).toBe("M0.0,10.0L1.0,20.0M3.0,30.0L4.0,40.0");
    expect(d).not.toContain(",0.0");
  });

  it("draws a reading alone between gaps as a dot, and nothing for no readings", () => {
    expect(linePath([{ x: 0, y: NaN }, { x: 1, y: 5 }, { x: 2, y: NaN }])).toBe("M1.0,5.0L1.0,5.0");
    expect(linePath([{ x: 0, y: NaN }])).toBe("");
  });

  it("closes the highest-readings band per unbroken run", () => {
    const d = bandPath([
      { x: 0, lo: 10, hi: 5 },
      { x: 1, lo: 10, hi: 5 },
      { x: 2, lo: NaN, hi: NaN },
      { x: 3, lo: 10, hi: 5 },
    ]);
    expect(d).toBe("M0.0,5.0L1.0,5.0L1.0,10.0L0.0,10.0Z");
  });

  it("places points mid-step, keeps NaN, and rounds the axis top", () => {
    const ps = pointsOf(create(MetricSeriesSchema, { name: "node/cpu", startUnixMs: 1000n, stepMs: 10n, avg: [1, NaN], max: [2, NaN] }));
    expect(ps[0]).toEqual({ t: 1005, v: 1, max: 2 });
    expect(ps[1].t).toBe(1015);
    expect(Number.isNaN(ps[1].v)).toBe(true);
    expect(hasReadings([{ t: 0, v: 0, max: 0 }, { t: 1, v: NaN, max: NaN }])).toBe(false);
    expect([niceMax(0), niceMax(3.2), niceMax(7), niceMax(1.4e6)]).toEqual([1, 5, 10, 2e6]);
  });
});

describe("readHistory", () => {
  it("returns the series a Node keeps", async () => {
    const fleet = new DemoFleet();
    const h = await readHistory(createDemoTransport("10.0.0.5:7443", fleet), [], "1d", fleet.now);
    expect(h.supported).toBe(true);
    if (!h.supported) return;
    expect([...h.series.keys()]).toEqual(expect.arrayContaining(["node/cpu", "node/net_rx", "drive/sda"]));
    expect([...h.series.keys()].some((k) => k.startsWith("service/"))).toBe(false);
    expect(h.to - h.from).toBe(86400_000);
    const svc = await readHistory(createDemoTransport("10.0.0.6:7443", fleet), ["service/minecraft/"], "1h", fleet.now);
    expect(svc.supported && [...svc.series.keys()].sort()).toEqual(["service/minecraft/cpu", "service/minecraft/disk_read", "service/minecraft/disk_write", "service/minecraft/memory"]);
  });

  it("says a Node from before keeps no history rather than failing, and passes other errors on", async () => {
    const fleet = new DemoFleet();
    expect(await readHistory(older("10.0.0.5:7443", fleet), ["node/cpu"], "1h")).toEqual({ supported: false });
    await expect(readHistory(older("10.0.0.5:7443", fleet, Code.Unavailable), ["node/cpu"], "1h")).rejects.toThrow();
  });
});
