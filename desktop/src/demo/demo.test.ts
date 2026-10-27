import { beforeEach, describe, expect, it } from "vitest";
import { createClient } from "@connectrpc/connect";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { ServiceService, ServiceStatus_State, ControlServiceRequest_Action } from "@/gen/maitre/v1/service_pb";
import { FilesService } from "@/gen/maitre/v1/files_pb";
import { NodeSchema } from "@/gen/maitre/v1/fleet_pb";
import { InstallerService, Provisioning_Phase } from "@/gen/maitre/v1/installer_pb";
import { gapLine, nodeDown, nodeWord, provisioningLine, syncGaps } from "@/fleet/useFleet";
import { notices } from "@/app/notices";
import { DemoFleet, INSTALLER_ID, NODE_A, NODE_B } from "./fleet";
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
    expect(new Set(all.map((r) => r.kind))).toEqual(new Set(["fleet", "node", "trusted_device", "space", "drive", "service", "update_policy", "tailscale", "public_settings"]));
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
    const { jobId } = await at(B).files.move({ from: { driveId: "sda" }, path: "Downloads/family-reunion.mp4", toSpace: "media", toPath: "Movies/family-reunion.mp4" });
    for (let i = 0; i < 40; i++) fleet.tick(fleet.now + 1000);
    const jobs = (await at(B).files.listJobs({})).jobs;
    expect(jobs.find((j) => j.jobId === jobId)?.state).toBe("done");
    const movies = await at(A).files.listDir({ scope: { spaceId: "media" }, path: "Movies" });
    expect(movies.entries.map((e) => e.name)).toContain("family-reunion.mp4");
    const after = await at(B).files.listDir({ scope: { driveId: "sda" }, path: "Downloads" });
    expect(after.entries.map((e) => e.name)).not.toContain("family-reunion.mp4");
  });

  it("reads a Node's logs from listed sources only", async () => {
    const r = await at(B).node.getNodeLogs({});
    expect(r.sources[0].id).toBe("agent");
    expect(r.sources.map((x) => x.id)).toContain("service:minecraft");
    expect(r.lines.length).toBeGreaterThan(0);
    await expect(at(B).node.getNodeLogs({ source: "sshd.service" })).rejects.toThrow(/no log source/);
  });

  it("deletes as a job, but never a folder a Service uses", async () => {
    await expect(at(A).files.delete({ scope: { spaceId: "general" }, names: ["jellyfin"] })).rejects.toThrow(/jellyfin uses general\/jellyfin\/config/);
    const { jobId } = await at(A).files.delete({ scope: { spaceId: "media" }, folder: "Documents", names: ["notes.txt"] });
    for (let i = 0; i < 5; i++) fleet.tick(fleet.now + 1000);
    expect((await at(A).files.listJobs({})).jobs.find((j) => j.jobId === jobId)?.kind).toBe("delete");
    const docs = await at(A).files.listDir({ scope: { spaceId: "media" }, path: "Documents" });
    expect(docs.entries.map((e) => e.name)).not.toContain("notes.txt");
  });

  it("refuses what the agent would refuse", async () => {
    await expect(at(A).node.renameNode({ name: "basalt" })).rejects.toThrow(/already exists/);
    await expect(at(A).services.controlService({ name: "nope", action: ControlServiceRequest_Action.STOP })).rejects.toThrow(/no Service/);
    await expect(at("10.0.0.99:7443").node.getNodeInfo({})).rejects.toThrow(/nothing answers/);
  });
});

describe("ADR 0007", () => {
  it("a Node with problems is reachable and needs attention, not Unreachable", async () => {
    fleet.setProblems(NODE_B, ["runs Maître Server 95c3d8a, which cannot answer PushStatus; update it"]);
    const st = (await at(A).node.getFleetStatus({})).nodes.find((n) => n.nodeId === NODE_B);
    expect(st?.reachable).toBe(true);
    expect(nodeWord(st, fleet.now)).toBe("Attention");
    expect(nodeDown(nodeWord(st, fleet.now))).toBe(false);
  });
});

