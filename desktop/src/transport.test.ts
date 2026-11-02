import { beforeEach, describe, expect, it, vi } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { Code, ConnectError, createClient } from "@connectrpc/connect";

const invoke = vi.fn();
beforeEach(() => invoke.mockClear());
vi.mock("@tauri-apps/api/core", () => ({ invoke: (...args: unknown[]) => invoke(...args) }));

import { NodeService, GetNodeInfoResponseSchema } from "./gen/maitre/v1/node_pb";
import { noteAddresses, transportFor } from "./transport";
import { demotedRoutes, routeFor } from "./fleet/route";
import { GrantsNeededSchema } from "./gen/maitre/v1/fleet_pb";

describe("transportFor", () => {
  it("routes a unary call through the rpc command with the pinned target", async () => {
    const reply = create(GetNodeInfoResponseSchema, { node: { nodeId: "abc", name: "n1", version: "dev" } });
    invoke.mockResolvedValueOnce({
      status: 200,
      content_type: "application/proto",
      body: Array.from(toBinary(GetNodeInfoResponseSchema, reply)),
    });
    const client = createClient(NodeService, transportFor({ addr: "10.0.0.5:7443", fingerprint: "ff".repeat(32) }));
    const res = await client.getNodeInfo({});
    expect(res.node?.name).toBe("n1");
    expect(invoke).toHaveBeenCalledWith(
      "rpc",
      expect.objectContaining({
        addr: "10.0.0.5:7443",
        fingerprint: "ff".repeat(32),
        procedure: "/maitre.v1.NodeService/GetNodeInfo",
      }),
    );
  });

  it("falls back on the Node's other ways when the address does not answer, and remembers the one that did (decision 284)", async () => {
    const fp = "dd".repeat(32);
    noteAddresses(fp, ["192.168.4.4:7443", "100.74.111.122:7443"]);
    const reply = create(GetNodeInfoResponseSchema, { node: { nodeId: "abc", name: "avalon", version: "dev" } });
    invoke.mockRejectedValueOnce("192.168.4.4:7443 could not be reached: did not answer in 15 s");
    invoke.mockResolvedValueOnce({ status: 200, content_type: "application/proto", body: Array.from(toBinary(GetNodeInfoResponseSchema, reply)) });
    const client = createClient(NodeService, transportFor({ addr: "192.168.4.4:7443", fingerprint: fp }));
    const res = await client.getNodeInfo({});
    expect(res.node?.name).toBe("avalon");
    expect(invoke).toHaveBeenLastCalledWith("rpc", expect.objectContaining({ addr: "100.74.111.122:7443", fingerprint: fp }));
    expect(routeFor(fp)).toBe("100.74.111.122:7443");
    expect(demotedRoutes(fp)).toEqual(["192.168.4.4:7443"]);
  });

  it("does not retry a Node that answered with an error, and gives up when no way answers", async () => {
    const fp = "cc".repeat(32);
    noteAddresses(fp, ["192.168.4.4:7443", "100.74.111.122:7443"]);
    invoke.mockRejectedValueOnce("192.168.4.4:7443 could not be reached: Connection refused");
    invoke.mockRejectedValueOnce("100.74.111.122:7443 could not be reached: did not answer in 15 s");
    const client = createClient(NodeService, transportFor({ addr: "192.168.4.4:7443", fingerprint: fp }));
    await expect(client.getNodeInfo({})).rejects.toThrow(/100\.74\.111\.122:7443 could not be reached/);
    expect(invoke).toHaveBeenCalledTimes(2);
    invoke.mockClear();
    invoke.mockResolvedValueOnce({ status: 400, content_type: "application/json", body: Array.from(new TextEncoder().encode(JSON.stringify({ code: "unavailable", message: "Space has no Pool" }))) });
    await expect(client.getNodeInfo({})).rejects.toThrow(/no Pool/);
    expect(invoke).toHaveBeenCalledTimes(1);
  });

  it("stays on one address for the Fleet read, which tries every way itself", async () => {
    const fp = "bb".repeat(32);
    noteAddresses(fp, ["192.168.4.4:7443", "100.74.111.122:7443"]);
    invoke.mockRejectedValueOnce("192.168.4.4:7443 could not be reached: Connection refused");
    const client = createClient(NodeService, transportFor({ addr: "192.168.4.4:7443", fingerprint: fp }, { fallback: false }));
    await expect(client.getNodeInfo({})).rejects.toThrow(/could not be reached/);
    expect(invoke).toHaveBeenCalledTimes(1);
    expect(demotedRoutes(fp)).toEqual([]);
  });

  it("keeps a Connect error's details (decision 189's GrantsNeeded)", async () => {
    const need = create(GrantsNeededSchema, { nodeId: "ab", service: "jellyfin", imageRepository: "docker.io/jellyfin/jellyfin", options: ["AddDevice=/dev/dri"], grantIds: ["g1"] });
    const value = btoa(String.fromCharCode(...toBinary(GrantsNeededSchema, need))).replace(/=+$/, "");
    const json = JSON.stringify({ code: "failed_precondition", message: "grants needed", details: [{ type: "maitre.v1.GrantsNeeded", value }] });
    invoke.mockResolvedValueOnce({ status: 400, content_type: "application/json", body: Array.from(new TextEncoder().encode(json)) });
    const client = createClient(NodeService, transportFor({ addr: "10.0.0.5:7443", fingerprint: "ee".repeat(32) }));
    const err = await client.getNodeInfo({}).then(
      () => undefined,
      (e) => ConnectError.from(e),
    );
    expect(err?.code).toBe(Code.FailedPrecondition);
    const got = err?.findDetails(GrantsNeededSchema) ?? [];
    expect(got).toHaveLength(1);
    expect(got[0].options).toEqual(["AddDevice=/dev/dri"]);
    expect(got[0].grantIds).toEqual(["g1"]);
  });
});
