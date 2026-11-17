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
import { TerminalSession, type SocketFactory, type TerminalChoice } from "./session";

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
  /** Open in your terminal (#11): the Desktop's `open_in_terminal`. */
  openInTerminal?: (t: Target, service: string, id: string) => Promise<void>;
}

/** Runs the Desktop's bridge in the person's own terminal app, attached to
 * Terminal `id` with a one-time token; rejects with the Console's words
 * (e.g. that no terminal app was found). */
const openInTerminal = (t: Target, service: string, terminal: string) =>
  invoke<void>("open_in_terminal", { addr: t.addr, fingerprint: t.fingerprint, service, terminal });

const desktop: TerminalDeps = {
  url: (t, service) => invoke<string>("terminal_url", { addr: t.addr, fingerprint: t.fingerprint, service }),
  openInTerminal,
};

const open = new Map<string, OpenTerminal & { deps: TerminalDeps }>();
const listeners = new Set<() => void>();
let version = 0;

function emit() {
  version++;
  listeners.forEach((l) => l());
}

function start(service: string, target: Target, deps: TerminalDeps, choice: TerminalChoice, attach?: string): TerminalSession {
  open.get(service)?.session.close();
  const session = new TerminalSession({ service, url: () => deps.url(target, service), socket: deps.socket, attach, choice });
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

/**
 * The view of a Terminal the Node holds, by its id, in place of the
 * Service's open one. choice: where it runs and as whom, as far as known,
 * for Run again once it ends.
 */
export function attachTerminal(service: string, target: Target, id: string, deps: TerminalDeps = desktop, choice: TerminalChoice = {}): TerminalSession {
  const cur = open.get(service);
  if (cur && cur.session.info.id === id && cur.session.info.phase !== "lost") return cur.session;
  return start(service, target, deps, choice, id);
}

/** Attach again to the Terminal whose connection was lost. */
export function reattach(service: string): TerminalSession | undefined {
  const cur = open.get(service);
  if (!cur?.session.info.id) return undefined;
  return start(service, cur.target, cur.deps, cur.session.choice, cur.session.info.id);
}

/** A new shell in place of the one that ended, where it ran and as whom. */
export function runAgain(service: string): TerminalSession | undefined {
  const cur = open.get(service);
  if (!cur) return undefined;
  return start(service, cur.target, cur.deps, cur.session.choice);
}

/** Close the view. The Terminal runs on, on the Node. */
export function closeTerminal(service: string) {
  const cur = open.get(service);
  if (!cur) return;
  cur.session.close();
  open.delete(service);
  emit();
}

/**
 * Open in your terminal (#11): attach the person's own terminal app to the
 * Terminal this view shows. The view stays open; both show the same
 * session. Rejects with a message for the Console.
 */
export async function openInYourTerminal(service: string): Promise<void> {
  const cur = open.get(service);
  const id = cur?.session.info.id;
  if (!cur || !id) throw new Error("This Terminal is not running.");
  await (cur.deps.openInTerminal ?? openInTerminal)(cur.target, service, id);
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
