// @vitest-environment jsdom
import "@/test/dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { TerminalExit_Reason, TerminalFrameSchema, type TerminalFrame } from "@/gen/maitre/v1/terminal_pb";
import type { TerminalSocket } from "@/terminal/session";
import { getTerminal, openTerminal, resetTerminals, type HeldTerminal } from "@/terminal/terminals";
import { TerminalLayer, TerminalList } from "./TerminalPane";

// The library is behind TerminalView (#10 may swap it); the pane is tested without it.
vi.mock("@/terminal/TerminalView", () => ({ TerminalView: () => <div data-testid="terminal-view" /> }));

type PlayedSocket = TerminalSocket & { node: (f: TerminalFrame["frame"]) => void; sent: TerminalFrame[]; closed: boolean };
let sockets: PlayedSocket[] = [];
function socket(): PlayedSocket {
  const s = {
    binaryType: "",
    sent: [] as TerminalFrame[],
    closed: false,
    send(d: Uint8Array) {
      s.sent.push(fromBinary(TerminalFrameSchema, d));
    },
    close() {
      s.closed = true;
    },
    onopen: null as (() => void) | null,
    onmessage: null as ((e: { data: ArrayBuffer }) => void) | null,
    onclose: null as ((e: { code: number; reason: string }) => void) | null,
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
      sockets[0].node({ case: "started", value: { $typeName: "maitre.v1.TerminalStarted", id: "1", container: "kuma", user: "root", argv: ["/bin/bash"], startedUnixMs: 1n } });
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

describe("a Terminal held by the Node (#5)", () => {
  beforeEach(() => {
    resetTerminals();
    sockets = [];
  });

  async function running() {
    openTerminal("kuma", target, { url: async () => "ws://x", socket });
    render(<TerminalLayer shown="kuma" onClose={() => {}} />);
    await flush();
    act(() => {
      sockets[0].onopen?.();
      sockets[0].node({ case: "started", value: { $typeName: "maitre.v1.TerminalStarted", id: "0123456789abcdef", container: "kuma", user: "root", argv: ["/bin/bash"], startedUnixMs: 1n } });
    });
  }

  it("End asks the Node to end it, and closing the view only detaches", async () => {
    await running();
    fireEvent.click(screen.getByRole("button", { name: "End kuma's Terminal" }));
    expect(sockets[0].sent.at(-1)?.frame.case).toBe("end");
    fireEvent.click(screen.getByRole("button", { name: "All of kuma's Terminals" }));
    expect(sockets[0].closed).toBe(true);
    expect(sockets[0].sent.filter((f) => f.frame.case === "end")).toHaveLength(1);
    expect(getTerminal("kuma")).toBeUndefined();
  });

  it("when the connection is lost (the agent restarting, say), Reattach attaches to the same Terminal", async () => {
    await running();
    act(() => sockets[0].onclose?.({ code: 1001, reason: "" }));
    expect(screen.getByText(/still running there/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /Reattach/ }));
    await flush();
    act(() => sockets[1].onopen?.());
    const f = sockets[1].sent[0].frame;
    expect(f.case === "attach" && f.value.id).toBe("0123456789abcdef");
  });
});

describe("the Service's Terminals (#5)", () => {
  beforeEach(() => {
    resetTerminals();
    sockets = [];
  });
  const held = (p: Partial<HeldTerminal>): HeldTerminal => ({ $typeName: "maitre.v1.TerminalInfo", id: "", service: "kuma", container: "kuma", user: "root", argv: ["/bin/bash"], startedUnixMs: 0n, running: false, code: 0, endedUnixMs: 0n, ...p });

  it("lists running and finished Terminals with their state; each opens, and a running one can be Ended", async () => {
    const ended: string[] = [];
    let list = [held({ id: "aaaaaaaaaaaaaaaa", running: true, startedUnixMs: 1n }), held({ id: "bbbbbbbbbbbbbbbb", code: 3, endedUnixMs: 2n })];
    const deps = {
      list: async () => list,
      end: async (_t: unknown, _s: string, id: string) => {
        ended.push(id);
        list = list.slice(1);
      },
      terminal: { url: async () => "ws://x", socket },
    };
    render(<TerminalList service="kuma" target={target} deps={deps} />);
    await flush();
    const rows = screen.getAllByTestId("held-terminal");
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toMatch(/Running since/);
    expect(rows[1].textContent).toMatch(/Exited with code 3/);
    expect(within(rows[1]).queryByRole("button", { name: "End" })).toBeNull();

    fireEvent.click(within(rows[0]).getByRole("button", { name: "End" }));
    await flush();
    expect(ended).toEqual(["aaaaaaaaaaaaaaaa"]);
    expect(screen.getAllByTestId("held-terminal")).toHaveLength(1);

    fireEvent.click(within(screen.getByTestId("held-terminal")).getByRole("button", { name: "Open" }));
    await flush();
    act(() => sockets[0].onopen?.());
    const f = sockets[0].sent[0].frame;
    expect(f.case === "attach" && f.value.id).toBe("bbbbbbbbbbbbbbbb");
    expect(getTerminal("kuma")?.session.info.id).toBe("bbbbbbbbbbbbbbbb");
  });

  it("starts a new Terminal where and as whom chosen (#7), and says when the Node has none", async () => {
    render(<TerminalList service="photos" target={target} containers={["server", "database"]} deps={{ list: async () => [], end: async () => {}, terminal: { url: async () => "ws://x", socket } }} />);
    await flush();
    expect(screen.getByText("No Terminal is running in photos.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /New Terminal/ }));
    fireEvent.click(screen.getByRole("radio", { name: "database" }));
    fireEvent.click(screen.getByRole("checkbox", { name: /Run as root/ }));
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    await flush();
    act(() => sockets[0].onopen?.());
    const f = sockets[0].sent[0].frame;
    expect(f.case === "start" && [f.value.component, f.value.root]).toEqual(["database", true]);
  });
});
