// @vitest-environment jsdom
import "@/test/dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { TerminalExit_Reason, TerminalFrameSchema, type TerminalFrame } from "@/gen/maitre/v1/terminal_pb";
import type { TerminalSocket } from "@/terminal/session";
import { getTerminal, openTerminal, resetTerminals, terminalsOf, type HeldTerminal } from "@/terminal/terminals";
import { GrantSchema } from "@/gen/maitre/v1/fleet_pb";
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
    const t = openTerminal("kuma", target, { url: async () => "ws://x", socket });
    render(<TerminalLayer shown={t.key} onClose={() => {}} onList={() => {}} />);
    await flush();
    act(() => {
      sockets[0].onopen?.();
      sockets[0].node({ case: "started", value: { $typeName: "maitre.v1.TerminalStarted", id: "1", container: "kuma", user: "root", argv: ["/bin/bash"], startedUnixMs: 1n } });
    });
    return t;
  }

  it("is labelled with the container and the user", async () => {
    await opened();
    expect(screen.getByText("kuma · root")).toBeTruthy();
    expect(screen.getByTestId("terminal-view")).toBeTruthy();
  });

  it("shows the exit code when the shell exits, and Run again starts a new one", async () => {
    const t = await opened();
    act(() => sockets[0].node({ case: "exit", value: { $typeName: "maitre.v1.TerminalExit", reason: TerminalExit_Reason.EXITED, code: 2, message: "" } }));
    expect(screen.getByText(/exited with code 2/)).toBeTruthy();
    const before = getTerminal(t.key)?.session;
    fireEvent.click(screen.getByRole("button", { name: /Run again/ }));
    await flush();
    expect(getTerminal(t.key)?.session).not.toBe(before);
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
    render(<TerminalLayer shown={undefined} onClose={() => {}} onList={() => {}} />);
    await flush();
    const pane = screen.getByTestId("terminal-pane");
    expect(pane.getAttribute("data-shown")).toBeNull();
  });
});

describe("a Terminal into a Service with protected grants (#8)", () => {
  beforeEach(() => {
    resetTerminals();
    sockets = [];
  });
  const grantsOf = (service: string) => (service === "jellyfin" ? [create(GrantSchema, { service, option: "Volume=/mnt/media" }), create(GrantSchema, { service, option: "PodmanArgs=--privileged" })] : []);

  async function into(service: string) {
    const t = openTerminal(service, target, { url: async () => "ws://x", socket });
    render(<TerminalLayer shown={t.key} grants={grantsOf} onClose={() => {}} onList={() => {}} />);
    await flush();
    act(() => {
      sockets[0].onopen?.();
      sockets[0].node({ case: "started", value: { $typeName: "maitre.v1.TerminalStarted", id: "1", container: service, user: "root", argv: ["/bin/bash"], startedUnixMs: 1n } });
    });
  }

  it("shows a warning line naming the grants, with nothing to confirm", async () => {
    await into("jellyfin");
    const note = screen.getByRole("note");
    expect(note.textContent).toBe("This Service has access to the Node: /mnt/media, privileged. Commands here can change the Node itself.");
    expect(within(note).queryByRole("button")).toBeNull();
    expect(screen.getByTestId("terminal-view")).toBeTruthy();
  });

  it("shows none for a Service without them", async () => {
    await into("kuma");
    expect(screen.queryByRole("note")).toBeNull();
    expect(screen.queryByText(/access to the Node/)).toBeNull();
  });
});

