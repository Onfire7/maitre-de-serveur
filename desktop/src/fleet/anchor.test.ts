import { describe, expect, it } from "vitest";
import { pickAnchor } from "./anchor";

const nodes = [
  { nodeId: "b", letter: "B", addresses: ["10.0.0.6:7443"] },
  { nodeId: "a", letter: "A", addresses: ["10.0.0.5:7443", "alder.local:7443"] },
  { nodeId: "c", letter: "C", addresses: [] },
];

describe("pickAnchor", () => {
  it("keeps the last anchor while it answers", () => {
    expect(pickAnchor(nodes, { nodeId: "b", addr: "10.0.0.6:7443" })).toEqual({ nodeId: "b", addr: "10.0.0.6:7443" });
  });
  it("starts with the first letter, skipping Nodes without an address", () => {
    expect(pickAnchor(nodes, undefined)).toEqual({ nodeId: "a", addr: "10.0.0.5:7443" });
    expect(pickAnchor([nodes[2]], undefined)).toBeUndefined();
  });
  it("fails over past the Nodes that did not answer, and wraps when all failed", () => {
    expect(pickAnchor(nodes, { nodeId: "a", addr: "10.0.0.5:7443" }, new Set(["a"]))).toEqual({ nodeId: "b", addr: "10.0.0.6:7443" });
    expect(pickAnchor(nodes, { nodeId: "b", addr: "10.0.0.6:7443" }, new Set(["a", "b"]))).toEqual({ nodeId: "a", addr: "10.0.0.5:7443" });
  });
  it("keeps a known-good address of the same Node", () => {
    expect(pickAnchor(nodes, { nodeId: "a", addr: "alder.local:7443" })).toEqual({ nodeId: "a", addr: "alder.local:7443" });
  });
});
