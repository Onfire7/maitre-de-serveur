// The Updates tab (Milestone 6 policy, decision 148): how Maître Server
// updates roll across the Fleet, and what each Node runs, with Update,
// Roll back and Restart for each (decision 197).
import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { create, toBinary } from "@bufbuild/protobuf";
import { toast } from "sonner";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { HourPicker, hourLabel } from "@/components/hour-picker";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { UpdatePolicySchema, type UpdatePolicy } from "@/gen/maitre/v1/fleet_pb";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { nodeDown, nodeWord } from "@/fleet/useFleet";
import { targetFor } from "@/fleet/actions";
import { currentRecord, putSigned } from "@/fleet/sign";
import type { ConsoleState } from "@/state";
import { useRoute } from "./routes";
import { ServerImage } from "./NodeDetails";
import { NodeMark } from "./status";

const STAGES = [6, 12, 24, 48, 72, 168];
const stageLabel = (h: number) => (h < 24 ? `${h} hours` : h === 24 ? "1 day" : h % 24 === 0 ? `${h / 24} days` : `${h} hours`);

export function UpdatesTab({ snap, state }: { snap: FleetSnapshot | undefined; state: ConsoleState }) {
  const qc = useQueryClient();
  const { go } = useRoute();
  const stored = snap?.policy ?? create(UpdatePolicySchema, { automatic: false, windowStartHour: 3, windowEndHour: 5, stageHours: 24 });
  const [policy, setPolicy] = useState<UpdatePolicy>(stored);
  useEffect(() => setPolicy(stored), [snap?.policy]);
  // A policy saved without a stage (0) means a day, as on the Node; shown
  // blank before, since no choice matched it.
  const stage = policy.stageHours > 0 ? policy.stageHours : 24;
  const inWindow = (h: number) => (policy.windowStartHour <= policy.windowEndHour ? h >= policy.windowStartHour && h < policy.windowEndHour : h >= policy.windowStartHour || h < policy.windowEndHour);
  async function save(p: UpdatePolicy) {
    setPolicy(p);
    if (!snap) return;
    try {
      await putSigned({ addr: snap.anchor.addr, fingerprint: snap.anchor.nodeId }, { kind: "update_policy", id: "fleet", body: toBinary(UpdatePolicySchema, p) }, currentRecord(snap.records, "update_policy", "fleet"));
      qc.invalidateQueries({ queryKey: ["fleet"] });
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    }
  }
  const hour = hourLabel;
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
              <HourPicker label="Updates may start from" value={policy.windowStartHour} highlight={inWindow} onChange={(h) => save(create(UpdatePolicySchema, { ...policy, windowStartHour: h }))} />
              <Label>and</Label>
              <HourPicker label="Updates may start until" value={policy.windowEndHour} highlight={inWindow} onChange={(h) => save(create(UpdatePolicySchema, { ...policy, windowEndHour: h }))} />
              <span className="text-xs text-muted-foreground">local time on the Nodes</span>
            </div>
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <Label>The others follow after</Label>
              <Select value={String(stage)} onValueChange={(v) => save(create(UpdatePolicySchema, { ...policy, stageHours: Number(v) }))}>
                <SelectTrigger size="sm" className="w-28 text-foreground" aria-label="The others follow after">
                  <SelectValue>{stageLabel(stage)}</SelectValue>
                </SelectTrigger>
                <SelectContent>
                  {[...new Set([...STAGES, stage])].sort((a, b) => a - b).map((h) => (
                    <SelectItem key={h} value={String(h)}>
                      {stageLabel(h)}
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
        <p className="text-xs text-muted-foreground">Updates, roll backs and restarts run one Node at a time. Ask for several and they queue; each starts once no Node is updating.</p>
        {(snap?.nodes ?? []).length === 0 && <p className="text-sm text-muted-foreground">No Node answers yet.</p>}
        {[...(snap?.nodes ?? [])]
          .sort((a, b) => a.name.localeCompare(b.name))
          .map((n) => {
            const word = nodeWord(snap!.status.get(n.nodeId), Date.now());
            return (
              <div key={n.nodeId} className="rounded-xl border bg-card p-4 [&_section]:mb-0!">
                <ServerImage
                  node={n}
                  target={targetFor(snap, state.nodes, n.nodeId)}
                  snap={snap!}
                  title={
                    <span className="flex items-center gap-2">
                      <NodeMark word={word} />
                      <button className="hover:underline" onClick={() => go({ name: "fleet", tab: "nodes", nodeId: n.nodeId })} title={`${n.name} on the Nodes tab`}>
                        {n.name}
                      </button>
                      {nodeDown(word) && <span className="text-xs font-normal text-muted-foreground">unreachable</span>}
                    </span>
                  }
                />
              </div>
            );
          })}
      </section>
    </div>
  );
}