describe("a Terminal held by the Node (#5)", () => {
  beforeEach(() => {
    resetTerminals();
    sockets = [];
  });

  async function running(onList: (service: string) => void = () => {}) {
    const t = openTerminal("kuma", target, { url: async () => "ws://x", socket });
    render(<TerminalLayer shown={t.key} onClose={() => {}} onList={onList} />);
    await flush();
    act(() => {
      sockets[0].onopen?.();
      sockets[0].node({ case: "started", value: { $typeName: "maitre.v1.TerminalStarted", id: "0123456789abcdef", container: "kuma", user: "root", argv: ["/bin/bash"], startedUnixMs: 1n } });
    });
    return t;
  }

  it("End asks the Node to end it, All Terminals leaves it open, and closing the view only detaches", async () => {
    const listed: string[] = [];
    const t = await running((s) => listed.push(s));
    fireEvent.click(screen.getByRole("button", { name: "End kuma's Terminal" }));
    expect(sockets[0].sent.at(-1)?.frame.case).toBe("end");
    fireEvent.click(screen.getByRole("button", { name: "All of kuma's Terminals" }));
    expect(listed).toEqual(["kuma"]);
    expect(sockets[0].closed).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Close kuma's Terminal" }));
    expect(sockets[0].closed).toBe(true);
    expect(sockets[0].sent.filter((f) => f.frame.case === "end")).toHaveLength(1);
    expect(getTerminal(t.key)).toBeUndefined();
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

describe("Open in your terminal (#11)", () => {
  beforeEach(() => {
    resetTerminals();
    sockets = [];
  });

  async function running(openIn: (t: typeof target, service: string, id: string) => Promise<void>) {
    const t = openTerminal("kuma", target, { url: async () => "ws://x", socket, openInTerminal: openIn });
    render(<TerminalLayer shown={t.key} onClose={() => {}} onList={() => {}} />);
    await flush();
    act(() => {
      sockets[0].onopen?.();
      sockets[0].node({ case: "started", value: { $typeName: "maitre.v1.TerminalStarted", id: "0123456789abcdef", container: "kuma", user: "root", argv: ["/bin/bash"], startedUnixMs: 1n } });
    });
  }

  it("opens the same Terminal in the person's own terminal app, and this view stays", async () => {
    const calls: unknown[][] = [];
    await running(async (...a) => {
      calls.push(a);
    });
    fireEvent.click(screen.getByRole("button", { name: "Open kuma's Terminal in your terminal" }));
    await flush();
    expect(calls).toEqual([[target, "kuma", "0123456789abcdef"]]);
    expect(sockets[0].closed).toBe(false);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("says plainly when no terminal app can be found", async () => {
    await running(async () => {
      throw "No terminal app was found on this computer. Install one, such as GNOME Terminal, Konsole or xterm, and try again; the Terminal is still open here.";
    });
    fireEvent.click(screen.getByRole("button", { name: "Open kuma's Terminal in your terminal" }));
    await flush();
    expect(screen.getByRole("alert").textContent).toMatch(/^No terminal app was found on this computer\./);
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("is offered only while the Terminal runs", async () => {
    await running(async () => {});
    act(() => sockets[0].node({ case: "exit", value: { $typeName: "maitre.v1.TerminalExit", reason: TerminalExit_Reason.EXITED, code: 0, message: "" } }));
    expect(screen.queryByRole("button", { name: "Open kuma's Terminal in your terminal" })).toBeNull();
  });
});

describe("several Terminals, shared (#6)", () => {
  beforeEach(() => {
    resetTerminals();
    sockets = [];
  });
  const deps = { url: async () => "ws://x", socket };

  it("keeps each open Terminal of a Service in its own view, and shows the one routed to", async () => {
    const a = openTerminal("kuma", target, deps);
    const b = openTerminal("kuma", target, deps);
    render(<TerminalLayer shown={b.key} onClose={() => {}} onList={() => {}} />);
    await flush();
    act(() => {
      sockets.forEach((s, i) => {
        s.onopen?.();
        s.node({ case: "started", value: { $typeName: "maitre.v1.TerminalStarted", id: `${i}`.repeat(16), container: i ? "worker" : "server", user: "root", argv: ["/bin/bash"], startedUnixMs: 1n } });
      });
    });
    const panes = screen.getAllByTestId("terminal-pane");
    expect(panes).toHaveLength(2);
    const shown = panes.filter((p) => p.getAttribute("data-shown"));
    expect(shown).toHaveLength(1);
    expect(within(shown[0]).getByText("worker · root")).toBeTruthy();
    expect(terminalsOf("kuma").map((t) => t.key)).toEqual([a.key, b.key]);
  });

  it("says why when the container already has its 10 Terminals", async () => {
    const t = openTerminal("kuma", target, deps);
    render(<TerminalLayer shown={t.key} onClose={() => {}} onList={() => {}} />);
    await flush();
    const why = "server already has 10 Terminals running, the most a container may hold. End one to open another.";
    act(() => {
      sockets[0].onopen?.();
      sockets[0].node({ case: "exit", value: { $typeName: "maitre.v1.TerminalExit", reason: TerminalExit_Reason.FAILED, code: 0, message: why } });
    });
    expect(screen.getByText(why)).toBeTruthy();
  });
});

describe("the Service's Terminals (#5)", () => {
  beforeEach(() => {
    resetTerminals();
    sockets = [];
  });
  const held = (p: Partial<HeldTerminal>): HeldTerminal => ({ $typeName: "maitre.v1.TerminalInfo", id: "", service: "kuma", container: "kuma", user: "root", argv: ["/bin/bash"], startedUnixMs: 0n, running: false, code: 0, endedUnixMs: 0n, root: false, openedBy: "", ...p });

  it("says who opened each Terminal, when, and its exit code (#8)", async () => {
    const at = (h: number, m: number) => BigInt(new Date(2026, 10, 17, h, m).getTime());
    const list = [
      held({ id: "aaaaaaaaaaaaaaaa", running: true, startedUnixMs: at(14, 2), openedBy: "Laptop" }),
      held({ id: "bbbbbbbbbbbbbbbb", code: 0, startedUnixMs: at(9, 30), endedUnixMs: at(9, 41), openedBy: "Phone" }),
    ];
    render(<TerminalList service="kuma" target={target} deps={{ list: async () => list, end: async () => {} }} onOpen={() => {}} />);
    await flush();
    const rows = screen.getAllByTestId("held-terminal");
    // In the person's own clock: 14:02, or 02:02 PM.
    expect(rows[0].textContent).toMatch(/Opened by Laptop, (14:02|02:02\s?PM), running/);
    expect(rows[1].textContent).toMatch(/Opened by Phone, 09:30( AM)?, exit 0 at 09:41/);
  });

  it("reopens a Terminal started with Run as root as root, and one whose image runs as root as the image's user (#8)", async () => {
    const opened: string[] = [];
    const list = [held({ id: "aaaaaaaaaaaaaaaa", running: true, user: "root", root: true }), held({ id: "bbbbbbbbbbbbbbbb", running: true, user: "root", root: false })];
    render(<TerminalList service="kuma" target={target} deps={{ list: async () => list, end: async () => {}, terminal: { url: async () => "ws://x", socket } }} onOpen={(key) => opened.push(key)} />);
    await flush();
    const rows = screen.getAllByTestId("held-terminal");
    fireEvent.click(within(rows[0]).getByRole("button", { name: "Open" }));
    fireEvent.click(within(rows[1]).getByRole("button", { name: "Open" }));
    await flush();
    expect(getTerminal(opened[0])?.session.choice.root).toBe(true);
    expect(getTerminal(opened[1])?.session.choice.root).toBeFalsy();
  });

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
    const opened: string[] = [];
    render(<TerminalList service="kuma" target={target} deps={deps} onOpen={(key) => opened.push(key)} />);
    await flush();
    const rows = screen.getAllByTestId("held-terminal");
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toMatch(/, running/);
    expect(rows[1].textContent).toMatch(/exit 3/);
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
    expect(opened).toHaveLength(1);
    expect(getTerminal(opened[0])?.session.info.id).toBe("bbbbbbbbbbbbbbbb");
  });

  it("starts a new Terminal where and as whom chosen (#7), and says when the Node has none", async () => {
    const opened: string[] = [];
    render(<TerminalList service="photos" target={target} containers={["server", "database"]} deps={{ list: async () => [], end: async () => {}, terminal: { url: async () => "ws://x", socket } }} onOpen={(key) => opened.push(key)} />);
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
    expect(getTerminal(opened[0])?.service).toBe("photos");
    expect(opened).toHaveLength(1);
  });
});
