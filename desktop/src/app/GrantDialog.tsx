// The warning a Service's protected options raise (decision 189). A Node
// refuses to run them until a Trusted Device signs a grant for each; this
// dialog names each option and what it allows, and resolves the question
// withGrants (src/fleet/grants.ts) asked.
import { useEffect, useRef, useState } from "react";
import { ShieldAlert } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import type { GrantsNeeded } from "@/gen/maitre/v1/fleet_pb";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { optionNote, setGrantAsker } from "@/fleet/grants";

export function GrantDialogHost({ snap }: { snap: FleetSnapshot | undefined }) {
  const [need, setNeed] = useState<GrantsNeeded | null>(null);
  const resolve = useRef<((ok: boolean) => void) | null>(null);
  useEffect(() => {
    setGrantAsker(
      (n) =>
        new Promise<boolean>((res) => {
          // A second question while one is open declines the first.
          resolve.current?.(false);
          resolve.current = res;
          setNeed(n);
        }),
    );
    return () => {
      setGrantAsker(undefined);
      resolve.current?.(false);
      resolve.current = null;
    };
  }, []);
  const answer = (ok: boolean) => {
    resolve.current?.(ok);
    resolve.current = null;
    setNeed(null);
  };
  const many = !!need && need.options.length > 1;
  const nodeName = need ? (snap?.nodes.find((n) => n.nodeId === need.nodeId)?.name ?? need.nodeId.slice(0, 8)) : "";
  return (
    <AlertDialog open={!!need} onOpenChange={(o) => !o && answer(false)}>
      <AlertDialogContent className="sm:max-w-xl">
        <AlertDialogHeader>
          <AlertDialogTitle className="flex items-center gap-2">
            <ShieldAlert className="size-5 text-destructive" /> Give {need?.service} power over {nodeName}?
          </AlertDialogTitle>
          <AlertDialogDescription>
            {need?.service} on {nodeName} runs <code className="rounded bg-muted px-1">{need?.imageRepository}</code> and asks for {many ? "these options" : "this option"}, which Maître keeps from containers unless you allow {many ? "them" : "it"}:
          </AlertDialogDescription>
        </AlertDialogHeader>
        <ul className="max-h-72 space-y-2 overflow-y-auto">
          {need?.options.map((o) => (
            <li key={o} className="rounded-lg border p-3 text-sm">
              <code className="block break-all font-mono text-xs">{o}</code>
              <span className="mt-1 block text-muted-foreground">{optionNote(o)}</span>
            </li>
          ))}
        </ul>
        <div className="rounded-lg border border-destructive/60 bg-destructive/5 p-3 text-sm">
          <b>{many ? "These" : "This"} can give the container control of {nodeName}</b>, and through it of everything stored there. Allow {many ? "them" : "it"} only if you trust this image and need what it does with {many ? "them" : "it"}. A different image needs your permission again; you can take it back from the Service's menu.
        </div>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction variant="destructive" onClick={() => answer(true)}>
            {many ? `Allow all ${need!.options.length} and continue` : "Allow and continue"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
