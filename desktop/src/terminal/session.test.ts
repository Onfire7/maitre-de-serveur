import { beforeEach, describe, expect, it } from "vitest";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { TerminalExit_Reason, TerminalFrameSchema, type TerminalFrame } from "@/gen/maitre/v1/terminal_pb";
import { TerminalSession, type TerminalSocket } from "./session";
import { attachTerminal, closeTerminal, getTerminal, openTerminal, reattach, resetTerminals, runAgain } from "./terminals";
import { IDLE_MS, MAX_LIVE, getFrame, resetFrames, showFrame, hideFrame, tick } from "@/app/frames";

/** A WebSocket the test plays the Node's end of. */
class FakeSocket implements TerminalSocket {
  binaryType = "blob";
  sent: TerminalFrame[] = [];
  closed = false;
  onopen: (() => void) | null = null;
  onmessage: ((e: { data: ArrayBuffer }) => void) | null = null;
  onclose: ((e: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(public url: string) {}
  send(d: Uint8Array) {
    this.sent.push(fromBinary(TerminalFrameSchema, d));
  }
  close() {
    this.closed = true;
  }
  node(frame: TerminalFrame["frame"]) {
    const b = toBinary(TerminalFrameSchema, create(TerminalFrameSchema, { frame }));
    this.onmessage?.({ data: b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength) as ArrayBuffer });
  }
}

const flush = () => new Promise((r) => setTimeout(r, 0));

function session(urls: string[] = ["ws://127.0.0.1:41000/t1"], attach?: string) {
  const sockets: FakeSocket[] = [];
  const s = new TerminalSession({
    service: "kuma",
    attach,
    url: async () => urls.shift() ?? "ws://none",
    socket: (url) => {
      const f = new FakeSocket(url);
      sockets.push(f);
      return f;
    },
  });
  return { s, sockets };
}

describe("a Terminal's session (ADR 0011)", () => {
  it("opens with a fresh URL, starts the Service's shell at the view's size, and says where it runs", async () => {
    const { s, sockets } = session();
    s.resize(120, 40);
    await flush();
    expect(sockets[0].url).toBe("ws://127.0.0.1:41000/t1");
    expect(sockets[0].binaryType).toBe("arraybuffer");
    expect(s.info.phase).toBe("connecting");
    sockets[0].onopen?.();
    const start = sockets[0].sent[0].frame;
    expect(start.case).toBe("start");
    expect(start.case === "start" && [start.value.service, start.value.size?.cols, start.value.size?.rows]).toEqual(["kuma", 120, 40]);
    sockets[0].node({ case: "started", value: { $typeName: "maitre.v1.TerminalStarted", id: "ab", container: "server", user: "root", argv: ["/bin/bash"], startedUnixMs: 1n } });
    expect(s.info).toMatchObject({ phase: "running", container: "server", user: "root" });
  });

  it("passes what is typed and the view's size to the Node, and the Node's output to the view", async () => {
    const { s, sockets } = session();
    await flush();
    sockets[0].onopen?.();
    const seen: string[] = [];
    s.onOutput((d) => seen.push(new TextDecoder().decode(d)));
    s.input("ls\r");
    s.resize(100, 30);
    const [, input, resize] = sockets[0].sent.map((f) => f.frame);
    expect(input.case === "input" && new TextDecoder().decode(input.value.data)).toBe("ls\r");
    expect(resize.case === "resize" && [resize.value.size?.cols, resize.value.size?.rows]).toEqual([100, 30]);
    sockets[0].node({ case: "output", value: { $typeName: "maitre.v1.TerminalOutput", data: new TextEncoder().encode("a b c\r\n") } });
    expect(seen).toEqual(["a b c\r\n"]);
  });

  it("keeps output that came before the view listened, so nothing is lost while it mounts", async () => {
    const { s, sockets } = session();
    await flush();
    sockets[0].onopen?.();
    sockets[0].node({ case: "output", value: { $typeName: "maitre.v1.TerminalOutput", data: new TextEncoder().encode("motd\r\n") } });
    const seen: string[] = [];
    s.onOutput((d) => seen.push(new TextDecoder().decode(d)));
    expect(seen).toEqual(["motd\r\n"]);
  });

  it("ends with the shell's exit code", async () => {
    const { s, sockets } = session();
    await flush();
    sockets[0].onopen?.();
    sockets[0].node({ case: "exit", value: { $typeName: "maitre.v1.TerminalExit", reason: TerminalExit_Reason.EXITED, code: 130, message: "" } });
    sockets[0].onclose?.({ code: 1000, reason: "" });
    expect(s.info).toMatchObject({ phase: "exited", code: 130 });
  });

  it("says when the container has no shell", async () => {
    const { s, sockets } = session();
    await flush();
    sockets[0].onopen?.();
    sockets[0].node({ case: "exit", value: { $typeName: "maitre.v1.TerminalExit", reason: TerminalExit_Reason.NO_SHELL, code: 0, message: "This container has no shell" } });
    expect(s.info).toMatchObject({ phase: "no-shell", message: "This container has no shell" });
  });

  it("says why when the connection fails or drops before the exit", async () => {
    const failing = new TerminalSession({ service: "kuma", url: async () => Promise.reject(new Error("Node unreachable")), socket: () => new FakeSocket("x") });
    await flush();
    expect(failing.info).toMatchObject({ phase: "failed", message: "Node unreachable" });
    const { s, sockets } = session();
    await flush();
    sockets[0].onopen?.();
    sockets[0].onclose?.({ code: 1006, reason: "" });
    expect(s.info.phase).toBe("failed");
    expect(s.info.message).toMatch(/connection/i);
  });
});

describe("a Terminal held by the Node (#5)", () => {
  it("reattaches by its id at the view's size, and the Node's replay comes first", async () => {
    const { s, sockets } = session(undefined, "0123456789abcdef");
    s.resize(100, 30);
    await flush();
    sockets[0].onopen?.();
    const attach = sockets[0].sent[0].frame;
    expect(attach.case === "attach" && [attach.value.id, attach.value.size?.cols, attach.value.size?.rows]).toEqual(["0123456789abcdef", 100, 30]);
    sockets[0].node({ case: "started", value: { $typeName: "maitre.v1.TerminalStarted", id: "0123456789abcdef", container: "server", user: "root", argv: ["/bin/bash"], startedUnixMs: 1n } });
    sockets[0].node({ case: "output", value: { $typeName: "maitre.v1.TerminalOutput", data: new TextEncoder().encode("earlier\r\n") } });
    const seen: string[] = [];
    s.onOutput((d) => seen.push(new TextDecoder().decode(d)));
    expect(seen).toEqual(["earlier\r\n"]);
    expect(s.info).toMatchObject({ phase: "running", id: "0123456789abcdef" });
  });

  it("knows the Terminal ran on when the connection drops, and End asks the Node to end it", async () => {
    const { s, sockets } = session();
    await flush();
    sockets[0].onopen?.();
    sockets[0].node({ case: "started", value: { $typeName: "maitre.v1.TerminalStarted", id: "ab12", container: "server", user: "root", argv: ["/bin/bash"], startedUnixMs: 1n } });
    s.end();
    expect(sockets[0].sent.at(-1)?.frame.case).toBe("end");
    sockets[0].onclose?.({ code: 1001, reason: "" });
    expect(s.info).toMatchObject({ phase: "lost", id: "ab12" });
  });

  it("closing it only detaches, quietly", async () => {
    const { s, sockets } = session();
    await flush();
    sockets[0].onopen?.();
    sockets[0].node({ case: "started", value: { $typeName: "maitre.v1.TerminalStarted", id: "ab12", container: "server", user: "root", argv: ["/bin/bash"], startedUnixMs: 1n } });
    s.close();
    sockets[0].onclose?.({ code: 1000, reason: "" });
    expect(sockets[0].sent.some((f) => f.frame.case === "end")).toBe(false);
    expect(s.info.phase).toBe("running");
  });
});

describe("the Console's open Terminals", () => {
  const target = { addr: "192.168.1.5:7443", fingerprint: "ab".repeat(32) };
  beforeEach(() => {
    resetTerminals();
    resetFrames(() => 0);
  });

  it("asks for a new one-time URL each time, and Run again starts a new shell", async () => {
    const urls: string[] = [];
    const sockets: FakeSocket[] = [];
    let n = 0;
    const deps = {
      url: async (t: typeof target, service: string) => {
        const u = `ws://127.0.0.1:41000/${service}-${++n}`;
        urls.push(u);
        expect(t).toEqual(target);
        return u;
      },
      socket: (u: string) => {
        const f = new FakeSocket(u);
        sockets.push(f);
        return f;
      },
    };
    const first = openTerminal("kuma", target, deps);
    expect(openTerminal("kuma", target, deps)).toBe(first);
    await flush();
    sockets[0].onopen?.();
    sockets[0].node({ case: "exit", value: { $typeName: "maitre.v1.TerminalExit", reason: TerminalExit_Reason.EXITED, code: 1, message: "" } });
    const again = runAgain("kuma");
    expect(again).not.toBe(first);
    expect(sockets[0].closed).toBe(true);
    await flush();
    expect(urls).toEqual(["ws://127.0.0.1:41000/kuma-1", "ws://127.0.0.1:41000/kuma-2"]);
    expect(getTerminal("kuma")?.session).toBe(again);
    closeTerminal("kuma");
    expect(getTerminal("kuma")).toBeUndefined();
    expect(sockets[1].closed).toBe(true);
  });

  it("opens a Terminal the Node holds by its id, and reattaches to the one whose connection was lost", async () => {
    const sockets: FakeSocket[] = [];
    const deps = { url: async () => "ws://x", socket: (u: string) => { const f = new FakeSocket(u); sockets.push(f); return f; } };
    const first = attachTerminal("kuma", target, "0123456789abcdef", deps);
    await flush();
    sockets[0].onopen?.();
    expect(sockets[0].sent[0].frame.case).toBe("attach");
    sockets[0].node({ case: "started", value: { $typeName: "maitre.v1.TerminalStarted", id: "0123456789abcdef", container: "server", user: "root", argv: ["/bin/bash"], startedUnixMs: 1n } });
    sockets[0].onclose?.({ code: 1001, reason: "" });
    const again = reattach("kuma");
    expect(again).not.toBe(first);
    await flush();
    sockets[1].onopen?.();
    const f = sockets[1].sent[0].frame;
    expect(f.case === "attach" && f.value.id).toBe("0123456789abcdef");
  });

  it("starts in the chosen container, as root when asked, and Run again keeps the choice (#7)", async () => {
    const sockets: FakeSocket[] = [];
    const deps = {
      url: async () => "ws://x",
      socket: (u: string) => {
        const f = new FakeSocket(u);
        sockets.push(f);
        return f;
      },
    };
    openTerminal("photos", target, deps, { component: "database", root: true });
    await flush();
    sockets[0].onopen?.();
    const start = (i: number) => {
      const f = sockets[i].sent[0].frame;
      return f.case === "start" ? [f.value.service, f.value.component, f.value.root] : f.case;
    };
    expect(start(0)).toEqual(["photos", "database", true]);
    runAgain("photos");
    await flush();
    sockets[1].onopen?.();
    expect(start(1)).toEqual(["photos", "database", true]);
    // Without a choice: the first container, as the image's user.
    openTerminal("kuma", target, deps);
    await flush();
    sockets[2].onopen?.();
    expect(start(2)).toEqual(["kuma", "", false]);
  });

  it("an open Terminal is neither dropped by the frames' idle timer nor counted in their limit", async () => {
    openTerminal("kuma", target, { url: async () => "ws://x", socket: (u) => new FakeSocket(u) });
    for (let i = 0; i < MAX_LIVE; i++) {
      showFrame(`page${i}`, `http://127.0.0.1:${41001 + i}/`);
      hideFrame(`page${i}`);
    }
    // MAX_LIVE Pages stay live beside the Terminal.
    expect(Array.from({ length: MAX_LIVE }, (_, i) => getFrame(`page${i}`)?.live)).toEqual(Array(MAX_LIVE).fill(true));
    tick(IDLE_MS * 5);
    expect(getFrame("page0")?.live).toBe(false);
    expect(getTerminal("kuma")).toBeDefined();
  });
});
