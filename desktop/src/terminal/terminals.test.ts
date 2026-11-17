// The Console's open Terminal views (terminals.ts): closing a Service's
// views leaves the others, and Open in your terminal (#11) attaches the
// person's own terminal app only to a Terminal that is running.
import { beforeEach, describe, expect, it } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { TerminalFrameSchema, type TerminalFrame } from "@/gen/maitre/v1/terminal_pb";
import type { TerminalSocket } from "./session";
import { closeServiceTerminals, openInYourTerminal, openTerminal, resetTerminals, terminalsOf, type TerminalDeps } from "./terminals";

class FakeSocket implements TerminalSocket {
  binaryType = "blob";
  closed = false;
  onopen: (() => void) | null = null;
  onmessage: ((e: { data: ArrayBuffer }) => void) | null = null;
  onclose: ((e: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  send() {}
  close() {
    this.closed = true;
  }
  node(frame: TerminalFrame["frame"]) {
    const b = toBinary(TerminalFrameSchema, create(TerminalFrameSchema, { frame }));
    this.onmessage?.({ data: b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength) as ArrayBuffer });
  }
}

const target = { addr: "127.0.0.1:7443", fingerprint: "ab".repeat(32) };
const flush = () => new Promise((r) => setTimeout(r, 0));

function deps() {
  const sockets: FakeSocket[] = [];
  const opened: string[][] = [];
  const d: TerminalDeps = {
    url: async () => "ws://127.0.0.1:41000/t",
    socket: () => {
      const s = new FakeSocket();
      sockets.push(s);
      return s;
    },
    openInTerminal: async (t, service, id) => {
      opened.push([t.addr, service, id]);
    },
  };
  return { d, sockets, opened };
}

beforeEach(() => resetTerminals());

describe("the Console's Terminal views", () => {
  it("closes every view of one Service and only those, leaving the Terminals on the Node", async () => {
    const { d, sockets } = deps();
    openTerminal("kuma", target, d);
    openTerminal("kuma", target, d);
    openTerminal("vault", target, d);
    await flush();
    closeServiceTerminals("kuma");
    expect(terminalsOf("kuma")).toEqual([]);
    expect(terminalsOf("vault")).toHaveLength(1);
    expect(sockets.map((s) => s.closed)).toEqual([true, true, false]);
  });

  it("opens the person's own terminal on the view's running Terminal, and refuses before it has started", async () => {
    const { d, sockets, opened } = deps();
    const view = openTerminal("kuma", target, d);
    await expect(openInYourTerminal(view.key)).rejects.toThrow("This Terminal is not running.");
    await expect(openInYourTerminal("no-such-view")).rejects.toThrow("This Terminal is not running.");
    await flush();
    sockets[0].onopen?.();
    sockets[0].node({ case: "started", value: { $typeName: "maitre.v1.TerminalStarted", id: "0123456789abcdef", container: "kuma", user: "root", argv: ["/bin/bash"], startedUnixMs: 1n } });
    await openInYourTerminal(view.key);
    expect(opened).toEqual([["127.0.0.1:7443", "kuma", "0123456789abcdef"]]);
    expect(terminalsOf("kuma")).toHaveLength(1);
  });
});
