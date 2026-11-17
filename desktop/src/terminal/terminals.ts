// The Terminals open in this Console, one per Service for now (ADR 0011).
// Unlike a Page's frame (app/frames.ts), an open Terminal is never dropped
// for being idle and does not count toward the frames' limit: it is the
// Console's own view, and closing it would end the shell.
import { useSyncExternalStore } from "react";
import { invoke } from "@tauri-apps/api/core";
import type { Target } from "@/transport";
import { TerminalSession, type SocketFactory, type TerminalChoice } from "./session";

/** What a Node lists in NodeInfo.features when it serves Terminals. */
export const TERMINAL_FEATURE = "terminal";

export interface OpenTerminal {
  service: string;
  target: Target;
  session: TerminalSession;
}

export interface TerminalDeps {
  /** A one-time URL for one Terminal connection (the Desktop's `terminal_url`). */
  url: (t: Target, service: string) => Promise<string>;
  socket?: SocketFactory;
}

const desktop: TerminalDeps = {
  url: (t, service) => invoke<string>("terminal_url", { addr: t.addr, fingerprint: t.fingerprint, service }),
};

const open = new Map<string, OpenTerminal & { deps: TerminalDeps }>();
const listeners = new Set<() => void>();
let version = 0;

function emit() {
  version++;
  listeners.forEach((l) => l());
}

function start(service: string, target: Target, deps: TerminalDeps, choice: TerminalChoice): TerminalSession {
  const session = new TerminalSession({ service, url: () => deps.url(target, service), socket: deps.socket, choice });
  session.subscribe(emit);
  open.set(service, { service, target, session, deps });
  emit();
  return session;
}

/** The Service's Terminal: the one open, else a new one in the chosen
 * container and as the chosen user (#7). */
export function openTerminal(service: string, target: Target, deps: TerminalDeps = desktop, choice: TerminalChoice = {}): TerminalSession {
  return open.get(service)?.session ?? start(service, target, deps, choice);
}

/** A new shell in place of the one that ended, where it ran and as whom. */
export function runAgain(service: string): TerminalSession | undefined {
  const cur = open.get(service);
  if (!cur) return undefined;
  cur.session.close();
  return start(service, cur.target, cur.deps, cur.session.choice);
}

/** Close the view, and with it (for now) the shell. */
export function closeTerminal(service: string) {
  const cur = open.get(service);
  if (!cur) return;
  cur.session.close();
  open.delete(service);
  emit();
}

export function getTerminal(service: string): OpenTerminal | undefined {
  return open.get(service);
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

export function useTerminals(): OpenTerminal[] {
  useSyncExternalStore(subscribe, () => version);
  return [...open.values()];
}

/** Tests: start over. */
export function resetTerminals() {
  for (const t of open.values()) t.session.close();
  open.clear();
  emit();
}
