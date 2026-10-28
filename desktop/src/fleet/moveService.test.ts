import { describe, expect, it } from "vitest";
import { DemoFleet, NODE_A, NODE_B } from "@/demo/fleet";
import { createDemoTransport } from "@/demo/transport";
import { planMove, specFor } from "./moveService";
import { readFleet, type FleetService, type FleetSnapshot } from "./useFleet";

async function setup() {
  const fleet = new DemoFleet();
  const snap = { ...(await readFleet({ nodeId: NODE_A, addr: "10.0.0.5:7443" }, [], (t) => createDemoTransport(t.addr, fleet))), anchor: { nodeId: NODE_A, addr: "10.0.0.5:7443" }, at: Date.now() } as FleetSnapshot;
  const svc = (name: string) => ({ spec: snap.specs.find((s) => s.name === name)!, word: "Online", nodeUnreachable: false }) as FleetService;
  return { snap, svc };
}

describe("planMove (decision 191)", () => {
  it("copies folders in a Space each Node pools on its own, and keeps those in a spanning Space", async () => {
    const { snap, svc } = await setup();
    const jellyfin = svc("jellyfin");
    const plan = planMove(snap, jellyfin, NODE_B);
    expect(plan.blockers).toEqual([]);
    const byName = Object.fromEntries(plan.volumes.map((v) => [v.volume, v]));
    // General is not spanning: Basalt's General Pool is a different one.
    for (const v of plan.volumes.filter((v) => v.fromSpace === "general")) expect(v.copy).toBe(true);
    // Media spans both Nodes: Basalt reaches the same folder.
    for (const v of plan.volumes.filter((v) => v.fromSpace === "media")) expect(byName[v.volume].copy).toBe(false);
  });

  it("lets a volume go to another Space, and the new spec carries it with the Node's own parts cleared", async () => {
    const { snap, svc } = await setup();
    const uptime = svc("uptime");
    const vol = uptime.spec.volumes[0].name;
    const plan = planMove(snap, uptime, NODE_A, { [vol]: "media" });
    expect(plan.volumes[0]).toMatchObject({ fromSpace: "general", toSpace: "media", copy: true });
    const spec = specFor(uptime.spec, plan);
    expect(spec.volumeSpaces[vol]).toBe("media");
    expect(spec.nodeId).toBe("");
    expect(spec.webHostPort).toBe(0);
    expect(uptime.spec.volumeSpaces[vol]).toBe("general");
  });

  it("refuses when a picked folder's Space is out of the new Node's reach, and warns for infrastructure", async () => {
    const { snap, svc } = await setup();
    const jellyfin = svc("jellyfin");
    const lonely = { ...snap, spaces: snap.spaces.map((sp) => (sp.spaceId === "media" ? { ...sp, spanNodes: false } : sp)), drives: snap.drives.filter((d) => !(d.nodeId === NODE_B)) } as FleetSnapshot;
    expect(Object.keys(jellyfin.spec.volumeFolders)).not.toHaveLength(0);
    expect(planMove(lonely, jellyfin, NODE_B).blockers.join(" ")).toMatch(/cannot reach/);
    const adguard = svc("adguard");
    const infra = { ...adguard, spec: { ...adguard.spec, infrastructure: true } } as FleetService;
    expect(planMove(snap, infra, NODE_B).warnings.join(" ")).toMatch(/network infrastructure/);
  });
});
