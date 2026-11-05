import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ServiceSpecSchema } from "@/gen/maitre/v1/service_pb";
import { NETWORK_SERVICE, NETWORK_TUNNEL, dependantsOf, networkChoices, setupOf, stackOf } from "./setup";
import type { FleetService } from "./useFleet";

const svc = (init: NonNullable<Parameters<typeof create<typeof ServiceSpecSchema>>[1]>, node = "Alder"): FleetService => ({
  spec: create(ServiceSpecSchema, init),
  word: "Online",
  nodeUnreachable: false,
  node: { nodeId: init.nodeId as string, name: node } as FleetService["node"],
});

const vpn = svc({ name: "vpn", nodeId: "a", tunnel: true });
const immich = svc({ name: "immich", nodeId: "a", components: [{ name: "server", image: "x", quadlet: "[Container]\n" }] });
const plex = svc({ name: "plex", nodeId: "a" });
const elsewhere = svc({ name: "vpn-b", nodeId: "b", tunnel: true }, "Basalt");
const qb = svc({ name: "qbittorrent", nodeId: "a", networkKind: NETWORK_TUNNEL, networkTarget: "vpn" });
const all = [vpn, immich, plex, elsewhere, qb];

describe("a Service's Setup (ADR 0010)", () => {
  it("offers the Tunnels and the Services of several containers, not a plain one", () => {
    const choices = networkChoices(qb, all);
    expect(choices.map((c) => [c.name, c.kind, c.otherNode])).toEqual([
      ["immich", NETWORK_SERVICE, undefined],
      ["vpn", NETWORK_TUNNEL, undefined],
      ["vpn-b", NETWORK_TUNNEL, "Basalt"],
    ]);
    // plex has no network of its own to join; it is already on the shared one.
    expect(choices.some((c) => c.name === "plex")).toBe(false);
    // A Service already in a Tunnel is not a network to join either.
    expect(networkChoices(plex, all).some((c) => c.name === "qbittorrent")).toBe(false);
  });

  it("names what leans on a Service, and the whole set that must sit on one Node", () => {
    expect(dependantsOf("vpn", all)).toEqual(["qbittorrent"]);
    expect(dependantsOf("plex", all)).toEqual([]);
    expect(stackOf("vpn", all)).toEqual(["qbittorrent", "vpn"]);
    expect(stackOf("plex", all)).toEqual(["plex"]);
  });

  it("starts from what the Service has now, Pages included", () => {
    const setup = setupOf(create(ServiceSpecSchema, { name: "jellyfin", webPort: 8096, webPath: "/web", address: "10.90.0.11", fleetReach: true, ports: ["1900/udp"] }));
    expect(setup.address).toBe("10.90.0.11");
    expect(setup.fleetReach).toBe(true);
    expect(setup.ports).toEqual(["1900/udp"]);
    expect(setup.pages).toMatchObject([{ name: "web", port: 8096, path: "/web" }]);
  });
});
