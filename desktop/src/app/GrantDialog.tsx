// The warning a Service's protected options raise (decision 189). A Node
// refuses to run them until a Trusted Device signs a grant for each; this
// dialog names each option and what it allows, and resolves the question
// withGrants (src/fleet/grants.ts) asked.
import { useEffect, useRef, useState } from "react";
import { ShieldAlert } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import type { GrantsNeeded } from "@/gen/maitre/v1/fleet_pb";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { declaredFor, optionNote, setGrantAsker, type GrantAnswer } from "@/fleet/grants";
import type { CatalogEntry } from "@/services";

export function GrantDialogHost({ snap }: { snap: FleetSnapshot | undefined }) {
  const [need, setNeed] = useState<GrantsNeeded | null>(null);
  const [entry, setEntry] = useState<CatalogEntry | undefined>(undefined);
  const resolve = useRef<((a: GrantAnswer) => void) | null>(null);
  useEffect(() => {
    setGrantAsker(
      (n, e) =>
        new Promise<GrantAnswer>((res) => {
          // A second question while one is open declines the first.
          resolve.current?.(false);
          resolve.current = res;
          setNeed(n);
          setEntry(e);
        }),
    );
    return () => {
      setGrantAsker(undefined);
      resolve.current?.(false);
      resolve.current = null;
    };
  }, []);
  const answer = (a: GrantAnswer) => {
    resolve.current?.(a);
    resolve.current = null;
    setNeed(null);
    setEntry(undefined);
  };
  const many = !!need && need.options.length > 1;
  // A Service of several containers names each option's container and image (plan C4).
  const multi = !!need && need.components.length > 0;
  const nodeName = need ? (snap?.nodes.find((n) => n.nodeId === need.nodeId)?.name ?? need.nodeId.slice(0, 8)) : "";
  const declared = need && entry ? declaredFor(entry, need) : undefined;
  if (need && entry && declared) return <EntryGrantDialog need={need} entry={entry} declared={declared} nodeName={nodeName} answer={answer} />;
  return (
    <AlertDialog open={!!need} onOpenChange={(o) => !o && answer(false)}>
      <AlertDialogContent className="sm:max-w-xl">
        <AlertDialogHeader>
          <AlertDialogTitle className="flex items-center gap-2">
            <ShieldAlert className="size-5 text-destructive" /> Give {need?.service} power over {nodeName}?
          </AlertDialogTitle>
          <AlertDialogDescription>
            {multi ? (
              <>
                {need?.service} on {nodeName} asks for {many ? "these options" : "this option"} for its containers
              </>
            ) : (
              <>
                {need?.service} on {nodeName} runs <code className="rounded bg-muted px-1">{need?.imageRepository}</code> and asks for {many ? "these options" : "this option"}
              </>
            )}
            , which Maître keeps from containers unless you allow {many ? "them" : "it"}:
          </AlertDialogDescription>
        </AlertDialogHeader>
        <ul className="max-h-72 space-y-2 overflow-y-auto">
          {need?.options.map((o, i) => (
            <li key={need.grantIds[i] || o} className="rounded-lg border p-3 text-sm">
              {multi && (
                <span className="mb-1 block text-xs text-muted-foreground">
                  <span className="font-medium text-foreground">{need.components[i]}</span>, running <code>{need.imageRepositories[i]}</code>
                </span>
              )}
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

const featureName = (f: string) => f.replace(/_/g, " ").replace(/^./, (c) => c.toUpperCase());

/**
 * The question for a Catalog entry that declares its grants (G8): each
 * option with the entry's reason and Maître's note, grouped into what it
 * needs to run and its optional features, and three answers.
 */
function EntryGrantDialog({ need, entry, declared, nodeName, answer }: { need: GrantsNeeded; entry: CatalogEntry; declared: NonNullable<ReturnType<typeof declaredFor>>; nodeName: string; answer: (a: GrantAnswer) => void }) {
  const items = need.options.map((option, i) => ({ option, grant: declared[i], component: need.components[i] ?? "", id: need.grantIds[i] || option }));
  const required = items.filter((x) => !x.grant.optional);
  const features = [...new Set(items.flatMap((x) => (x.grant.optional ? [x.grant.optional] : [])))];
  // Every feature allowed until the person switches one off.
  const [off, setOff] = useState<string[]>([]);
  const allowed = required.length > 0 || features.some((f) => !off.includes(f));
  const option = (x: (typeof items)[number]) => (
    <li key={x.id} className="rounded-lg border p-3 text-sm">
      <span className="block">{x.grant.why}</span>
      <code className="mt-1 block break-all font-mono text-xs text-muted-foreground">
        {x.component ? `${x.component}: ` : ""}
        {x.option}
      </code>
      <span className="mt-1 block text-xs text-muted-foreground">{optionNote(x.option)}</span>
    </li>
  );
  return (
    <AlertDialog open onOpenChange={(o) => !o && answer(false)}>
      <AlertDialogContent className="sm:max-w-xl">
        <AlertDialogHeader>
          <AlertDialogTitle className="flex items-center gap-2">
            <ShieldAlert className="size-5 text-destructive" /> {entry.name} asks for power over {nodeName}
          </AlertDialogTitle>
          <AlertDialogDescription>Maître keeps these from containers unless you allow them. {entry.name} says why it needs each:</AlertDialogDescription>
        </AlertDialogHeader>
        <div className="max-h-80 space-y-3 overflow-y-auto">
          {required.length > 0 && (
            <section aria-label="Needed to run">
              <h4 className="mb-1 text-sm font-medium">Needed to run</h4>
              <ul className="space-y-2">{required.map(option)}</ul>
            </section>
          )}
          {features.map((f) => {
            const of = items.filter((x) => x.grant.optional === f);
            return (
              <section key={f} aria-label={featureName(f)}>
                <label className="mb-1 flex items-center justify-between gap-3 text-sm font-medium">
                  <span>
                    {featureName(f)} <span className="font-normal text-muted-foreground">(optional)</span>
                  </span>
                  <Switch aria-label={`Allow ${featureName(f).toLowerCase()}`} checked={!off.includes(f)} onCheckedChange={(on) => setOff(on ? off.filter((x) => x !== f) : [...off, f])} />
                </label>
                <ul className="space-y-2">{of.map(option)}</ul>
                <p className="mt-1 text-xs text-muted-foreground">Without it: {of[0].grant.without}</p>
              </section>
            );
          })}
        </div>
        {allowed && (
          <div className="rounded-lg border border-destructive/60 bg-destructive/5 p-3 text-sm">
            <b>These can give the container control of {nodeName}</b>, and through it of everything stored there. Allow them only if you trust {entry.name}. You can take them back from the Service's menu.
          </div>
        )}
        <AlertDialogFooter className="gap-2">
          <AlertDialogCancel>Don't install</AlertDialogCancel>
          {off.length > 0 ? (
            <Button variant={allowed ? "destructive" : "default"} onClick={() => answer({ without: off })}>
              {allowed ? "Allow the rest and install" : "Install"} without {off.map(featureName).join(" and ").toLowerCase()}
            </Button>
          ) : (
            <AlertDialogAction variant="destructive" onClick={() => answer(true)}>
              Allow and install
            </AlertDialogAction>
          )}
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
