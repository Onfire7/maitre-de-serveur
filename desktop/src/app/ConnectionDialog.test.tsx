// @vitest-environment jsdom
// Decision 284: the connection chooser names each way to each Node, says
// what each answered, pins a choice, and takes a typed address.
import "@/test/dom";
import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { NodeSchema } from "@/gen/maitre/v1/fleet_pb";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { ConnectionDialog, connectionLine } from "./ConnectionDialog";

const A = "a".repeat(64);
const B = "b".repeat(64);
const nodes = [create(NodeSchema, { nodeId: A, name: "Alder", letter: "A" }), create(NodeSchema, { nodeId: B, name: "Boulder", letter: "B" })];
const known = [
  { node_id: A, name: "Alder", addresses: ["192.168.4.74:7443", "100.64.0.5:7443"] },
  { node_id: B, name: "Boulder", addresses: ["192.168.4.75:7443"] },
];
const candidates = known.map((n) => ({ nodeId: n.node_id, letter: "", addresses: n.addresses }));
const snap = (addr: string) => ({ anchor: { addr, nodeId: A }, nodes, records: [], status: new Map() }) as unknown as FleetSnapshot;

function mount(over: Partial<React.ComponentProps<typeof ConnectionDialog>> = {}) {
  const onChoose = vi.fn();
  const onUnpin = vi.fn();
  const onAddAddress = vi.fn(async () => undefined);
  render(<ConnectionDialog open onOpenChange={() => undefined} snap={undefined} known={known} candidates={candidates} attempts={[]} pin={undefined} onChoose={onChoose} onUnpin={onUnpin} onAddAddress={onAddAddress} {...over} />);
  return { onChoose, onUnpin, onAddAddress };
}

describe("ConnectionDialog", () => {
  it("says how the Fleet is reached", () => {
    expect(connectionLine(snap("100.64.0.5:7443"), known)).toBe("through Alder over Tailscale");
    expect(connectionLine(snap("192.168.4.74:7443"), known)).toBe("through Alder at home");
    expect(connectionLine(undefined, known)).toBe("not connected");
  });

  it("lists every way with what it answered, and pins the one chosen", () => {
    const attempts = [
      { nodeId: A, addr: "192.168.4.74:7443", error: "dial tcp: i/o timeout", at: 1 },
      { nodeId: B, addr: "192.168.4.75:7443", error: "dial tcp: i/o timeout", at: 2 },
    ];
    const { onChoose } = mount({ attempts, error: "dial tcp: i/o timeout" });
    expect(screen.getByText(/No Node answers: dial tcp/)).toBeTruthy();
    expect(screen.getAllByText("at home")).toHaveLength(2);
    expect(screen.getByText("over Tailscale")).toBeTruthy();
    expect(screen.getAllByText("dial tcp: i/o timeout")).toHaveLength(2);
    expect(screen.getByText("not tried this round")).toBeTruthy();
    fireEvent.click(screen.getByLabelText("Use Alder over Tailscale"));
    expect(onChoose).toHaveBeenCalledWith(A, "100.64.0.5:7443");
  });

  it("shows the pin and lets the app choose again", () => {
    const { onUnpin } = mount({ snap: snap("100.64.0.5:7443"), pin: { fingerprint: A, addr: "100.64.0.5:7443" } });
    expect(screen.getByText(/Reading the Fleet through Alder over Tailscale, as you chose/)).toBeTruthy();
    expect(screen.getByText("connected · pinned")).toBeTruthy();
    fireEvent.click(screen.getByText("Let the app choose"));
    expect(onUnpin).toHaveBeenCalled();
  });

  it("keeps a typed address under the Node picked and uses it", async () => {
    const { onChoose, onAddAddress } = mount();
    const use = screen.getAllByText("Use").at(-1)!.closest("button")!;
    expect(use.hasAttribute("disabled")).toBe(true);
    fireEvent.change(screen.getByLabelText("Another address"), { target: { value: "100.64.0.9:7443" } });
    expect(use.hasAttribute("disabled")).toBe(false);
    fireEvent.click(use);
    await waitFor(() => expect(onChoose).toHaveBeenCalledWith(A, "100.64.0.9:7443"));
    expect(onAddAddress).toHaveBeenCalledWith(A, "100.64.0.9:7443");
  });
});
