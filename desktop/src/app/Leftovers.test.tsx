// @vitest-environment jsdom
// Decision 276: the Nodes tab lists what removed Nodes left behind, and
// clears an entry through any Node.
import "@/test/dom";
import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { create, toBinary } from "@bufbuild/protobuf";
import { RecordSchema, type Record as Rec } from "@/gen/maitre/v1/fleet_pb";
import { ServiceSpecSchema } from "@/gen/maitre/v1/service_pb";
import type { FleetSnapshot } from "@/fleet/useFleet";
import type { ConsoleState } from "@/state";

const put = vi.fn(async () => undefined);
vi.mock("@/fleet/sign", () => ({ putSigned: (...a: unknown[]) => put(...(a as [])) }));

import { Leftovers } from "./Leftovers";

const GONE = "4".repeat(64);
const A = "a".repeat(64);
const svc = toBinary(ServiceSpecSchema, create(ServiceSpecSchema, { name: "jellyfin", nodeId: GONE }));
const records: Rec[] = [
  create(RecordSchema, { kind: "node", id: A, ownerNodeId: A, body: new Uint8Array([1]) }),
  create(RecordSchema, { kind: "node", id: GONE, ownerNodeId: GONE, deleted: true }),
  create(RecordSchema, { kind: "service", id: "jellyfin", ownerNodeId: GONE, body: svc }),
];
const state = { nodes: [{ node_id: A, name: "Boulder", addresses: ["192.168.4.74:7443"] }] } as unknown as ConsoleState;

describe("Leftovers", () => {
  it("shows nothing when nothing was left", () => {
    const { container } = render(<Leftovers snap={{ records: records.slice(0, 2) } as FleetSnapshot} state={state} />);
    expect(container.textContent).toBe("");
  });

  it("lists a removed Node's Service and removes it through a Node", async () => {
    const done = vi.fn();
    render(<Leftovers snap={{ records } as FleetSnapshot} state={state} onDone={done} />);
    expect(screen.getByText("jellyfin")).toBeTruthy();
    expect(screen.getByText(/Service of removed Node 44444444/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /Remove/ }));
    await waitFor(() => expect(done).toHaveBeenCalled());
    expect(put).toHaveBeenCalledWith({ addr: "192.168.4.74:7443", fingerprint: A }, { kind: "service", id: "jellyfin", deleted: true }, records[2]);
  });
});
