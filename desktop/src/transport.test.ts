import { describe, expect, it, vi } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { createClient } from "@connectrpc/connect";
import { GetNodeInfoResponseSchema, NodeService } from "./gen/maitre/v1/node_pb";

// Stand in for the Tauri `rpc` command: record the call, answer with a
// serialised GetNodeInfoResponse like a Node would.
const calls: { procedure: string; body: number[] }[] = [];
vi.mock("@tauri-apps/api/core", () => ({
  invoke: async (_cmd: string, args: { procedure: string; body: number[] }) => {
    calls.push(args);
    const msg = create(GetNodeInfoResponseSchema, {
      node: { nodeId: "abc", name: "basement", version: "dev", devMode: true, uptimeSeconds: 7n },
    });
    return { status: 200, content_type: "application/proto", body: Array.from(toBinary(GetNodeInfoResponseSchema, msg)) };
  },
}));

describe("tauri transport", () => {
  it("routes a generated client call through the rpc command", async () => {
    const { transport } = await import("./transport");
    const node = createClient(NodeService, transport);
    const res = await node.getNodeInfo({});
    expect(calls).toHaveLength(1);
    expect(calls[0].procedure).toBe("/maitre.v1.NodeService/GetNodeInfo");
    expect(res.node?.name).toBe("basement");
    expect(res.node?.uptimeSeconds).toBe(7n);
  });
});
