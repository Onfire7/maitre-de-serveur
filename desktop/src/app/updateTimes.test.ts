// How long a Node's updates take, as the Console says it: the Node works
// out the figures (GetUpdateStatus); the Console only words them.
import { describe, expect, it } from "vitest";
import { updateTimesLine } from "./updateTimes";

const min = 60_000;

describe("the line saying how long a Node's updates take", () => {
  it("rounds the Node's figures to whole minutes", () => {
    expect(updateTimesLine({ typicalUpdateMs: BigInt(6 * min + 20_000), typicalDowntimeMs: BigInt(2 * min - 25_000) })).toBe("Updates usually take about 6 minutes; its Services are down for about 2.");
    expect(updateTimesLine({ typicalUpdateMs: BigInt(5 * min + 40_000), typicalDowntimeMs: BigInt(70_000) })).toBe("Updates usually take about 6 minutes; its Services are down for about 1.");
  });
  it("says under a minute below one", () => {
    expect(updateTimesLine({ typicalUpdateMs: BigInt(50_000), typicalDowntimeMs: BigInt(30_000) })).toBe("Updates usually take under a minute; its Services are down for under a minute.");
    expect(updateTimesLine({ typicalUpdateMs: BigInt(3 * min), typicalDowntimeMs: BigInt(40_000) })).toBe("Updates usually take about 3 minutes; its Services are down for under a minute.");
  });
  it("says a few minutes without a figure", () => {
    expect(updateTimesLine({ typicalUpdateMs: 0n, typicalDowntimeMs: 0n })).toBe("Updates usually take a few minutes; its Services are down for a few minutes.");
    // Older runs that never recorded the restart: a total, no downtime.
    expect(updateTimesLine({ typicalUpdateMs: BigInt(6 * min), typicalDowntimeMs: 0n })).toBe("Updates usually take about 6 minutes; its Services are down for a few minutes.");
  });
  it("says the Services keep running when it is not to restart", () => {
    expect(updateTimesLine({ typicalUpdateMs: BigInt(6 * min), typicalDowntimeMs: BigInt(2 * min) }, false)).toBe("Updates usually take about 6 minutes; its Services keep running until it next restarts.");
    expect(updateTimesLine({ typicalUpdateMs: 0n, typicalDowntimeMs: 0n }, false)).toBe("Updates usually take a few minutes; its Services keep running until it next restarts.");
  });
});
