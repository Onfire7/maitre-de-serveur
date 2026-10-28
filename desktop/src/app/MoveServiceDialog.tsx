// Move a Service to another Node (decision 191): pick the Node, see where
// each folder goes, and, when it is running, agree to turn it off first or
// cancel. Progress shows step by step; closing the dialog lets it finish.
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { ArrowRight, Check, CircleAlert, Loader2, Minus, TriangleAlert, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { cn } from "@/lib/utils";
import { ownSpacesOn } from "@/files/sources";
import { declined } from "@/fleet/grants";
import { moveService, planMove, stepsFor, type Step, type StepId } from "@/fleet/moveService";
import { nodeDown, nodeWord, type FleetService, type FleetSnapshot } from "@/fleet/useFleet";
import type { ConsoleState } from "@/state";

export function MoveServiceDialog({ service, snap, state, onClose, onDone }: { service: FleetService | null; snap: FleetSnapshot | undefined; state: ConsoleState; onClose: () => void; onDone: () => void }) {
  const others = useMemo(() => (snap?.nodes ?? []).filter((n) => n.nodeId !== service?.spec.nodeId).sort((a, b) => a.name.localeCompare(b.name)), [snap?.nodes, service?.spec.nodeId]);
  const down = (id: string) => nodeDown(nodeWord(snap?.status.get(id), Date.now()));
  const [toNodeId, setToNodeId] = useState("");
  const [choice, setChoice] = useState<Record<string, string>>({});
  const [steps, setSteps] = useState<Step[] | null>(null);
  const [error, setError] = useState("");
  const running = service?.status?.state === 2 || service?.status?.state === 3;

  useEffect(() => {
    if (!service) return;
    setToNodeId(others.find((n) => !down(n.nodeId))?.nodeId ?? "");
    setChoice({});
    setSteps(null);
    setError("");
  }, [service?.spec.name]);

  if (!service) return null;
  const name = service.spec.name;
  const fromName = service.node?.name ?? "its Node";
  const toName = others.find((n) => n.nodeId === toNodeId)?.name ?? "";
  const plan = toNodeId ? planMove(snap, service, toNodeId, choice) : undefined;
  const reach = toNodeId ? ownSpacesOn(snap, toNodeId) : [];
  const spaceName = (id: string) => snap?.spaces.find((sp) => sp.spaceId === id)?.name ?? id;
  const busy = !!steps && !error && steps.some((s) => s.state === "todo" || s.state === "doing");
  const finished = !!steps && !busy;

  async function start() {
    if (!plan || !service) return;
    const list = stepsFor(service, plan, fromName, toName, running);
    setSteps(list);
    setError("");
    const set = (id: StepId, patch: Partial<Step>) => setSteps((cur) => cur && cur.map((s) => (s.id === id ? { ...s, ...patch } : s)));
    try {
      await moveService(snap, state.nodes, service, toNodeId, plan, set);
      toast.success(`${name} now runs on ${toName}`);
    } catch (e) {
      const msg = declined(e) ? `Root powers were not granted on ${toName}, so ${name} stays on ${fromName}.` : String((e as Error).message ?? e);
      setError(msg);
      toast.error(`${name} did not move: ${msg}`);
    }
    onDone();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Move {name} to another Node</DialogTitle>
          <DialogDescription>
            It runs on {fromName} now. Its settings, address and Access come along{service.grants ? "; its root powers are asked for again on the new Node" : ""}.
          </DialogDescription>
        </DialogHeader>

        {steps ? (
          <ol className="space-y-2 text-sm">
            {steps.map((s) => (
              <li key={s.id} className={cn("flex items-start gap-2", s.state === "skipped" && "text-muted-foreground")}>
                <span className="mt-0.5 inline-flex size-4 shrink-0 items-center justify-center">
                  {s.state === "doing" ? <Loader2 className="size-4 animate-spin" /> : s.state === "done" ? <Check className="size-4 text-status-online" /> : s.state === "failed" ? <X className="size-4 text-destructive" /> : s.state === "skipped" ? <Minus className="size-4" /> : <span className="size-1.5 rounded-full bg-muted-foreground/50" />}
                </span>
                <span className="min-w-0 flex-1">
                  {s.label}
                  {s.state === "doing" && s.progress !== undefined && (
                    <span className="mt-1 block h-1.5 overflow-hidden rounded bg-muted">
                      <span className="block h-full bg-primary transition-[width]" style={{ width: `${Math.round(s.progress * 100)}%` }} />
                    </span>
                  )}
                  {s.detail && s.state !== "failed" && <span className="block text-xs text-muted-foreground">{s.detail}</span>}
                </span>
              </li>
            ))}
            {error && <li className="rounded-lg border border-destructive/60 bg-destructive/5 p-3 text-sm">{error}</li>}
            {busy && <li className="text-xs text-muted-foreground">You can close this; the move keeps going and says when it is done.</li>}
          </ol>
        ) : others.length === 0 ? (
          <p className="text-sm text-muted-foreground">This Fleet has no other Node. Add one on the Nodes tab first.</p>
        ) : (
          <div className="space-y-4 text-sm">
            <label className="flex items-center gap-3">
              <span className="w-20 shrink-0 text-muted-foreground">Move to</span>
              <Select value={toNodeId} onValueChange={(v) => (setToNodeId(v), setChoice({}))}>
                <SelectTrigger className="w-56" aria-label="Node to move to">
                  <SelectValue placeholder="Pick a Node" />
                </SelectTrigger>
                <SelectContent>
                  {others.map((n) => (
                    <SelectItem key={n.nodeId} value={n.nodeId} disabled={down(n.nodeId)}>
                      {n.name}
                      {down(n.nodeId) && " (unreachable)"}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </label>
            {plan && plan.volumes.length > 0 && (
              <div>
                <div className="mb-1.5 text-muted-foreground">Its data</div>
                <ul className="divide-y rounded-lg border">
                  {plan.volumes.map((v) => (
                    <li key={v.volume} className="flex items-center gap-2 px-3 py-2">
                      <span className="w-20 shrink-0 truncate font-medium">{v.volume}</span>
                      {v.picked ? (
                        <span className="text-muted-foreground">its folder {service.spec.volumeFolders[v.volume] || ""} in {spaceName(v.fromSpace)}, unchanged</span>
                      ) : (
                        <>
                          <span className="shrink-0 text-muted-foreground">{spaceName(v.fromSpace)}</span>
                          <ArrowRight className="size-3.5 shrink-0 text-muted-foreground" />
                          <Select value={v.toSpace} onValueChange={(sp) => setChoice((c) => ({ ...c, [v.volume]: sp }))}>
                            <SelectTrigger size="sm" className="w-32" aria-label={`Space for ${v.volume}`}>
                              <SelectValue placeholder="Pick a Space" />
                            </SelectTrigger>
                            <SelectContent>
                              {reach.map((sp) => (
                                <SelectItem key={sp.spaceId} value={sp.spaceId}>
                                  {sp.name}
                                </SelectItem>
                              ))}
                            </SelectContent>
                          </Select>
                          <span className="ml-auto text-xs text-muted-foreground">copied to {toName}</span>
                        </>
                      )}
                    </li>
                  ))}
                </ul>
              </div>
            )}
            {plan?.blockers.map((b) => (
              <p key={b} className="flex gap-2 rounded-lg border border-destructive/60 bg-destructive/5 p-3">
                <CircleAlert className="mt-0.5 size-4 shrink-0 text-destructive" />
                {b}
              </p>
            ))}
            {plan?.warnings.map((w) => (
              <p key={w} className="flex gap-2 rounded-lg border border-status-attention/60 bg-status-attention/10 p-3">
                <TriangleAlert className="mt-0.5 size-4 shrink-0 text-status-attention" />
                {w}
              </p>
            ))}
            {running && plan && !plan.blockers.length && (
              <p className="rounded-lg border p-3">
                <b>{name} is running.</b> It has to be turned off to move, and is started again on {toName} once there.
              </p>
            )}
          </div>
        )}

        <DialogFooter>
          {finished ? (
            <Button onClick={onClose}>Close</Button>
          ) : busy ? (
            <Button variant="outline" onClick={onClose}>
              Keep going in the background
            </Button>
          ) : (
            <>
              <Button variant="outline" onClick={onClose}>
                Cancel
              </Button>
              <Button disabled={!plan || plan.blockers.length > 0 || !toNodeId} onClick={start}>
                {running ? "Turn off and move" : "Move"}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
