// Standing conditions (decision 150): a badge on the Fleet entry and a list
// on the Fleet Overview. Transient events are toasts, not notices.
import type { FleetSnapshot } from "@/fleet/useFleet";
import { nodeWord, provisioningLine } from "@/fleet/useFleet";
import type { Found } from "@/state";
import type { FleetTab } from "./routes";

export interface Notice {
  id: string;
  title: string;
  detail: string;
  tab: FleetTab;
  nodeId?: string;
  severity: "info" | "attention";
}

export function notices(snap: FleetSnapshot | undefined, found: Found[], scope: { nodeName: string; until: number } | null, now = Date.now()): Notice[] {
  const out: Notice[] = [];
  if (!snap) return out;
  if (snap.legacy)
    out.push({ id: "legacy", title: "The Nodes run an older Maître Server", detail: "This app asks each Node directly until they are updated, which is slower and misses Nodes it cannot reach. Update them from the Nodes tab.", tab: "nodes", severity: "info" });
  if (!snap.spaces.some((s) => s.defaultForNew))
    out.push({ id: "no-default-space", title: "No default Storage Space", detail: snap.spaces.length ? "Mark one Space as the default for new Services." : "Create a Storage Space before installing a Service.", tab: "storage", severity: "attention" });
  for (const f of found.filter((f) => f.kind === "device")) out.push({ id: `device:${f.id}`, title: `${f.name || "A device"} is asking to join`, detail: "Approve it only if the code on its screen matches.", tab: "devices", severity: "info" });
  for (const n of snap.nodes) {
    const st = snap.status.get(n.nodeId);
    if (st?.provisioning?.phase === 5) out.push({ id: `install-failed:${n.nodeId}`, title: `${n.name}'s install failed`, detail: provisioningLine(st), tab: "nodes", nodeId: n.nodeId, severity: "attention" });
    else if (nodeWord(st, now) === "Unreachable") out.push({ id: `unreachable:${n.nodeId}`, title: `${n.name} is unreachable`, detail: "Its Services are unavailable until it is back.", tab: "nodes", nodeId: n.nodeId, severity: "attention" });
    else if (st?.updateState === 3) out.push({ id: `update-failed:${n.nodeId}`, title: `${n.name}'s update failed`, detail: st.updateMessage || "See the Node's details.", tab: "nodes", nodeId: n.nodeId, severity: "attention" });
    else if (st?.lastRollback) out.push({ id: `rolled-back:${n.nodeId}`, title: `${n.name} rolled back its last update`, detail: st.lastRollback, tab: "nodes", nodeId: n.nodeId, severity: "attention" });
    else if (st?.updateStaged) out.push({ id: `staged:${n.nodeId}`, title: `${n.name} has an update staged`, detail: "Restart it to run the new version.", tab: "nodes", nodeId: n.nodeId, severity: "info" });
    else if (st?.metrics?.poolFallback) out.push({ id: `fallback:${n.nodeId}`, title: `${n.name} has no Pool for the default Space`, detail: "Service data is landing on its system disk.", tab: "storage", nodeId: n.nodeId, severity: "attention" });
  }
  if (scope) out.push({ id: "scope", title: `Trusted by ${scope.nodeName} only`, detail: `Enter a recovery code from another Node before ${new Date(scope.until).toLocaleString()} to be trusted everywhere.`, tab: "devices", severity: "attention" });
  return out;
}
