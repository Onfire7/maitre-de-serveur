// Services of several containers on the demo Node (plan C1-C5): each
// container's status, logs, image and grants, and the Console's refusal to
// install one on a Node that does not say it can.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createClient, type Transport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { ServiceService, ServiceSpecSchema, ServiceStatus_State, ControlServiceRequest_Action } from "@/gen/maitre/v1/service_pb";
import { DemoFleet, NODE_A } from "@/demo/fleet";
import { createDemoTransport } from "@/demo/transport";
import { canRollBack, componentsOf, logSource } from "./components";

const A = "10.0.0.5:7443";
let fleet: DemoFleet;
let older = false;

// transportFor reaches the demo Node, or one that lists no features.
vi.mock("@/transport", () => ({
  transportFor: (t: { addr: string }): Transport => {
    const tr = createDemoTransport(t.addr, fleet);
    return {
      ...tr,
      unary: async (...args: Parameters<Transport["unary"]>) => {
        const res = await tr.unary(...args);
        if (older && args[0].name === "GetNodeInfo") (res.message as unknown as { node: { features: string[] } }).node.features = [];
        return res;
      },
    } as Transport;
  },
}));

const services = () => createClient(ServiceService, createDemoTransport(A, fleet));
const immich = () => fleet.services.find((s) => s.spec.name === "immich")!;

beforeEach(() => {
  fleet = new DemoFleet();
  older = false;
});

describe("a Service of several containers", () => {
  it("reports each container's state", async () => {
    const st = (await services().listServices({})).services.find((s) => s.spec?.name === "immich")!;
    expect(st.state).toBe(ServiceStatus_State.RUNNING);
    expect(st.components.map((c) => c.name)).toEqual(["server", "machine-learning", "redis", "database"]);
    expect(st.components.find((c) => c.name === "machine-learning")?.restarts).toBe(2);
  });

  it("is one component with no name when it has a single container", () => {
    const one = fleet.services.find((s) => s.spec.name === "jellyfin")!.spec;
    expect(componentsOf(one)).toEqual([{ name: "", image: one.image, previousImage: one.previousImage, quadlet: one.quadlet }]);
    expect(componentsOf(immich().spec).map((c) => c.name)).toEqual(["server", "machine-learning", "redis", "database"]);
    // Roll back has the database's earlier image to go back to.
    expect(canRollBack(immich().spec)).toBe(true);
  });

  it("lists its whole log and each container's on the Node's Logs", async () => {
    const node = createClient(NodeService, createDemoTransport(A, fleet));
    const res = await node.getNodeLogs({ source: logSource("immich", "redis") });
    expect(res.sources.map((s) => s.id)).toContain("service:immich/database");
    expect(res.lines.every((l) => l.includes("immich-redis["))).toBe(true);
    const whole = await node.getNodeLogs({ source: logSource("immich") });
    expect(new Set(whole.lines.map((l) => /immich-([a-z-]+)\[/.exec(l)?.[1]))).toEqual(new Set(["server", "machine-learning", "redis", "database"]));
  });

  it("changes one container's image, keeping the rest", async () => {
    const before = componentsOf(immich().spec);
    await services().setImage({ name: "immich", component: "redis", image: "docker.io/valkey/valkey:9" });
    const after = componentsOf(immich().spec);
    expect(after.find((c) => c.name === "redis")?.image).toMatch(/^docker\.io\/valkey\/valkey:9@sha256:/);
    expect(after.find((c) => c.name === "redis")?.previousImage).toBe(before.find((c) => c.name === "redis")?.image);
    expect(after.filter((c) => c.name !== "redis")).toEqual(before.filter((c) => c.name !== "redis"));
    await expect(services().setImage({ name: "immich", component: "nope", image: "docker.io/valkey/valkey:9" })).rejects.toThrow(/no component "nope"/);
  });

  it("updates and rolls back every container together", async () => {
    await services().controlService({ name: "immich", action: ControlServiceRequest_Action.UPDATE });
    expect(componentsOf(immich().spec).every((c) => c.image.endsWith("5c".repeat(32)) && c.previousImage)).toBe(true);
    await services().controlService({ name: "immich", action: ControlServiceRequest_Action.ROLLBACK });
    expect(componentsOf(immich().spec).every((c) => !c.image.endsWith("5c".repeat(32)))).toBe(true);
  });

  it("asks for grants per container, naming its image", () => {
    const spec = create(ServiceSpecSchema, { ...immich().spec, name: "immich2" });
    spec.components = spec.components.map((c) => (c.name === "machine-learning" ? { ...c, quadlet: c.quadlet + "AddDevice=/dev/dri\n" } : c));
    const need = fleet.grantsNeeded(spec, NODE_A)!;
    expect(need.options).toEqual(["AddDevice=/dev/dri"]);
    expect(need.components).toEqual(["machine-learning"]);
    expect(need.imageRepositories).toEqual(["ghcr.io/immich-app/immich-machine-learning"]);
    expect(need.imageRepository).toBe("");
  });

  it("installs only on a Node that says it runs several containers", async () => {
    const { needComponents } = await import("./actions");
    const t = { addr: A, fingerprint: NODE_A };
    await expect(needComponents(t, immich().spec, "Alder")).resolves.toBeUndefined();
    older = true;
    await expect(needComponents(t, immich().spec, "Alder")).rejects.toThrow("immich runs several containers, which needs a newer Maître Server on Alder. Update it on the Nodes tab.");
    // A single container installs as it always has, without asking.
    await expect(needComponents(t, fleet.services.find((s) => s.spec.name === "jellyfin")!.spec, "Alder")).resolves.toBeUndefined();
  });
});
