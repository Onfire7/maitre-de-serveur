import { describe, expect, it, vi } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { createClient } from "@connectrpc/connect";

const invoke = vi.fn();
vi.mock("@tauri-apps/api/core", () => ({ invoke: (...args: unknown[]) => invoke(...args) }));

import { NodeService, GetNodeInfoResponseSchema } from "./gen/maitre/v1/node_pb";
import { transportFor } from "./transport";

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
});
