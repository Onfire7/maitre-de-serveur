import { describe, expect, it } from "vitest";
import { Code, ConnectError, type Transport } from "@connectrpc/connect";
import { DemoFleet, NODE_A, NODE_B } from "@/demo/fleet";
import { createDemoTransport } from "@/demo/transport";
import { readFleet } from "./useFleet";

// A Node from before ADR 0006: everything but GetFleetStatus and PushStatus.
function older(addr: string, fleet: DemoFleet): Transport {
  const t = createDemoTransport(addr, fleet);
  return {
    ...t,
    unary: (method, ...rest) => {
      if (method.name === "GetFleetStatus" || method.name === "PushStatus") return Promise.reject(new ConnectError(`procedure "/maitre.v1.NodeService/${method.name}" not registered`, Code.Unimplemented));
      return t.unary(method, ...rest);
    },
  } as Transport;
}

describe("readFleet against older Nodes", () => {
  it("falls back to asking each Node, and says so", async () => {
    const fleet = new DemoFleet();
    const snap = await readFleet({ nodeId: NODE_A, addr: "10.0.0.5:7443" }, [], (t) => older(t.addr, fleet));
    expect(snap.legacy).toBe(true);
    expect(snap.nodes.map((n) => n.name)).toEqual(["Alder", "Basalt"]);
    expect(snap.status.get(NODE_A)?.reachable).toBe(true);
    expect(snap.status.get(NODE_B)?.reachable).toBe(true);
    expect(snap.status.get(NODE_B)?.services.map((s) => s.spec?.name).sort()).toEqual(["minecraft", "uptime"]);
    expect(snap.status.get(NODE_A)?.metrics?.cpuPercent).toBeGreaterThan(0);
  });

  it("marks an older Node that does not answer as unreachable without failing the read", async () => {
    const fleet = new DemoFleet();
    fleet.setReachable(NODE_B, false);
    const snap = await readFleet({ nodeId: NODE_A, addr: "10.0.0.5:7443" }, [], (t) => older(t.addr, fleet));
    expect(snap.status.get(NODE_A)?.reachable).toBe(true);
    expect(snap.status.get(NODE_B)?.reachable).toBe(false);
  });

  it("uses GetFleetStatus when the anchor has it", async () => {
    const fleet = new DemoFleet();
    const snap = await readFleet({ nodeId: NODE_A, addr: "10.0.0.5:7443" }, [], (t) => createDemoTransport(t.addr, fleet));
    expect(snap.legacy).toBe(false);
    expect(snap.status.size).toBe(2);
  });
});
