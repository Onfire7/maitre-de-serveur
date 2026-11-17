// The Console's open Terminals (ADR 0011), in the Service's area beside
// its Pages. Every open Terminal stays mounted, hidden while another view
// is shown, so its scrollback and its shell are there when it is shown
// again; none is dropped for being idle, and none counts toward the Page
// frames' limit (frames.ts). Drawing is TerminalView's; this is the label,
// the end of the shell and Run again around it.
import { useSyncExternalStore } from "react";
import { Play, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { TerminalView } from "@/terminal/TerminalView";
import { closeTerminal, runAgain, useTerminals, type OpenTerminal } from "@/terminal/terminals";

/** Every open Terminal; `shown` names the Service whose Terminal is in view. */
export function TerminalLayer({ shown, onClose }: { shown: string | undefined; onClose: (service: string) => void }) {
  const open = useTerminals();
  return (
    <>
      {open.map((t) => (
        <TerminalPane key={t.service} terminal={t} shown={t.service === shown} onClose={() => onClose(t.service)} />
      ))}
    </>
  );
}

function TerminalPane({ terminal, shown, onClose }: { terminal: OpenTerminal; shown: boolean; onClose: () => void }) {
  const { session, service } = terminal;
  const info = useSyncExternalStore(
    (l) => session.subscribe(l),
    () => session.info,
  );
  const label = info.container ? `${info.container} · ${info.user}` : info.phase === "connecting" ? "Connecting…" : "Terminal";
  const ended = info.phase === "exited" || info.phase === "failed" || info.phase === "no-shell";
  return (
    <div data-testid="terminal-pane" data-shown={shown ? "true" : undefined} aria-hidden={!shown} className={cn("absolute inset-0 z-10 flex flex-col bg-[#101010] text-neutral-100", !shown && "invisible pointer-events-none")}>
      <div className="flex h-9 shrink-0 items-center gap-2 border-b border-white/10 px-3 text-xs">
        <span className="font-medium">{service}</span>
        <span className="font-mono text-neutral-400">{label}</span>
        <button aria-label={`Close ${service}'s Terminal`} title="Close the Terminal (ends its shell)" onClick={() => {
          closeTerminal(service);
          onClose();
        }} className="ml-auto rounded p-1 text-neutral-400 hover:bg-white/10 hover:text-neutral-100">
          <X className="size-3.5" />
        </button>
      </div>
      {info.phase === "no-shell" ? (
        <div className="flex flex-1 items-center justify-center p-8 text-sm text-neutral-300">{info.message}</div>
      ) : (
        <TerminalView io={session} active={shown} className="min-h-0 flex-1 p-1" />
      )}
      {ended && info.phase !== "no-shell" && (
        <div role="status" className="flex shrink-0 items-center gap-3 border-t border-white/10 px-3 py-2 text-sm">
          <span className="text-neutral-300">{info.phase === "exited" ? `The shell exited with code ${info.code}.` : info.message}</span>
          <Button size="sm" variant="secondary" className="ml-auto" onClick={() => runAgain(service)}>
            <Play /> Run again
          </Button>
        </div>
      )}
    </div>
  );
}
