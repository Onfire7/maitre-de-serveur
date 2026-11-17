// @vitest-environment jsdom
import "@/test/dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { create, toBinary } from "@bufbuild/protobuf";
import { TerminalExit_Reason, TerminalFrameSchema, type TerminalFrame } from "@/gen/maitre/v1/terminal_pb";
import type { TerminalSocket } from "@/terminal/session";
import { getTerminal, openTerminal, resetTerminals } from "@/terminal/terminals";
import { TerminalLayer } from "./TerminalPane";

// The library is behind TerminalView (#10 may swap it); the pane is tested without it.
vi.mock("@/terminal/TerminalView", () => ({ TerminalView: () => <div data-testid="terminal-view" /> }));

let sockets: (TerminalSocket & { node: (f: TerminalFrame["frame"]) => void })[] = [];
function socket(): TerminalSocket & { node: (f: TerminalFrame["frame"]) => void } {
  const s = {
    binaryType: "",
    send() {},
    close() {},
    onopen: null as (() => void) | null,
    onmessage: null as ((e: { data: ArrayBuffer }) => void) | null,
    onclose: null,
    onerror: null,
    node(frame: TerminalFrame["frame"]) {
      const b = toBinary(TerminalFrameSchema, create(TerminalFrameSchema, { frame }));
      s.onmessage?.({ data: b.slice().buffer as ArrayBuffer });
    },
  };
  sockets.push(s);
  return s;
}
const flush = () => act(() => new Promise((r) => setTimeout(r, 0)));
const target = { addr: "127.0.0.1:7443", fingerprint: "ab".repeat(32) };

describe("a Terminal in the Service's area (ADR 0011)", () => {
  beforeEach(() => {
    resetTerminals();
    sockets = [];
  });

  async function opened() {
    openTerminal("kuma", target, { url: async () => "ws://x", socket });
    render(<TerminalLayer shown="kuma" onClose={() => {}} />);
    await flush();
    act(() => {
      sockets[0].onopen?.();
      sockets[0].node({ case: "started", value: { $typeName: "maitre.v1.TerminalStarted", id: "1", container: "kuma", user: "root", argv: ["/bin/bash"] } });
    });
  }

  it("is labelled with the container and the user", async () => {
    await opened();
    expect(screen.getByText("kuma · root")).toBeTruthy();
    expect(screen.getByTestId("terminal-view")).toBeTruthy();
  });

  it("shows the exit code when the shell exits, and Run again starts a new one", async () => {
    await opened();
    act(() => sockets[0].node({ case: "exit", value: { $typeName: "maitre.v1.TerminalExit", reason: TerminalExit_Reason.EXITED, code: 2, message: "" } }));
    expect(screen.getByText(/exited with code 2/)).toBeTruthy();
    const before = getTerminal("kuma")?.session;
    fireEvent.click(screen.getByRole("button", { name: /Run again/ }));
    await flush();
    expect(getTerminal("kuma")?.session).not.toBe(before);
    expect(sockets).toHaveLength(2);
    expect(screen.queryByText(/exited with code/)).toBeNull();
  });

  it("says so when the container has no shell", async () => {
    await opened();
    act(() => sockets[0].node({ case: "exit", value: { $typeName: "maitre.v1.TerminalExit", reason: TerminalExit_Reason.NO_SHELL, code: 0, message: "This container has no shell" } }));
    expect(screen.getByText("This container has no shell")).toBeTruthy();
  });

  it("keeps a Terminal that is not shown, hidden", async () => {
    openTerminal("kuma", target, { url: async () => "ws://x", socket });
    render(<TerminalLayer shown={undefined} onClose={() => {}} />);
    await flush();
    const pane = screen.getByTestId("terminal-pane");
    expect(pane.getAttribute("data-shown")).toBeNull();
  });
});