describe("decision 168", () => {
  it("reports an out-of-sync Node once, from whichever side saw it", async () => {
    const g = { nodeId: NODE_B, aheadNodeId: NODE_A, count: 2, records: ["service jellyfin", "service uptime-kuma"] };
    fleet.setSyncGaps([g]);
    const res = await at(A).node.getFleetStatus({});
    const snap = { anchor: { nodeId: NODE_A, addr: A }, nodes: [{ nodeId: NODE_A, name: "Alder" }, { nodeId: NODE_B, name: "Basalt" }], status: new Map(res.nodes.map((n) => [n.nodeId, n])), spaces: [{ defaultForNew: true }] } as never;
    const gaps = syncGaps(snap);
    expect(gaps).toHaveLength(1);
    expect(gapLine(gaps[0])).toBe("2 records (service jellyfin, service uptime-kuma)");
    const n = notices(snap, [], null).find((x) => x.id.startsWith("out-of-sync"));
    expect(n?.title).toBe("Basalt is out of sync");
    expect(n?.detail).toContain("behind Alder on 2 records");
  });
});

describe("a Node being installed", () => {
  it("is Provisioning in every Console, from the anchor, until it talks", async () => {
    const ins = fleet.installer;
    await createClient(InstallerService, createDemoTransport(ins.addr, fleet)).provision({ nodeName: "Cedar", targetDisk: "/dev/nvme0n1" });
    // The Console that started it puts the record through a sponsor.
    const body = toBinary(NodeSchema, create(NodeSchema, { nodeId: INSTALLER_ID, name: "Cedar", addresses: [ins.addr], provisionedUnixMs: BigInt(fleet.now) }));
    await at(A).node.putRecord({ record: { kind: "node", id: INSTALLER_ID, body } });
    for (let i = 0; i < 10; i++) fleet.tick(fleet.now + 1000);

    // Another Console reads Basalt as its anchor.
    const st = (await at(B).node.getFleetStatus({})).nodes.find((n) => n.nodeId === INSTALLER_ID);
    expect(st?.provisioning?.phase).toBe(Provisioning_Phase.INSTALLING);
    expect(nodeWord(st, fleet.now)).toBe("Provisioning");
    expect(nodeDown("Provisioning")).toBe(true);
    expect(provisioningLine(st)).toMatch(/^20% · /);
  });

  it("raises a notice when the install failed, not an unreachable one", () => {
    const st = { nodeId: "c", reachable: false, provisioning: { phase: Provisioning_Phase.FAILED, message: "disk too small" } } as never;
    const snap = { nodes: [{ nodeId: "c", name: "Cedar" }], status: new Map([["c", st]]), spaces: [{ defaultForNew: true }] } as never;
    expect(notices(snap, [], null).map((n) => n.title)).toEqual(["Cedar's install failed"]);
    expect(notices(snap, [], null)[0].detail).toBe("install failed: disk too small");
  });
});

describe("MoveVolume", () => {
  it("stops the Service, moves the folder as a job, follows the spec and restarts", async () => {
    const svc = createClient(ServiceService, createDemoTransport("10.0.0.5:7443", fleet));
    const { jobId } = await svc.moveVolume({ name: "jellyfin", volume: "media", toSpace: "general" });
    expect(jobId).toMatch(/^job-/);
    let st = (await svc.listServices({})).services.find((s) => s.spec?.name === "jellyfin")!;
    expect(st.state).toBe(ServiceStatus_State.STOPPED);
    for (let i = 0; i < 20; i++) fleet.tick(fleet.now + 1000);
    st = (await svc.listServices({})).services.find((s) => s.spec?.name === "jellyfin")!;
    expect(st.spec?.volumeSpaces.media).toBe("general");
    expect(st.state).toBe(ServiceStatus_State.RUNNING);
    await expect(svc.moveVolume({ name: "jellyfin", volume: "media", toSpace: "general" })).rejects.toThrow(/already in/);
  });
});
