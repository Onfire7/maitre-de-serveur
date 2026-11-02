import { describe, expect, it } from "vitest";
import { allPicks, attemptKey, pickAnchor } from "./anchor";

const nodes = [
  { nodeId: "b", letter: "B", addresses: ["10.0.0.6:7443", "100.64.0.6:7443"] },
  { nodeId: "a", letter: "A", addresses: ["10.0.0.5:7443", "alder.local:7443", "100.64.0.5:7443"] },
  { nodeId: "c", letter: "C", addresses: [] },
];
const failed = (...picks: { nodeId: string; addr: string }[]) => new Set(picks.map(attemptKey));

describe("pickAnchor", () => {
  it("keeps the last anchor while it answers", () => {
    expect(pickAnchor(nodes, { nodeId: "b", addr: "10.0.0.6:7443" })).toEqual({ nodeId: "b", addr: "10.0.0.6:7443" });
  });
  it("starts with the first letter, skipping Nodes without an address", () => {
    expect(pickAnchor(nodes, undefined)).toEqual({ nodeId: "a", addr: "10.0.0.5:7443" });
    expect(pickAnchor([nodes[2]], undefined)).toBeUndefined();
  });
  it("tries the same Node's other ways before the next Node (decision 284)", () => {
    const a1 = { nodeId: "a", addr: "10.0.0.5:7443" };
    expect(pickAnchor(nodes, a1, failed(a1))).toEqual({ nodeId: "a", addr: "alder.local:7443" });
    const a2 = { nodeId: "a", addr: "alder.local:7443" };
    expect(pickAnchor(nodes, a2, failed(a1, a2))).toEqual({ nodeId: "a", addr: "100.64.0.5:7443" });
  });
  it("fails over to the next Node once every way to the last one failed, and wraps when all failed", () => {
    const aAll = nodes[1].addresses.map((addr) => ({ nodeId: "a", addr }));
    expect(pickAnchor(nodes, aAll[2], failed(...aAll))).toEqual({ nodeId: "b", addr: "10.0.0.6:7443" });
    const bAll = nodes[0].addresses.map((addr) => ({ nodeId: "b", addr }));
    expect(pickAnchor(nodes, bAll[1], failed(...aAll, ...bAll))).toEqual({ nodeId: "a", addr: "10.0.0.5:7443" });
  });
  it("keeps a known-good address of the same Node, the Tailscale one too", () => {
    expect(pickAnchor(nodes, { nodeId: "a", addr: "100.64.0.5:7443" })).toEqual({ nodeId: "a", addr: "100.64.0.5:7443" });
  });
  it("drops a remembered way the Node no longer lists", () => {
    expect(pickAnchor(nodes, { nodeId: "a", addr: "10.9.9.9:7443" })).toEqual({ nodeId: "a", addr: "10.0.0.5:7443" });
  });
  it("lists every way by letter", () => {
    expect(allPicks(nodes).map(attemptKey)).toEqual(["a@10.0.0.5:7443", "a@alder.local:7443", "a@100.64.0.5:7443", "b@10.0.0.6:7443", "b@100.64.0.6:7443"]);
  });
});
