// The Terminals open in this Console, one view per Service for now (ADR
// 0011; #6 keys them by Terminal). Unlike a Page's frame (app/frames.ts), an
// open Terminal is never dropped for being idle and does not count toward
// the frames' limit: it is the Console's own view. The Terminal itself
// belongs to the Node: closing the view detaches, and the Service's area
// lists what the Node holds (listTerminals) to reopen.
import { useSyncExternalStore } from "react";
import { invoke } from "@tauri-apps/api/core";
import { createClient } from "@connectrpc/connect";
import { TerminalService, type TerminalInfo as HeldTerminal } from "@/gen/maitre/v1/terminal_pb";
import { transportFor, type Target } from "@/transport";
import { TerminalSession, type SocketFactory } from "./session";

export type { HeldTerminal };

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

function start(service: string, target: Target, deps: TerminalDeps, attach?: string): TerminalSession {
  open.get(service)?.session.close();
  const session = new TerminalSession({ service, url: () => deps.url(target, service), socket: deps.socket, attach });
  session.subscribe(emit);
  open.set(service, { service, target, session, deps });
  emit();
  return session;
}

/** The Service's Terminal: the one open, else a new one. */
export function openTerminal(service: string, target: Target, deps: TerminalDeps = desktop): TerminalSession {
  return open.get(service)?.session ?? start(service, target, deps);
}

/** The view of a Terminal the Node holds, by its id, in place of the Service's open one. */
export function attachTerminal(service: string, target: Target, id: string, deps: TerminalDeps = desktop): TerminalSession {
  const cur = open.get(service);
  if (cur && cur.session.info.id === id && cur.session.info.phase !== "lost") return cur.session;
  return start(service, target, deps, id);
}

/** Attach again to the Terminal whose connection was lost. */
export function reattach(service: string): TerminalSession | undefined {
  const cur = open.get(service);
  if (!cur?.session.info.id) return undefined;
  return start(service, cur.target, cur.deps, cur.session.info.id);
}

/** A new shell in place of the one that ended. */
export function runAgain(service: string): TerminalSession | undefined {
  const cur = open.get(service);
  if (!cur) return undefined;
  return start(service, cur.target, cur.deps);
}

/** Close the view. The Terminal runs on, on the Node. */
export function closeTerminal(service: string) {
  const cur = open.get(service);
  if (!cur) return;
  cur.session.close();
  open.delete(service);
  emit();
}

/** The Service's Terminals the Node holds, running and finished, oldest first. */
export async function listTerminals(target: Target, service: string): Promise<HeldTerminal[]> {
  return (await createClient(TerminalService, transportFor(target)).listTerminals({ service })).terminals;
}

/** End a Terminal the Node holds (SIGHUP, then SIGKILL after 5 s), or forget a finished one. */
export async function endTerminal(target: Target, service: string, id: string): Promise<void> {
  await createClient(TerminalService, transportFor(target)).endTerminal({ service, id });
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
