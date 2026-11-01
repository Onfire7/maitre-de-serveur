// @vitest-environment jsdom
// Decision 282: the Nodes tab sets the Fleet's time zone and says which
// Nodes have taken it.
import "@/test/dom";
import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { create, toBinary, fromBinary } from "@bufbuild/protobuf";
import { FleetSettingsSchema, NodeSchema, RecordSchema, type Record as Rec } from "@/gen/maitre/v1/fleet_pb";
import { FleetNodeStatusSchema } from "@/gen/maitre/v1/node_pb";
import type { FleetSnapshot } from "@/fleet/useFleet";

const put = vi.fn(async () => undefined);
vi.mock("@/fleet/sign", async (orig) => ({ ...(await orig<typeof import("@/fleet/sign")>()), putSigned: (...a: unknown[]) => put(...(a as [])) }));

import { FleetTimeZone } from "./FleetTimeZone";
import { fleetTimeZone, nodeZones } from "@/fleet/timezone";

const A = "a".repeat(64);
const B = "b".repeat(64);
const nodes = [create(NodeSchema, { nodeId: A, name: "Boulder" }), create(NodeSchema, { nodeId: B, name: "Avalon" })];
const setting = (tz: string): Rec => create(RecordSchema, { kind: "fleet_settings", id: "fleet", ownerNodeId: A, body: toBinary(FleetSettingsSchema, create(FleetSettingsSchema, { timeZone: tz })) });
const status = (a: string, b: string | undefined) =>
  new Map([
    [A, create(FleetNodeStatusSchema, { nodeId: A, reachable: true, timeZone: a })],
    [B, create(FleetNodeStatusSchema, { nodeId: B, reachable: b !== undefined, timeZone: b ?? "" })],
  ]);
const snap = (records: Rec[], st = status("Europe/Paris", "Europe/Paris")) => ({ anchor: { addr: "192.168.4.74:7443", nodeId: A }, nodes, records, status: st }) as unknown as FleetSnapshot;
const wrap = (ui: React.ReactElement) => render(<QueryClientProvider client={new QueryClient()}>{ui}</QueryClientProvider>);

describe("FleetTimeZone", () => {
  it("reads the zone and where each Node stands", () => {
    expect(fleetTimeZone([])).toBe("");
    expect(fleetTimeZone([setting("Europe/Paris")])).toBe("Europe/Paris");
    const s = snap([setting("Europe/Paris")], status("Europe/Paris", "UTC"));
    expect(nodeZones(s, "Europe/Paris").map((z) => z.state)).toEqual(["on", "other"]);
    expect(nodeZones(snap([], status("Europe/Paris", "")), "Europe/Paris").map((z) => z.state)).toEqual(["on", "unknown"]);
    expect(nodeZones(snap([], status("Europe/Paris", undefined)), "Europe/Paris").map((z) => z.state)).toEqual(["on", "unreachable"]);
  });

  it("says when every Node is on the zone, and which is not", () => {
    const { unmount } = wrap(<FleetTimeZone snap={snap([setting("Europe/Paris")])} />);
    expect(screen.getByText(/All 2 Nodes are on/)).toBeTruthy();
    unmount();
    wrap(<FleetTimeZone snap={snap([setting("Europe/Paris")], status("Europe/Paris", "UTC"))} />);
    expect(screen.getByText(/Avalon is still on UTC/)).toBeTruthy();
  });

  it("writes the zone typed through the anchor", async () => {
    wrap(<FleetTimeZone snap={snap([])} />);
    const input = screen.getByLabelText("Fleet time zone") as HTMLInputElement;
    expect(input.value).toBe("");
    fireEvent.change(input, { target: { value: "Europe/Paris" } });
    fireEvent.click(screen.getByText("Save"));
    await waitFor(() => expect(put).toHaveBeenCalled());
    const [target, rec] = put.mock.calls[0] as unknown as [{ addr: string; fingerprint: string }, { kind: string; id: string; body: Uint8Array; deleted: boolean }];
    expect(target).toEqual({ addr: "192.168.4.74:7443", fingerprint: A });
    expect(rec.kind).toBe("fleet_settings");
    expect(rec.id).toBe("fleet");
    expect(rec.deleted).toBe(false);
    expect(fromBinary(FleetSettingsSchema, rec.body).timeZone).toBe("Europe/Paris");
  });

  it("refuses a name that is not a zone", async () => {
    put.mockClear();
    wrap(<FleetTimeZone snap={snap([])} />);
    fireEvent.change(screen.getByLabelText("Fleet time zone"), { target: { value: "paris time" } });
    fireEvent.click(screen.getByText("Save"));
    await new Promise((r) => setTimeout(r, 20));
    expect(put).not.toHaveBeenCalled();
  });

  it("clears the zone with a deletion", async () => {
    put.mockClear();
    wrap(<FleetTimeZone snap={snap([setting("Europe/Paris")])} />);
    fireEvent.click(screen.getByText("Clear"));
    await waitFor(() => expect(put).toHaveBeenCalled());
    const rec = (put.mock.calls[0] as unknown as [unknown, { deleted: boolean }])[1];
    expect(rec.deleted).toBe(true);
  });
});
