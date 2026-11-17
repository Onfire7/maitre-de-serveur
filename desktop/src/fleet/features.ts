// What each Node's agent offers (NodeInfo.features, plan C5), so the
// Console offers only what will work there: read once per Node when a
// view needs it, and again after five minutes (a Node may have been
// updated). Kept outside React Query so any component can ask without a
// provider.
import { useEffect, useSyncExternalStore } from "react";
import { createClient } from "@connectrpc/connect";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { transportFor, type Target } from "@/transport";

const FRESH_MS = 5 * 60_000;
const known = new Map<string, { features: readonly string[]; at: number }>();
const asking = new Set<string>();
const listeners = new Set<() => void>();
let version = 0;
const none: readonly string[] = [];

type Ask = (t: Target) => Promise<readonly string[]>;
let ask: Ask = async (t) => (await createClient(NodeService, transportFor(t)).getNodeInfo({}, { timeoutMs: 4000 })).node?.features ?? [];

function emit() {
  version++;
  listeners.forEach((l) => l());
}

function refresh(t: Target) {
  const cur = known.get(t.fingerprint);
  if (asking.has(t.fingerprint) || (cur && Date.now() - cur.at < FRESH_MS)) return;
  asking.add(t.fingerprint);
  ask(t).then(
    (features) => known.set(t.fingerprint, { features, at: Date.now() }),
    () => {},
  ).finally(() => {
    asking.delete(t.fingerprint);
    emit();
  });
}

/** The Node's features; none until it has said. */
export function useNodeFeatures(t: Target | undefined): readonly string[] {
  useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => version,
  );
  useEffect(() => {
    if (t) refresh(t);
  }, [t?.fingerprint, t?.addr]);
  return (t && known.get(t.fingerprint)?.features) || none;
}

/** Tests: what to answer, and forget what was heard. */
export function setFeaturesForTest(f: Ask | undefined, at?: { nodeId: string; features: string[] }) {
  known.clear();
  if (f) ask = f;
  if (at) known.set(at.nodeId, { features: at.features, at: Date.now() });
  emit();
}
