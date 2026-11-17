// ADR 0012: how each Node is reached, and Unreachable meaning that neither
// the Console nor the anchor reaches it.
import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { FleetNodeStatusSchema } from "@/gen/maitre/v1/node_pb";
import { NodeSchema } from "@/gen/maitre/v1/fleet_pb";
import { nodeWord, type FleetSnapshot, type NodeStatus } from "./useFleet";
import { createReach, PROBE_MS, wayLine, type Way } from "./reach";

const A = "a".repeat(64);
const B = "b".repeat(64);
const NOW = 1_800_000_000_000;

describe("Node status (ADR 0012)", () => {
  it("is Unreachable when neither the anchor nor the Console reaches it", () => {
    const st = create(FleetNodeStatusSchema, { nodeId: B, reachable: false, lastSeenUnixMs: BigInt(NOW - 120_000) });
    expect(nodeWord(st, NOW)).toBe("Unreachable");
    expect(nodeWord({ ...st, consoleReached: false }, NOW)).toBe("Unreachable");
  });

  it("is Healthy when the anchor has lost it but the Console reaches it", () => {
    const st = create(FleetNodeStatusSchema, { nodeId: B, reachable: false, lastSeenUnixMs: BigInt(NOW - 120_000) });
    expect(nodeWord({ ...st, consoleReached: true }, NOW)).toBe("Healthy");
  });

  it("is Healthy when only the anchor reaches it, through the Relay", () => {
    const st = create(FleetNodeStatusSchema, { nodeId: B, reachable: true, lastSeenUnixMs: BigInt(NOW) });
    expect(nodeWord({ ...st, way: "relayed", consoleReached: false }, NOW)).toBe("Healthy");
  });
});

const C = "c".repeat(64);
const anchor = { nodeId: A, addr: "192.168.4.74:7443" };
const targets = [
  { fingerprint: B, addr: "192.168.4.75:7443" },
  { fingerprint: C, addr: "192.168.4.76:7443" },
];
const heard = (id: string, reachable = true) => create(FleetNodeStatusSchema, { nodeId: id, reachable, lastSeenUnixMs: BigInt(reachable ? NOW : NOW - 120_000) });

/** A Desktop whose dial reached these Nodes these ways, and whose calls to `answers` succeed. */
function desktop(ways: Record<string, Way>, answers: string[]) {
  const probed: string[] = [];
  const reach = createReach({
    wayOf: async (id) => ways[id] ?? null,
    probe: async (t) => {
      probed.push(t.fingerprint);
      if (!answers.includes(t.fingerprint)) throw new Error(`${t.addr} could not be reached: timeout`);
      ways[t.fingerprint] ??= "direct";
    },
  });
  return { reach, probed, settle: () => new Promise((r) => setTimeout(r, 0)) };
}

describe("how each Node is reached", () => {
  it("takes the Desktop's way for each Node, the anchor's own way for the anchor", async () => {
    const { reach, probed } = desktop({ [B]: "direct", [C]: "relayed" }, [B, C]);
    const status = new Map<string, NodeStatus>([A, B, C].map((id) => [id, heard(id)]));
    const out = await reach(anchor, status, targets, NOW);
    expect(out.get(A)).toMatchObject({ way: "direct", wayAddr: anchor.addr, consoleReached: true });
    expect(out.get(B)).toMatchObject({ way: "direct", wayAddr: "192.168.4.75:7443" });
    expect(out.get(C)).toMatchObject({ way: "relayed" });
    expect(out.get(C)?.wayAddr).toBeUndefined();
    // Every Node was reached already and the anchor hears them all: nothing to ask.
    expect(probed).toEqual([]);
  });

  it("asks a Node it has not reached yet, once per while, and then knows its way", async () => {
    const { reach, probed, settle } = desktop({}, [B, C]);
    const status = new Map<string, NodeStatus>([A, B, C].map((id) => [id, heard(id)]));
    expect((await reach(anchor, status, targets, NOW)).get(B)?.way).toBeUndefined();
    await settle();
    expect(probed.sort()).toEqual([B, C]);
    expect((await reach(anchor, status, targets, NOW + 3000)).get(B)?.way).toBe("direct");
    expect(probed).toHaveLength(2);
  });

  it("finds a Node the anchor has lost but this device reaches: not Unreachable", async () => {
    const { reach, settle } = desktop({}, [B]);
    const status = new Map<string, NodeStatus>([
      [A, heard(A)],
      [B, heard(B, false)],
      [C, heard(C, false)],
    ]);
    await reach(anchor, status, targets, NOW);
    await settle();
    const out = await reach(anchor, status, targets, NOW + 3000);
    expect(nodeWord(out.get(B), NOW + 3000)).toBe("Healthy");
    expect(nodeWord(out.get(C), NOW + 3000)).toBe("Unreachable");
  });

  it("asks again while the anchor has lost it, and forgets an answer that stopped coming", async () => {
    const answers = [B];
    const { reach, probed, settle } = desktop({}, answers);
    const status = new Map<string, NodeStatus>([
      [A, heard(A)],
      [B, heard(B, false)],
    ]);
    const only = [targets[0]];
    await reach(anchor, status, only, NOW);
    await settle();
    answers.length = 0;
    await reach(anchor, status, only, NOW + 3000);
    expect(probed).toHaveLength(1);
    await reach(anchor, status, only, NOW + PROBE_MS);
    await settle();
    expect(probed).toHaveLength(2);
    const out = await reach(anchor, status, only, NOW + PROBE_MS + 3000);
    expect(out.get(B)?.consoleReached).toBe(false);
    expect(nodeWord(out.get(B), NOW + PROBE_MS + 3000)).toBe("Unreachable");
  });
});

describe("the way each Node is reached, in words", () => {
  const nodes = [create(NodeSchema, { nodeId: A, name: "Alder", letter: "A" }), create(NodeSchema, { nodeId: B, name: "Boulder", letter: "B" }), create(NodeSchema, { nodeId: C, name: "Cedar", letter: "C" })];
  const snap = (anchorAddr: string, status: [string, Partial<NodeStatus>][]) =>
    ({ anchor: { nodeId: A, addr: anchorAddr }, nodes, status: new Map(status.map(([id, st]) => [id, { ...heard(id, st.reachable ?? true), ...st }])) }) as unknown as FleetSnapshot;

  it("names the anchor's way, a direct way by its address, and the Relay by the anchor", () => {
    const s = snap("100.64.0.5:7443", [
      [A, { way: "direct", wayAddr: "100.64.0.5:7443", consoleReached: true }],
      [B, { way: "direct", wayAddr: "192.168.4.75:7443" }],
      [C, { way: "relayed" }],
    ]);
    expect(wayLine(s, A, NOW)).toBe("over Tailscale");
    expect(wayLine(s, B, NOW)).toBe("at home");
    expect(wayLine(s, C, NOW)).toBe("through Alder");
  });

  it("says a Node no one reaches is unreachable, and one not reached yet nothing", () => {
    const s = snap("192.168.4.74:7443", [
      [B, { reachable: false }],
      [C, {}],
    ]);
    expect(wayLine(s, B, NOW)).toBe("unreachable");
    expect(wayLine(s, C, NOW)).toBeUndefined();
    expect(wayLine(undefined, C, NOW)).toBeUndefined();
  });
});
