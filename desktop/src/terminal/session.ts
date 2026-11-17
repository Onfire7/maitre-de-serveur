// One Terminal's connection (ADR 0011): a WebSocket to the Desktop's
// Terminal proxy, at a URL with a one-time token, which relays it over
// the Service tunnel to the Node's Terminal endpoint. Every binary message
// is one TerminalFrame (api/maitre/v1/terminal.proto). The session knows
// nothing of how it is drawn: the view (TerminalView) listens to its
// output and sends what is typed and its size.
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { TerminalExit_Reason, TerminalFrameSchema, type TerminalFrame } from "@/gen/maitre/v1/terminal_pb";

/** The part of a WebSocket the session uses, so tests can play the Node. */
export interface TerminalSocket {
  binaryType: string;
  send(data: Uint8Array): void;
  close(): void;
  onopen: (() => void) | null;
  onmessage: ((e: { data: ArrayBuffer }) => void) | null;
  onclose: ((e: { code: number; reason: string }) => void) | null;
  onerror: (() => void) | null;
}

export type SocketFactory = (url: string) => TerminalSocket;

export const browserSocket: SocketFactory = (url) => new WebSocket(url) as unknown as TerminalSocket;

export interface TerminalInfo {
  /** connecting → running → exited; or no-shell, or failed with a message. */
  phase: "connecting" | "running" | "exited" | "no-shell" | "failed";
  /** Where it runs and as whom, once the Node says (the view's label). */
  container: string;
  user: string;
  /** The shell's exit code, once exited. */
  code?: number;
  /** Why, for no-shell and failed. */
  message?: string;
}

type Listener = () => void;
type OutputListener = (data: Uint8Array) => void;

/** Output kept for a view that is not listening yet. */
const BACKLOG_BYTES = 256 * 1024;

export class TerminalSession {
  readonly service: string;
  info: TerminalInfo = { phase: "connecting", container: "", user: "" };
  private socket: TerminalSocket | undefined;
  private open = false;
  private closed = false;
  private size = { cols: 80, rows: 24 };
  private listeners = new Set<Listener>();
  private outputs = new Set<OutputListener>();
  private backlog: Uint8Array[] = [];
  private backlogBytes = 0;

  constructor(opts: { service: string; url: () => Promise<string>; socket?: SocketFactory }) {
    this.service = opts.service;
    const factory = opts.socket ?? browserSocket;
    opts.url().then(
      (url) => {
        if (this.closed) return;
        const s = factory(url);
        s.binaryType = "arraybuffer";
        s.onopen = () => {
          this.open = true;
          this.send({ case: "start", value: { $typeName: "maitre.v1.TerminalStart", service: this.service, component: "", size: { $typeName: "maitre.v1.TerminalSize", ...this.size } } });
        };
        s.onmessage = (e) => this.receive(e.data);
        s.onclose = (e) => {
          this.open = false;
          if (this.info.phase === "connecting" || this.info.phase === "running") {
            this.set({ phase: "failed", message: e.reason ? `The connection to the Node closed: ${e.reason}` : "The connection to the Node closed before the shell ended." });
          }
        };
        s.onerror = () => {};
        this.socket = s;
      },
      (e) => this.set({ phase: "failed", message: String((e as Error)?.message ?? e) }),
    );
  }

  private set(p: Partial<TerminalInfo>) {
    this.info = { ...this.info, ...p };
    this.listeners.forEach((l) => l());
  }

  private send(frame: TerminalFrame["frame"]) {
    if (!this.open || !this.socket) return;
    this.socket.send(toBinary(TerminalFrameSchema, create(TerminalFrameSchema, { frame })));
  }

  private receive(data: ArrayBuffer) {
    let f: TerminalFrame;
    try {
      f = fromBinary(TerminalFrameSchema, new Uint8Array(data));
    } catch {
      return;
    }
    switch (f.frame.case) {
      case "started":
        this.set({ phase: "running", container: f.frame.value.container, user: f.frame.value.user });
        return;
      case "output": {
        const d = f.frame.value.data;
        if (this.outputs.size) this.outputs.forEach((o) => o(d));
        else {
          this.backlog.push(d);
          this.backlogBytes += d.length;
          while (this.backlogBytes > BACKLOG_BYTES && this.backlog.length > 1) this.backlogBytes -= this.backlog.shift()!.length;
        }
        return;
      }
      case "exit": {
        const x = f.frame.value;
        if (x.reason === TerminalExit_Reason.NO_SHELL) this.set({ phase: "no-shell", message: x.message || "This container has no shell" });
        else if (x.reason === TerminalExit_Reason.EXITED) this.set({ phase: "exited", code: x.code });
        else this.set({ phase: "failed", message: x.message || "The Terminal could not start." });
      }
    }
  }

  /** What was typed, as a terminal sends it. */
  input(data: string | Uint8Array) {
    const bytes = typeof data === "string" ? new TextEncoder().encode(data) : data;
    this.send({ case: "input", value: { $typeName: "maitre.v1.TerminalInput", data: bytes } });
  }

  /** The view's size in cells; before the shell starts, the size it starts at. */
  resize(cols: number, rows: number) {
    if (cols < 1 || rows < 1 || (cols === this.size.cols && rows === this.size.rows)) return;
    this.size = { cols, rows };
    this.send({ case: "resize", value: { $typeName: "maitre.v1.TerminalResize", size: { $typeName: "maitre.v1.TerminalSize", cols, rows } } });
  }

  /** Hear the output; what came before is replayed first. */
  onOutput(l: OutputListener): () => void {
    for (const d of this.backlog) l(d);
    this.backlog = [];
    this.backlogBytes = 0;
    this.outputs.add(l);
    return () => this.outputs.delete(l);
  }

  subscribe(l: Listener): () => void {
    this.listeners.add(l);
    return () => this.listeners.delete(l);
  }

  close() {
    this.closed = true;
    this.socket?.close();
  }
}
