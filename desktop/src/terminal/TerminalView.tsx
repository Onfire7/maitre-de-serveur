// Draws a Terminal's output and takes its keys (ADR 0011). This is the one
// place that knows the terminal library: xterm.js with its fit addon for
// now, until the prototype of #10 chooses (docs/todo.md, "which terminal
// library"). What is around it sees only the props below, so swapping the
// library changes this file and nothing else.
import { useEffect, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";

/** What the view draws from and sends to: a session (session.ts). */
export interface TerminalIO {
  onOutput(l: (data: Uint8Array) => void): () => void;
  input(data: string): void;
  /** The view's own size: it becomes the session's (#6). */
  resize(cols: number, rows: number): void;
  /** The session's size, which another Device may have set: the view shows it. */
  onSize(l: (cols: number, rows: number) => void): () => void;
}

export function TerminalView({ io, active, className }: { io: TerminalIO; active: boolean; className?: string }) {
  const box = useRef<HTMLDivElement>(null);
  const term = useRef<{ t: Terminal; fitNow: () => void } | null>(null);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const t = new Terminal({ cursorBlink: true, fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace", fontSize: 13, scrollback: 5000, allowProposedApi: true });
    const fit = new FitAddon();
    t.loadAddon(fit);
    t.open(el);
    // The session is shared (#6): the view says its own size, which the
    // Node makes the session's when this Device types or resizes, and
    // shows the session at the size the Node says, which may be another's.
    let shared: { cols: number; rows: number } | undefined;
    const show = (cols: number, rows: number) => {
      if (t.cols !== cols || t.rows !== rows) t.resize(cols, rows);
    };
    const fitNow = () => {
      // A hidden view has no size to fit to.
      if (!el.offsetWidth || !el.offsetHeight) return;
      let d: { cols: number; rows: number } | undefined;
      try {
        d = fit.proposeDimensions();
      } catch {
        return;
      }
      if (!d || !(d.cols >= 1) || !(d.rows >= 1)) return;
      io.resize(d.cols, d.rows);
      if (!shared) show(d.cols, d.rows);
    };
    term.current = { t, fitNow };
    fitNow();
    const offSize = io.onSize((cols, rows) => {
      shared = { cols, rows };
      show(cols, rows);
    });
    const off = io.onOutput((d) => t.write(d));
    const keys = t.onData((d) => io.input(d));
    const ro = new ResizeObserver(fitNow);
    ro.observe(el);
    return () => {
      ro.disconnect();
      keys.dispose();
      off();
      offSize();
      t.dispose();
      term.current = null;
    };
  }, [io]);
  useEffect(() => {
    const cur = term.current;
    if (!active || !cur) return;
    cur.fitNow();
    cur.t.focus();
  }, [active, io]);
  return <div ref={box} data-testid="terminal-view" className={className} />;
}
