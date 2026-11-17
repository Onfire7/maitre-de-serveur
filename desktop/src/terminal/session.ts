// One view's connection to a Terminal (ADR 0011): a WebSocket to the Desktop's
// Terminal proxy, at a URL with a one-time token, which relays it over
// the Service tunnel to the Node's Terminal endpoint. Every binary message
// is one TerminalFrame (api/maitre/v1/terminal.proto). The session knows
// nothing of how it is drawn: the view (TerminalView) listens to its
// output and sends what is typed and its size. The Terminal belongs to the
// Node: closing this connection only detaches, and a new session can attach
// to it again by its id, getting its recent output replayed first.
// Terminals are shared (#6): other Devices may be attached too, and the one
// that last typed or resized sets the session's size, which the Node says
// to every view (onSize) so each shows the session at that size.
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

/** Where a Terminal runs and as whom (#7): the component ("" the first) and Run as root. */
export interface TerminalChoice {
  component?: string;
  root?: boolean;
}

export const browserSocket: SocketFactory = (url) => new WebSocket(url) as unknown as TerminalSocket;

/** What a view knows of its Terminal session (not the Node's maitre.v1.TerminalInfo). */
export interface SessionState {
  /**
   * connecting → running → exited; or no-shell, or failed with a message;
   * or lost: the connection closed while the Terminal ran on, on the Node.
   */
  phase: "connecting" | "running" | "exited" | "no-shell" | "failed" | "lost";
  /** The Node's id for the Terminal, once it says. */
  id?: string;
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
type SizeListener = (cols: number, rows: number) => void;

/** Output kept for a view that is not listening yet. */
const BACKLOG_BYTES = 256 * 1024;

export class TerminalSession {
  readonly service: string;
  readonly choice: TerminalChoice;
  info: SessionState = { phase: "connecting", container: "", user: "" };
  private socket: TerminalSocket | undefined;
  private open = false;
  private closed = false;
  private size = { cols: 80, rows: 24 };
  private listeners = new Set<Listener>();
  private outputs = new Set<OutputListener>();
  private sizes = new Set<SizeListener>();
  /** The session's size, as the Node last said. */
  private shared: { cols: number; rows: number } | undefined;
  private backlog: Uint8Array[] = [];
  private backlogBytes = 0;

  /**
   * attach: the id of a Terminal the Node holds, to reattach to it rather
   * than start one. choice: where a new one runs and as whom (#7); kept
   * for Run again.
   */
  constructor(opts: { service: string; url: () => Promise<string>; socket?: SocketFactory; attach?: string; choice?: TerminalChoice }) {
    this.service = opts.service;
    this.choice = opts.choice ?? {};
    if (opts.attach) this.info = { ...this.info, id: opts.attach };
    const factory = opts.socket ?? browserSocket;
    opts.url().then(
      (url) => {
        if (this.closed) return;
        const s = factory(url);
        s.binaryType = "arraybuffer";
        s.onopen = () => {
          this.open = true;
          const size = { $typeName: "maitre.v1.TerminalSize", ...this.size } as const;
          if (opts.attach) this.send({ case: "attach", value: { $typeName: "maitre.v1.TerminalAttach", id: opts.attach, size } });
          else this.send({ case: "start", value: { $typeName: "maitre.v1.TerminalStart", service: this.service, component: this.choice.component ?? "", root: !!this.choice.root, size } });
        };
        s.onmessage = (e) => this.receive(e.data);
        s.onclose = (e) => {
          this.open = false;
          if (this.closed) return;
          if (this.info.phase === "running") {
            // The Terminal runs on, on the Node; the view can reattach.
            this.set({ phase: "lost", message: "The connection to the Node was lost. The Terminal is still running there." });
          } else if (this.info.phase === "connecting") {
            this.set({ phase: "failed", message: e.reason ? `The connection to the Node closed: ${e.reason}` : "The connection to the Node closed before the Terminal started." });
          }
        };
        s.onerror = () => {};
        this.socket = s;
      },
      (e) => this.set({ phase: "failed", message: String((e as Error)?.message ?? e) }),
    );
  }

  private set(p: Partial<SessionState>) {
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
        this.set({ phase: "running", id: f.frame.value.id, container: f.frame.value.container, user: f.frame.value.user });
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
      case "resize": {
        const { cols = 0, rows = 0 } = f.frame.value.size ?? {};
        if (cols < 1 || rows < 1) return;
        this.shared = { cols, rows };
        this.sizes.forEach((l) => l(cols, rows));
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

  /**
   * The view's own size in cells, said again only when it changes; it
   * becomes the session's (#6). Before the shell starts, the size it starts at.
   */
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

  /** Hear the session's size, which another Device's view may have set; the current one first. */
  onSize(l: SizeListener): () => void {
    if (this.shared) l(this.shared.cols, this.shared.rows);
    this.sizes.add(l);
    return () => this.sizes.delete(l);
  }

  /** End the Terminal on the Node: SIGHUP, then SIGKILL after 5 seconds. Its exit comes back. */
  end() {
    this.send({ case: "end", value: { $typeName: "maitre.v1.TerminalEnd" } });
  }

  subscribe(l: Listener): () => void {
    this.listeners.add(l);
    return () => this.listeners.delete(l);
  }

  /** Detach: the Terminal runs on, on the Node. */
  close() {
    this.closed = true;
    this.socket?.close();
  }
}
