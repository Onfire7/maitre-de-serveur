// @vitest-environment jsdom
// "Restart now" for a staged update restarts the Node, so it asks first as
// the Node's Restart does (#9, #22): how long its Services stop, and the
// Terminals the restart ends.
import "@/test/dom";
import { describe, expect, it } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { TerminalInfoSchema } from "@/gen/maitre/v1/terminal_pb";
import { RestartStaged } from "./NodeDetails";

const target = { addr: "127.0.0.1:7443", fingerprint: "ab".repeat(32) };
const held = create(TerminalInfoSchema, { id: "t1", service: "nextcloud", container: "server", user: "root", argv: ["occ", "upgrade"], running: true });

describe("Restart now for a staged update", () => {
  it("asks first, naming the downtime and the Terminals it ends, and restarts only when held", async () => {
    const restarts: number[] = [];
    render(<RestartStaged name="Alder" target={target} status={{ typicalDowntimeMs: 95_000n }} onRestart={() => restarts.push(1)} list={async () => [held]} />);
    fireEvent.click(screen.getByRole("button", { name: /Restart now/ }));
    const dialog = await screen.findByRole("alertdialog", { name: "Restart Alder?" });
    expect(within(dialog).getByText(/Its Services stop for about 2 minutes while it comes back\./)).toBeTruthy();
    expect(await within(dialog).findByText(/A Terminal is running on this Node in nextcloud \(server\):/)).toBeTruthy();
    expect(within(dialog).getByRole("button", { name: /Hold to restart/ })).toBeTruthy();
    expect(restarts).toEqual([]);
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(restarts).toEqual([]);
  });
});
