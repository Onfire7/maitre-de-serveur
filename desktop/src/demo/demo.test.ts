import { beforeEach, describe, expect, it } from "vitest";
import { createClient } from "@connectrpc/connect";
import { fromBinary } from "@bufbuild/protobuf";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { ServiceService, ServiceStatus_State, ControlServiceRequest_Action } from "@/gen/maitre/v1/service_pb";
import { FilesService } from "@/gen/maitre/v1/files_pb";
import { NodeSchema } from "@/gen/maitre/v1/fleet_pb";
import { DemoFleet, NODE_A, NODE_B } from "./fleet";
import { createDemoTransport } from "./transport";

let fleet: DemoFleet;
const at = (addr: string) => ({
  node: createClient(NodeService, createDemoTransport(addr, fleet)),
  services: createClient(ServiceService, createDemoTransport(addr, fleet)),
  files: createClient(FilesService, createDemoTransport(addr, fleet)),
});
const A = "10.0.0.5:7443";
const B = "10.0.0.6:7443";

beforeEach(() => {
  fleet = new DemoFleet();
});

describe("demo Fleet", () => {
  it("answers GetFleetStatus for every Node from either Node, itself first", async () => {
    const fromA = await at(A).node.getFleetStatus({});
    expect(fromA.nodeId).toBe(NODE_A);
    expect(fromA.nodes.map((n) => n.nodeId)).toEqual([NODE_A, NODE_B]);
    expect(fromA.nodes.every((n) => n.reachable && n.metrics)).toBe(true);
    expect(fromA.nodes[1].services.map((s) => s.spec?.name).sort()).toEqual(["minecraft", "uptime"]);
    const fromB = await at(B).node.getFleetStatus({});
    expect(fromB.nodes[0].nodeId).toBe(NODE_B);
  });

  it("serves the synced records, decodable, from any Node", async () => {
    const recs = (await at(B).node.listRecords({ kind: "node" })).records;
    expect(recs.map((r) => fromBinary(NodeSchema, r.body).name).sort()).toEqual(["Alder", "Basalt"]);
    const all = (await at(A).node.listRecords({})).records;
    expect(new Set(all.map((r) => r.kind))).toEqual(new Set(["fleet", "node", "trusted_device", "space", "drive", "service", "update_policy", "public_settings"]));
  });

  it("makes a switched-off Node unreachable directly and in the anchor's view", async () => {
    fleet.setReachable(NODE_B, false);
    await expect(at(B).node.getNodeInfo({})).rejects.toThrow(/no route to host/);
    fleet.tick(fleet.now + 61_000);
    const fromA = await at(A).node.getFleetStatus({});
    expect(fromA.nodes.find((n) => n.nodeId === NODE_B)?.reachable).toBe(false);
    expect(fromA.nodes.find((n) => n.nodeId === NODE_A)?.reachable).toBe(true);
    fleet.setReachable(NODE_B, true);
    expect((await at(B).node.getNodeInfo({})).node?.name).toBe("Basalt");
  });

  it("runs the script: a Starting Service comes up, a stopped one starts on request", async () => {
    const before = await at(B).services.listServices({});
    expect(before.services.find((s) => s.spec?.name === "uptime")?.state).toBe(ServiceStatus_State.STARTING);
    for (let i = 0; i < 30; i++) fleet.tick(fleet.now + 1000);
    const after = await at(B).services.listServices({});
    const uptime = after.services.find((s) => s.spec?.name === "uptime");
    expect(uptime?.state).toBe(ServiceStatus_State.RUNNING);
    expect(uptime?.healthy).toBe(true);

    const r = await at(A).services.controlService({ name: "paperless", action: ControlServiceRequest_Action.START });
    expect(r.service?.state).toBe(ServiceStatus_State.STARTING);
    for (let i = 0; i < 8; i++) fleet.tick(fleet.now + 1000);
    expect((await at(A).services.listServices({})).services.find((s) => s.spec?.name === "paperless")?.state).toBe(ServiceStatus_State.RUNNING);
  });

  it("installs from the Catalog on the asked Node and lists it Fleet-wide", async () => {
    const r = await at(B).services.installService({ spec: { name: "status", catalogId: "uptime-kuma", image: "x", webPort: 3001, volumes: [{ name: "data", path: "/app/data" }] } });
    expect(r.service?.spec?.nodeId).toBe(NODE_B);
    expect(r.service?.spec?.volumeSpaces).toEqual({ data: "general" });
    const recs = (await at(A).node.listRecords({ kind: "service" })).records;
    expect(recs.some((x) => x.id === "status")).toBe(true);
  });

  it("moves files with a job that finishes and relocates the entry", async () => {
    const before = await at(B).files.listDir({ scope: { driveId: "sda" }, path: "Downloads" });
    expect(before.entries.map((e) => e.name)).toContain("family-reunion.mp4");
    const { jobId } = await at(B).files.move({ from: { driveId: "sda" }, path: "Downloads/family-reunion.mp4", toSpace: "media", toPath: "Movies" });
    for (let i = 0; i < 40; i++) fleet.tick(fleet.now + 1000);
    const jobs = (await at(B).files.listJobs({})).jobs;
    expect(jobs.find((j) => j.jobId === jobId)?.state).toBe("done");
    const movies = await at(A).files.listDir({ scope: { spaceId: "media" }, path: "Movies" });
    expect(movies.entries.map((e) => e.name)).toContain("family-reunion.mp4");
    const after = await at(B).files.listDir({ scope: { driveId: "sda" }, path: "Downloads" });
    expect(after.entries.map((e) => e.name)).not.toContain("family-reunion.mp4");
  });

  it("refuses what the agent would refuse", async () => {
    await expect(at(A).node.renameNode({ name: "basalt" })).rejects.toThrow(/already exists/);
    await expect(at(A).services.controlService({ name: "nope", action: ControlServiceRequest_Action.STOP })).rejects.toThrow(/no Service/);
    await expect(at("10.0.0.99:7443").node.getNodeInfo({})).rejects.toThrow(/nothing answers/);
  });
});
