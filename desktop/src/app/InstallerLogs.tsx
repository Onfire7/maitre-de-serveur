// The log of a machine running the Installer (decision 271): its journal,
// read over the network and refreshed while open, so a failed install can be
// read from here rather than at the machine's screen.
import { useEffect, useState } from "react";
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { InstallerService } from "@/gen/maitre/v1/installer_pb";
import { transportFor } from "@/transport";
import { api } from "@/state";

/** Reads the last lines of the Installer's log at addr. */
export async function installerLogs(addr: string, lines = 300): Promise<string[]> {
  const p = await api.peekInstaller(addr);
  const r = await createClient(InstallerService, transportFor({ addr, fingerprint: p.fingerprint })).getLogs({ lines });
  return r.lines;
}

function message(e: unknown) {
  const ce = ConnectError.from(e);
  return ce.code === Code.Unimplemented ? "This Installer is too old to send its log: read it on the machine's screen (press 3)." : ce.rawMessage;
}

export function InstallerLogs({ addr, label, onClose }: { addr: string; label: string; onClose: () => void }) {
  const [lines, setLines] = useState<string[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let stop = false;
    const read = () =>
      installerLogs(addr)
        .then((l) => !stop && (setLines(l), setError("")))
        .catch((e) => !stop && setError(message(e)));
    read();
    const t = setInterval(read, 3000);
    return () => {
      stop = true;
      clearInterval(t);
    };
  }, [addr]);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-4xl">
        <DialogHeader>
          <DialogTitle>Installer log: {label}</DialogTitle>
          <DialogDescription>The last lines of the Installer's journal on {addr}, newest last, refreshed every few seconds.</DialogDescription>
        </DialogHeader>
        {error ? (
          <p className="text-sm text-destructive">{error}</p>
        ) : lines === null ? (
          <p className="text-sm text-muted-foreground">Reading…</p>
        ) : (
          <pre className="max-h-[60vh] overflow-auto rounded-md border bg-muted/30 p-2 font-mono text-[11px] whitespace-pre-wrap break-all">{lines.join("\n")}</pre>
        )}
      </DialogContent>
    </Dialog>
  );
}
