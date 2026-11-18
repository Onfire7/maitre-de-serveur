// The Terminals open in this Console (ADR 0011), one view per Terminal: a
// Service may have several open at once (#6), and a Terminal the Node holds
// is opened here at most once. Unlike a Page's frame (app/frames.ts), an
// open Terminal is never dropped for being idle and does not count toward
// the frames' limit: it is the Console's own view. The Terminal itself
// belongs to the Node: closing the view detaches, and the Service's area
// lists what the Node holds (listTerminals) to reopen.
import { useSyncExternalStore } from "react";
import { invoke } from "@tauri-apps/api/core";
import { createClient } from "@connectrpc/connect";
import { TerminalService, type Terminal as HeldTerminal } from "@/gen/maitre/v1/terminal_pb";
import { transportFor, type Target } from "@/transport";
import { TerminalSession, type SocketFactory, type TerminalChoice } from "./session";

export type { HeldTerminal };

/** What a Node lists in NodeInfo.features when it serves Terminals. */
export const TERMINAL_FEATURE = "terminal";

export interface OpenTerminal {
  /** This view's key, the route's: it stays when the view reattaches or runs again. */
  key: string;
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

type Open = OpenTerminal & { deps: TerminalDeps };
const open = new Map<string, Open>();
const listeners = new Set<() => void>();
let version = 0;
let views = 0;

function emit() {
  version++;
  listeners.forEach((l) => l());
}

/**
 * Connect view key (a new one when undefined) to a new Terminal where and
 * as whom choice says, or to Terminal attach.
 */
function start(key: string | undefined, service: string, target: Target, deps: TerminalDeps, choice: TerminalChoice, attach?: string): Open {
  if (key) open.get(key)?.session.close();
  const session = new TerminalSession({ service, url: () => deps.url(target, service), socket: deps.socket, attach, choice });
  session.subscribe(emit);
  const view = { key: key ?? `view-${++views}`, service, target, session, deps };
  open.set(view.key, view);
  emit();
  return view;
}

/** A new Terminal in the Service, in a view of its own, in the chosen
 * container and as the chosen user (#7). */
export function openTerminal(service: string, target: Target, deps: TerminalDeps = desktop, choice: TerminalChoice = {}): OpenTerminal {
  return start(undefined, service, target, deps, choice);
}

/**
 * The view of a Terminal the Node holds, by its id: the one open here, else
 * a new one. choice: where it runs and as whom, as far as known, for Run
 * again once it ends.
 */
export function attachTerminal(service: string, target: Target, id: string, deps: TerminalDeps = desktop, choice: TerminalChoice = {}): OpenTerminal {
  const cur = [...open.values()].find((t) => t.service === service && t.session.info.id === id);
  if (cur && cur.session.info.phase !== "lost") return cur;
  return start(cur?.key, service, target, deps, choice, id);
}

/** Attach the view again to the Terminal whose connection was lost. */
export function reattach(key: string): OpenTerminal | undefined {
  const cur = open.get(key);
  if (!cur?.session.info.id) return undefined;
  return start(key, cur.service, cur.target, cur.deps, cur.session.choice, cur.session.info.id);
}

/** A new shell in the view, in place of the one that ended, where it ran and as whom. */
export function runAgain(key: string): OpenTerminal | undefined {
  const cur = open.get(key);
  if (!cur) return undefined;
  return start(key, cur.service, cur.target, cur.deps, cur.session.choice);
}

/** Close the view. The Terminal runs on, on the Node. */
export function closeTerminal(key: string) {
  const cur = open.get(key);
  if (!cur) return;
  cur.session.close();
  open.delete(key);
  emit();
}

/** Close every view of the Service's Terminals (it was removed, say). */
export function closeServiceTerminals(service: string) {
  for (const t of terminalsOf(service)) closeTerminal(t.key);
}

/**
 * Open in your terminal (#11): attach the person's own terminal app to the
 * Terminal this view shows. The view stays open; both show the same
 * session. Rejects with a message for the Console.
 */
export async function openInYourTerminal(key: string): Promise<void> {
  const cur = open.get(key);
  const id = cur?.session.info.id;
  if (!cur || !id) throw new Error("This Terminal is not running.");
  await (cur.deps.openInTerminal ?? openInTerminal)(cur.target, cur.service, id);
}

/** The Service's Terminals the Node holds, running and finished, oldest first. */
export async function listTerminals(target: Target, service: string): Promise<HeldTerminal[]> {
  return (await createClient(TerminalService, transportFor(target)).listTerminals({ service })).terminals;
}

/** End a Terminal the Node holds (SIGHUP, then SIGKILL after 5 s), or forget a finished one. */
export async function endTerminal(target: Target, service: string, id: string): Promise<void> {
  await createClient(TerminalService, transportFor(target)).endTerminal({ service, id });
}

export function getTerminal(key: string): OpenTerminal | undefined {
  return open.get(key);
}

/** The Service's open views, oldest first. */
export function terminalsOf(service: string): OpenTerminal[] {
  return [...open.values()].filter((t) => t.service === service);
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
