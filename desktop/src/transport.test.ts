import { describe, expect, it, vi } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { Code, ConnectError, createClient } from "@connectrpc/connect";

const invoke = vi.fn();
vi.mock("@tauri-apps/api/core", () => ({ invoke: (...args: unknown[]) => invoke(...args) }));

import { NodeService, GetNodeInfoResponseSchema } from "./gen/maitre/v1/node_pb";
import { transportFor } from "./transport";
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
