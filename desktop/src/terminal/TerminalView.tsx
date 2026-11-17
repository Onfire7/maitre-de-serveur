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
  resize(cols: number, rows: number): void;
}

export function TerminalView({ io, active, className }: { io: TerminalIO; active: boolean; className?: string }) {
  const box = useRef<HTMLDivElement>(null);
  const term = useRef<{ t: Terminal; fit: FitAddon } | null>(null);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const t = new Terminal({ cursorBlink: true, fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace", fontSize: 13, scrollback: 5000, allowProposedApi: true });
    const fit = new FitAddon();
    t.loadAddon(fit);
    t.open(el);
    term.current = { t, fit };
    const fitNow = () => {
      // A hidden view has no size to fit to.
      if (!el.offsetWidth || !el.offsetHeight) return;
      try {
        fit.fit();
      } catch {
        return;
      }
      io.resize(t.cols, t.rows);
    };
    fitNow();
    const off = io.onOutput((d) => t.write(d));
    const keys = t.onData((d) => io.input(d));
    const ro = new ResizeObserver(fitNow);
    ro.observe(el);
    return () => {
      ro.disconnect();
      keys.dispose();
      off();
      t.dispose();
      term.current = null;
    };
  }, [io]);
  useEffect(() => {
    const cur = term.current;
    if (!active || !cur) return;
    try {
      cur.fit.fit();
      io.resize(cur.t.cols, cur.t.rows);
    } catch {
      /* not laid out yet */
    }
    cur.t.focus();
  }, [active, io]);
  return <div ref={box} data-testid="terminal-view" className={className} />;
}
