// The Updates tab (Milestone 6 policy, decision 148): how Maître Server
// updates roll across the Fleet, and what each Node runs.
import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { create, toBinary } from "@bufbuild/protobuf";
import { toast } from "sonner";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { NodeService, UpdateStatus_State } from "@/gen/maitre/v1/node_pb";
import { UpdatePolicySchema, type UpdatePolicy } from "@/gen/maitre/v1/fleet_pb";
import { transportFor } from "@/transport";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { targetFor } from "@/fleet/actions";
import type { ConsoleState } from "@/state";
import { useRoute } from "./routes";

const HOURS = Array.from({ length: 24 }, (_, h) => h);
const STAGES = [6, 12, 24, 48, 72, 168];

export function UpdatesTab({ snap, state }: { snap: FleetSnapshot | undefined; state: ConsoleState }) {
  const qc = useQueryClient();
  const { go } = useRoute();
  const stored = snap?.policy ?? create(UpdatePolicySchema, { automatic: false, windowStartHour: 3, windowEndHour: 5, stageHours: 24 });
  const [policy, setPolicy] = useState<UpdatePolicy>(stored);
  useEffect(() => setPolicy(stored), [snap?.policy]);
  async function save(p: UpdatePolicy) {
    setPolicy(p);
    if (!snap) return;
    try {
      await createClient(NodeService, transportFor({ addr: snap.anchor.addr, fingerprint: snap.anchor.nodeId })).putRecord({ record: { kind: "update_policy", id: "fleet", body: toBinary(UpdatePolicySchema, p) } });
      qc.invalidateQueries({ queryKey: ["fleet"] });
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    }
  }
  const hour = (h: number) => `${String(h).padStart(2, "0")}:00`;
  return (
    <div className="max-w-2xl space-y-8">
      <section className="space-y-4">
        <h2 className="text-base font-semibold">Maître Server</h2>
        <label className="flex items-center justify-between gap-4 rounded-xl border bg-card p-4">
          <span>
            <span className="block text-sm font-medium">Update the Nodes automatically</span>
            <span className="block text-xs text-muted-foreground">One Node goes first; the others follow once it has run the new version without trouble.</span>
          </span>
          <Switch checked={policy.automatic} onCheckedChange={(v) => save(create(UpdatePolicySchema, { ...policy, automatic: v }))} />
        </label>
        {policy.automatic && (
          <div className="space-y-4 rounded-xl border bg-card p-4">
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <Label>Between</Label>
              <HourSelect value={policy.windowStartHour} onChange={(h) => save(create(UpdatePolicySchema, { ...policy, windowStartHour: h }))} />
              <Label>and</Label>
              <HourSelect value={policy.windowEndHour} onChange={(h) => save(create(UpdatePolicySchema, { ...policy, windowEndHour: h }))} />
              <span className="text-xs text-muted-foreground">local time on the Nodes</span>
            </div>
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <Label>The others follow after</Label>
              <Select value={String(policy.stageHours)} onValueChange={(v) => save(create(UpdatePolicySchema, { ...policy, stageHours: Number(v) }))}>
                <SelectTrigger size="sm" className="w-28">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {STAGES.map((h) => (
                    <SelectItem key={h} value={String(h)}>
                      {h < 24 ? `${h} hours` : h === 24 ? "1 day" : `${h / 24} days`}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <span className="text-xs text-muted-foreground">without a rollback on the first Node</span>
            </div>
            <label className="flex items-center justify-between gap-4 text-sm">
              <span>
                Paused
                <span className="block text-xs text-muted-foreground">Hold every update until you switch this off.</span>
              </span>
              <Switch checked={policy.paused} onCheckedChange={(v) => save(create(UpdatePolicySchema, { ...policy, paused: v }))} />
            </label>
          </div>
        )}
        <p className="text-xs text-muted-foreground">
          {policy.automatic ? `Updates run between ${hour(policy.windowStartHour)} and ${hour(policy.windowEndHour)}${policy.paused ? ", paused for now" : ""}.` : "Nodes update only when you ask, from their details."}
        </p>
      </section>

      <section className="space-y-3">
        <h2 className="text-base font-semibold">What each Node runs</h2>
        <ul className="divide-y rounded-xl border bg-card">
          {(snap?.nodes ?? []).map((n) => (
            <NodeImageRow key={n.nodeId} nodeId={n.nodeId} name={n.name} snap={snap!} state={state} onOpen={() => go({ name: "fleet", tab: "nodes", nodeId: n.nodeId })} />
          ))}
        </ul>
      </section>
    </div>
  );
}

function HourSelect({ value, onChange }: { value: number; onChange: (h: number) => void }) {
  return (
    <Select value={String(value)} onValueChange={(v) => onChange(Number(v))}>
      <SelectTrigger size="sm" className="w-24">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {HOURS.map((h) => (
          <SelectItem key={h} value={String(h)}>
            {String(h).padStart(2, "0")}:00
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

function NodeImageRow({ nodeId, name, snap, state, onOpen }: { nodeId: string; name: string; snap: FleetSnapshot; state: ConsoleState; onOpen: () => void }) {
  const t = targetFor(snap, state.nodes, nodeId);
  const q = useQuery({
    queryKey: ["update-status", nodeId, t?.addr],
    enabled: !!t,
    refetchInterval: 10_000,
    retry: false,
    queryFn: async () => (await createClient(NodeService, transportFor(t!)).getUpdateStatus({})).status,
  });
  const s = q.data;
  return (
    <li className="flex items-center gap-3 px-4 py-2.5 text-sm">
      <span className="w-28 shrink-0 font-medium">{name}</span>
      <span className="min-w-0 flex-1 truncate text-muted-foreground">
        {q.error ? "unreachable" : !s ? "…" : !s.supported ? "not an image-based Node (dev mode)" : <>{s.booted?.version || s.booted?.digest.slice(7, 19) || "unknown version"}{s.booted?.timestamp && ` · built ${new Date(s.booted.timestamp).toLocaleDateString()}`}</>}
      </span>
      {s?.state === UpdateStatus_State.UPDATING && <Badge className="bg-status-starting text-white">updating {Math.round(s.progress * 100)}%</Badge>}
      {s?.staged && s.state !== UpdateStatus_State.UPDATING && <Badge variant="outline">update staged, restart to apply</Badge>}
      {s?.lastRollback && <Badge className="bg-status-attention text-black">rolled back</Badge>}
      {s?.state === UpdateStatus_State.FAILED && <Badge variant="destructive">failed</Badge>}
      <Button size="sm" variant="ghost" onClick={onOpen}>
        Details
      </Button>
    </li>
  );
}
