import { describe, expect, it } from "vitest";
import { nodeAddr, typedNode, withTyped } from "./address";
import type { Found } from "@/state";

describe("nodeAddr", () => {
  it("adds the Node's port", () => {
    expect(nodeAddr(" 192.168.1.20 ")).toBe("192.168.1.20:7443");
    expect(nodeAddr("nas.lan")).toBe("nas.lan:7443");
    expect(nodeAddr("fe80::1")).toBe("[fe80::1]:7443");
  });
  it("keeps a port that was given", () => {
    expect(nodeAddr("10.0.0.5:8443")).toBe("10.0.0.5:8443");
    expect(nodeAddr("[fd00::5]:9000")).toBe("[fd00::5]:9000");
    expect(nodeAddr("https://nas.lan:7443/")).toBe("nas.lan:7443");
  });
  it("refuses what is not an address", () => {
    for (const s of ["", "  ", "a b", "10.0.0.5:99999", "10.0.0.5:x", "-bad"]) expect(nodeAddr(s)).toBe("");
  });
});

describe("withTyped", () => {
  const found: Found = { ...typedNode("10.0.0.5:7443", "aa"), name: "Avalon", recovery: false };
  it("adds a Node discovery did not find", () => {
    expect(withTyped([found], [typedNode("10.0.0.6:7443", "bb")]).map((f) => f.id)).toEqual(["aa", "bb"]);
  });
  it("gives a found Node the typed address first", () => {
    const out = withTyped([found], [typedNode("192.168.1.5:7443", "aa")]);
    expect(out).toHaveLength(1);
    expect(out[0].name).toBe("Avalon");
    expect(out[0].addresses).toEqual(["192.168.1.5:7443", "10.0.0.5:7443"]);
    expect(found.addresses).toEqual(["10.0.0.5:7443"]);
  });
});
