// The warning before an action that ends running Terminals (ADR 0011, #9):
// stopping, restarting, updating, moving or removing a Service, applying its
// Service Configuration, and updating, restarting or rolling back its Node
// name the Terminals they will end, and still go ahead. The wording is a
// list of lines, so a confirmation can add its own (#22: how long the Node
// is down).
import { Fragment, useEffect, useRef, useState, type ReactNode } from "react";
import { createClient } from "@connectrpc/connect";
import { TerminalService } from "@/gen/maitre/v1/terminal_pb";
import { transportFor, type Target } from "@/transport";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import type { HeldTerminal } from "./terminals";

export interface WarningScope {
  /** The Terminals are anywhere on the Node, so each names its Service. */
  node?: boolean;
  /** The action covers several Services, so each names its Service. */
  services?: boolean;
  /**
   * How long the Node's Services stop, when the action restarts the Node
   * (#22): "about 2 minutes", "a few minutes" (servicesStopFor). It takes
   * the place of "It will end": the restart ends them.
   */
  downtime?: string;
}

/**
 * The lines naming the running Terminals among held, with each command in
 * backticks: "A Terminal is running in server: `app migrate`. It will end."
 * None when none is running.
 */
export function terminalWarning(held: readonly HeldTerminal[], scope: WarningScope = {}): string[] {
  const running = held.filter((t) => t.running);
  const named = (t: HeldTerminal) => {
    const service = scope.node || scope.services;
    const where = service && t.container !== t.service ? `${t.service} (${t.container})` : service ? t.service : t.container;
    return `in ${where}: ${t.argv.length ? `\`${t.argv.join(" ")}\`` : "a shell"}`;
  };
  const onNode = scope.node ? " on this Node" : "";
  if (running.length === 0) return [];
  if (running.length === 1) return [`A Terminal is running${onNode} ${named(running[0])}. ${scope.downtime ? `Its Services stop for ${scope.downtime}.` : "It will end."}`];
  const lines = [`${running.length} Terminals are running${onNode}, and will end:`, ...running.map(named)];
  return scope.downtime ? [...lines, `Their Services stop for ${scope.downtime}.`] : lines;
}

/** Every Terminal the Node holds (ListTerminals without a Service). */
export async function nodeTerminals(target: Target): Promise<HeldTerminal[]> {
  return (await createClient(TerminalService, transportFor(target)).listTerminals({ service: "" }, { timeoutMs: 4000 })).terminals;
}

/** The Node's running Terminals in these Services, or all of them; none when it cannot say. */
export async function runningTerminals(target: Target, services?: readonly string[], list: (t: Target, service: string) => Promise<HeldTerminal[]> = (t) => nodeTerminals(t)): Promise<HeldTerminal[]> {
  try {
    return (await list(target, "")).filter((t) => t.running && (!services || services.includes(t.service)));
  } catch {
    // A Node without Terminals, or not answering: nothing to warn about.
    return [];
  }
}

/** Lines with `commands` shown as code. */
export function WarningLines({ lines }: { lines: readonly string[] }) {
  if (!lines.length) return null;
  return (
    <div role="note" className="space-y-0.5 rounded-md border border-status-attention/50 bg-status-attention/10 px-3 py-2 text-sm">
      {lines.map((l, i) => (
        <p key={i}>
          {l.split("`").map((part, j) =>
            j % 2 ? (
              <code key={j} className="rounded bg-muted px-1 font-mono text-xs">
                {part}
              </code>
            ) : (
              <Fragment key={j}>{part}</Fragment>
            ),
          )}
        </p>
      ))}
    </div>
  );
}

/**
 * The running Terminals an action will end, asked of the Node when shown:
 * those of services, or every one on the Node when services is left out.
 * Shows nothing until the Node answers, or when none is running. With
 * downtime (the Node restarts), it says how long its Services stop.
 */
export function EndsTerminals({ target, services, downtime, list }: { target: Target | undefined; services?: readonly string[]; downtime?: string; list?: (t: Target, service: string) => Promise<HeldTerminal[]> }) {
  const [running, setRunning] = useState<HeldTerminal[]>([]);
  const key = services?.join("\n");
  useEffect(() => {
    if (!target) return;
    let live = true;
    runningTerminals(target, services, list).then((r) => live && setRunning(r));
    return () => {
      live = false;
    };
  }, [target?.addr, target?.fingerprint, key]);
  return <WarningLines lines={terminalWarning(running, { node: !services, services: (services?.length ?? 0) > 1, downtime })} />;
}

/** What an action without a confirmation of its own asks first, when it would end Terminals. */
export interface EndingAsk {
  title: string;
  /** The button that goes ahead: "Stop", "Restart", "Update". */
  action: string;
  /** Where the action lands: each Node, with the Services it touches. */
  groups: { target: Target | undefined; services: readonly string[] }[];
}

/**
 * Stop, Restart and Update of a Service have no confirmation; while a
 * Terminal runs in one, they ask first (ADR 0011: warn, never refuse).
 * confirm resolves true at once when none is running.
 */
export function useEndingConfirm(list?: (t: Target, service: string) => Promise<HeldTerminal[]>): [(ask: EndingAsk) => Promise<boolean>, ReactNode] {
  const [shown, setShown] = useState<{ ask: EndingAsk; lines: string[] } | null>(null);
  const answer = useRef<(go: boolean) => void>(undefined);
  const settle = (go: boolean) => {
    answer.current?.(go);
    answer.current = undefined;
    setShown(null);
  };
  async function confirm(ask: EndingAsk): Promise<boolean> {
    const running = (await Promise.all(ask.groups.map((g) => (g.target ? runningTerminals(g.target, g.services, list) : [])))).flat();
    if (!running.length) return true;
    const services = new Set(ask.groups.flatMap((g) => g.services));
    return new Promise((resolve) => {
      answer.current?.(false);
      answer.current = resolve;
      setShown({ ask, lines: terminalWarning(running, { services: services.size > 1 }) });
    });
  }
  const dialog = (
    <AlertDialog open={!!shown} onOpenChange={(o) => !o && settle(false)}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{shown?.ask.title}</AlertDialogTitle>
          <AlertDialogDescription className="sr-only">It ends the Terminals running there.</AlertDialogDescription>
        </AlertDialogHeader>
        <WarningLines lines={shown?.lines ?? []} />
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction onClick={() => settle(true)}>{shown?.ask.action}</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
  return [confirm, dialog];
}
