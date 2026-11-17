// The Console's open Terminals (ADR 0011), in the Service's area beside
// its Pages. Every open Terminal stays mounted, hidden while another view
// is shown, so its scrollback and its shell are there when it is shown
// again; none is dropped for being idle, and none counts toward the Page
// frames' limit (frames.ts). Drawing is TerminalView's; this is the label,
// End, the exit and Run again around it. The Terminals belong to the Node
// (#5): closing a view detaches, and TerminalList lists the Service's
// running and finished Terminals to reopen. A Service may have several
// open, one view per Terminal, and other Devices may share them (#6).
import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import { List, Play, Plus, RotateCw, Square, SquareTerminal, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type { Target } from "@/transport";
import type { TerminalChoice } from "@/terminal/session";
import { OpenTerminalDialog } from "./OpenTerminalDialog";
import { TerminalView } from "@/terminal/TerminalView";
import {
  attachTerminal,
  closeTerminal,
  endTerminal,
  listTerminals,
  openInYourTerminal,
  openTerminal,
  reattach,
  runAgain,
  useTerminals,
  type HeldTerminal,
  type OpenTerminal,
  type TerminalDeps,
} from "@/terminal/terminals";

/**
 * Every open Terminal; `shown` is the key of the view in sight. onClose
 * follows closing a view; onList goes to the Service's Terminals.
 */
export function TerminalLayer({ shown, onClose, onList }: { shown: string | undefined; onClose: (t: OpenTerminal) => void; onList: (service: string) => void }) {
  const open = useTerminals();
  return (
    <>
      {open.map((t) => (
        <TerminalPane key={t.key} terminal={t} shown={t.key === shown} onClose={() => onClose(t)} onList={() => onList(t.service)} />
      ))}
    </>
  );
}

function TerminalPane({ terminal, shown, onClose, onList }: { terminal: OpenTerminal; shown: boolean; onClose: () => void; onList: () => void }) {
  const { session, service, key } = terminal;
  const info = useSyncExternalStore(
    (l) => session.subscribe(l),
    () => session.info,
  );
  const label = info.container ? `${info.container} · ${info.user}` : info.phase === "connecting" ? "Connecting…" : "Terminal";
  const ended = info.phase === "exited" || info.phase === "failed" || info.phase === "no-shell";
  const iconButton = "rounded p-1 text-neutral-400 hover:bg-white/10 hover:text-neutral-100";
  const [launchError, setLaunchError] = useState<string>();
  const openInTerminal = () => {
    setLaunchError(undefined);
    openInYourTerminal(key).catch((e: unknown) => setLaunchError(e instanceof Error ? e.message : String(e)));
  };
  return (
    <div data-testid="terminal-pane" data-shown={shown ? "true" : undefined} aria-hidden={!shown} className={cn("absolute inset-0 z-10 flex flex-col bg-[#101010] text-neutral-100", !shown && "invisible pointer-events-none")}>
      <div className="flex h-9 shrink-0 items-center gap-2 border-b border-white/10 px-3 text-xs">
        <span className="font-medium">{service}</span>
        <span className="font-mono text-neutral-400">{label}</span>
        <span className="ml-auto" />
        {info.phase === "running" && info.id && (
          <button aria-label={`Open ${service}'s Terminal in your terminal`} title="Open in your terminal (this view stays open on the same Terminal)" onClick={openInTerminal} className={iconButton}>
            <SquareTerminal className="size-3.5" />
          </button>
        )}
        {info.phase === "running" && (
          <button aria-label={`End ${service}'s Terminal`} title="End the Terminal (hang it up, then kill it after 5 seconds)" onClick={() => session.end()} className={iconButton}>
            <Square className="size-3.5" />
          </button>
        )}
        <button aria-label={`All of ${service}'s Terminals`} title="All Terminals (this one keeps running)" onClick={onList} className={iconButton}>
          <List className="size-3.5" />
        </button>
        <button aria-label={`Close ${service}'s Terminal`} title="Close the view (the Terminal keeps running)" onClick={() => {
          closeTerminal(key);
          onClose();
        }} className={iconButton}>
          <X className="size-3.5" />
        </button>
      </div>
      {info.phase === "no-shell" ? (
        <div className="flex flex-1 items-center justify-center p-8 text-sm text-neutral-300">{info.message}</div>
      ) : (
        <TerminalView io={session} active={shown} className="min-h-0 flex-1 p-1" />
      )}
      {launchError && (
        <div role="alert" className="flex shrink-0 items-center gap-3 border-t border-white/10 px-3 py-2 text-sm">
          <span className="text-neutral-300">{launchError}</span>
          <Button size="sm" variant="ghost" className="ml-auto" onClick={() => setLaunchError(undefined)}>
            Dismiss
          </Button>
        </div>
      )}
      {info.phase === "lost" && (
        <div role="status" className="flex shrink-0 items-center gap-3 border-t border-white/10 px-3 py-2 text-sm">
          <span className="text-neutral-300">{info.message}</span>
          <Button size="sm" variant="secondary" className="ml-auto" onClick={() => reattach(key)}>
            <RotateCw /> Reattach
          </Button>
        </div>
      )}
      {ended && info.phase !== "no-shell" && (
        <div role="status" className="flex shrink-0 items-center gap-3 border-t border-white/10 px-3 py-2 text-sm">
          <span className="text-neutral-300">{info.phase === "exited" ? `The shell exited with code ${info.code}.` : info.message}</span>
          <Button size="sm" variant="secondary" className="ml-auto" onClick={() => runAgain(key)}>
            <Play /> Run again
          </Button>
        </div>
      )}
    </div>
  );
}

/** How the list reaches the Node; tests play it. */
export interface TerminalListDeps {
  list: (t: Target, service: string) => Promise<HeldTerminal[]>;
  end: (t: Target, service: string, id: string) => Promise<void>;
  terminal?: TerminalDeps;
}

const node: TerminalListDeps = { list: listTerminals, end: endTerminal };

const time = (ms: bigint) => new Date(Number(ms)).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

/** The state of a Terminal the Node holds, in the Console's words. */
export function terminalState(t: HeldTerminal): string {
  return t.running ? `Running since ${time(t.startedUnixMs)}` : `Exited with code ${t.code} at ${time(t.endedUnixMs)}`;
}

/**
 * The Service's Terminals the Node holds (#5), running and finished, each
 * to reopen; a running one can be Ended. A finished one is kept, with its
 * exit code and output, until it has been opened or for 24 hours. There
 * may be several per container, up to 10 running (#6), and other Devices
 * may have opened them. New Terminal asks where and as whom (#7);
 * `containers` are the Service's components, none for a Service of one
 * container. onOpen goes to the view opened.
 */
export function TerminalList({ service, target, containers = [], deps = node, onOpen }: { service: string; target: Target; containers?: string[]; deps?: TerminalListDeps; onOpen: (key: string) => void }) {
  const [held, setHeld] = useState<HeldTerminal[] | undefined>();
  const [choosing, setChoosing] = useState(false);
  const [error, setError] = useState<string>();
  const refresh = useCallback(() => {
    deps.list(target, service).then(
      (ts) => {
        setHeld(ts);
        setError(undefined);
      },
      (e) => setError(String((e as Error)?.message ?? e)),
    );
  }, [deps, target, service]);
  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 5000);
    return () => clearInterval(t);
  }, [refresh]);
  return (
    <div className="h-full overflow-y-auto p-6">
      <div className="mx-auto max-w-2xl">
        <div className="mb-3 flex items-center gap-2">
          <h2 className="text-sm font-medium">{service}'s Terminals</h2>
          <Button size="sm" className="ml-auto" onClick={() => setChoosing(true)}>
            <Plus /> New Terminal
          </Button>
        </div>
        {error && <p role="alert" className="text-sm text-destructive">The Terminals could not be listed: {error}</p>}
        {!held && !error && <p className="text-sm text-muted-foreground">Asking the Node…</p>}
        {held?.length === 0 && <p className="text-sm text-muted-foreground">No Terminal is running in {service}.</p>}
        {!!held?.length && (
        <ul className="divide-y rounded-md border">
          {held.map((t) => (
            <li key={t.id} data-testid="held-terminal" className="flex items-center gap-3 px-3 py-2 text-sm">
              <span className="font-mono">{t.container} · {t.user}</span>
              <span className="font-mono text-muted-foreground">{t.argv.join(" ")}</span>
              <span className={cn("text-muted-foreground", !t.running && "text-foreground")}>{terminalState(t)}</span>
              <span className="ml-auto" />
              <Button size="sm" variant="secondary" onClick={() => onOpen(attachTerminal(service, target, t.id, deps.terminal, heldChoice(service, t)).key)}>
                Open
              </Button>
              {t.running && (
                <Button size="sm" variant="outline" onClick={() => deps.end(target, service, t.id).then(refresh, (e) => setError(String((e as Error)?.message ?? e)))}>
                  End
                </Button>
              )}
            </li>
          ))}
        </ul>
        )}
      </div>
      {choosing && (
        <OpenTerminalDialog
          service={service}
          containers={containers}
          onClose={() => setChoosing(false)}
          onOpen={(choice) => {
            setChoosing(false);
            onOpen(openTerminal(service, target, deps.terminal, choice).key);
          }}
        />
      )}
    </div>
  );
}

/**
 * Where a held Terminal ran and as whom, as far as its info tells, for Run
 * again once it ends: its container is the component's name in a Service
 * of several (the Service's own name otherwise), and a shell that ran as
 * "root" runs again as root (uid 0 either way).
 */
function heldChoice(service: string, t: HeldTerminal): TerminalChoice {
  return { ...(t.container && t.container !== service ? { component: t.container } : {}), ...(t.user === "root" ? { root: true } : {}) };
}
