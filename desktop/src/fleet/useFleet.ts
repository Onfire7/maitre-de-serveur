// The Console's one Fleet-wide query (decisions 159 and 161): records plus
// GetFleetStatus through the anchor Node every 3 s, with failover. Page-
// scoped calls to a particular Node (logs, files, Drives...) use
// `transportFor` directly and are not routed through here.
import { useCallback, useMemo, useRef } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError, createClient, type Transport } from "@connectrpc/connect";
import { create, fromBinary } from "@bufbuild/protobuf";
import { NodeService, FleetNodeStatusSchema, type FleetNodeStatus } from "@/gen/maitre/v1/node_pb";
import { NodeSchema, FleetSchema, TrustedDeviceSchema, UpdatePolicySchema, type Record as Rec, type Node, type TrustedDevice, type UpdatePolicy } from "@/gen/maitre/v1/fleet_pb";
import { ServiceService, ServiceSpecSchema, type ServiceSpec, type ServiceStatus } from "@/gen/maitre/v1/service_pb";
import { SpaceSchema, DriveRecordSchema, type Space, type DriveRecord } from "@/gen/maitre/v1/storage_pb";
import { transportFor, type Target } from "@/transport";
import { type KnownNode } from "@/state";
import { pickAnchor, type AnchorPick } from "./anchor";

export const FLEET_POLL_MS = 3000;
/** Metrics older than this are shown dimmed (decision 158). */
export const STALE_MS = 30_000;
/** A Node the anchor has not heard from for this long is Unreachable (decision 162). */
export const UNREACHABLE_MS = 60_000;

export interface FleetSnapshot {
  anchor: AnchorPick;
  /** When the anchor answered, Console clock. */
  at: number;
  fleet?: { fleetId: string; name: string };
  nodes: Node[];
  devices: TrustedDevice[];
  spaces: Space[];
  drives: DriveRecord[];
  specs: ServiceSpec[];
  policy?: UpdatePolicy;
  status: Map<string, FleetNodeStatus>;
  records: Rec[];
  /**
   * The anchor runs a Maître Server from before GetFleetStatus (ADR 0006),
   * so the status was gathered by asking each Node directly, the old way.
   * Happens during a staged rollout or until the Nodes are updated.
   */
  legacy?: boolean;
}

function decode<T>(records: Rec[], kind: string, schema: Parameters<typeof fromBinary>[0]): T[] {
  return records.filter((r) => r.kind === kind && !r.deleted).map((r) => fromBinary(schema, r.body) as T);
}

type TransportFor = (t: Target) => Transport;

/**
 * The Fleet status the old way, for Nodes from before GetFleetStatus: the
 * anchor's view of its peers from GetNodeInfo, then each reachable Node's
 * own metrics and Services, asked directly and in parallel.
 */
async function legacyStatus(anchor: AnchorPick, nodes: Node[], known: KnownNode[], tf: TransportFor): Promise<FleetNodeStatus[]> {
  const opts = { timeoutMs: 4000 };
  const now = BigInt(Date.now());
  const info = await createClient(NodeService, tf({ addr: anchor.addr, fingerprint: anchor.nodeId })).getNodeInfo({}, opts);
  const peers = new Map((info.node?.peers ?? []).map((p) => [p.nodeId, p]));
  const ids = Array.from(new Set([anchor.nodeId, ...nodes.map((n) => n.nodeId), ...peers.keys()]));
  return Promise.all(
    ids.map(async (id) => {
      const addr = id === anchor.nodeId ? anchor.addr : (peers.get(id)?.address || nodes.find((n) => n.nodeId === id)?.addresses[0] || known.find((k) => k.node_id === id)?.addresses[0]);
      const base = create(FleetNodeStatusSchema, { nodeId: id, reachable: false, lastSeenUnixMs: peers.get(id)?.lastSeenUnixMs ?? 0n });
      if (!addr) return base;
      const t = { addr, fingerprint: id };
      try {
        const [m, sv] = await Promise.all([createClient(NodeService, tf(t)).getNodeMetrics({}, opts), createClient(ServiceService, tf(t)).listServices({}, opts)]);
        return create(FleetNodeStatusSchema, { nodeId: id, reachable: true, lastSeenUnixMs: now, metrics: m.metrics, metricsUnixMs: now, services: sv.services, servicesUnixMs: now });
      } catch {
        return base;
      }
    }),
  );
}

export async function readFleet(anchor: AnchorPick, known: KnownNode[] = [], tf: TransportFor = transportFor): Promise<Omit<FleetSnapshot, "anchor" | "at">> {
  const client = createClient(NodeService, tf({ addr: anchor.addr, fingerprint: anchor.nodeId }));
  const records = (await client.listRecords({})).records;
  const nodes = decode<Node>(records, "node", NodeSchema).sort((a, b) => a.letter.localeCompare(b.letter));
  let statuses: FleetNodeStatus[];
  let legacy = false;
  try {
    statuses = (await client.getFleetStatus({})).nodes;
  } catch (e) {
    if (!(e instanceof ConnectError) || e.code !== Code.Unimplemented) throw e;
    legacy = true;
    statuses = await legacyStatus(anchor, nodes, known, tf);
  }
  const fleet = decode<{ fleetId: string; name: string }>(records, "fleet", FleetSchema)[0];
  return {
    fleet,
    legacy,
    nodes,
    devices: decode<TrustedDevice>(records, "trusted_device", TrustedDeviceSchema),
    spaces: decode<Space>(records, "space", SpaceSchema),
    drives: decode<DriveRecord>(records, "drive", DriveRecordSchema),
    specs: decode<ServiceSpec>(records, "service", ServiceSpecSchema),
    policy: decode<UpdatePolicy>(records, "update_policy", UpdatePolicySchema)[0],
    status: new Map(statuses.map((n) => [n.nodeId, n])),
    records,
  };
}

/**
 * Poll the Fleet through the anchor. `known` is the Console's own Node list
 * (addresses to try before the first answer); after that the synced Node
 * records supply addresses, so a renamed or re-addressed Node is followed.
 */
export function useFleet(known: KnownNode[]) {
  const qc = useQueryClient();
  const last = useRef<AnchorPick | undefined>(undefined);
  const failed = useRef(new Set<string>());

  const candidates = useCallback(() => {
    const snap = qc.getQueryData<FleetSnapshot>(["fleet"]);
    const fromRecords = snap?.nodes.map((n) => ({ nodeId: n.nodeId, letter: n.letter, addresses: n.addresses })) ?? [];
    const fromKnown = known.map((n) => ({ nodeId: n.node_id, letter: "", addresses: n.addresses }));
    const seen = new Set(fromRecords.map((n) => n.nodeId));
    return [...fromRecords, ...fromKnown.filter((n) => !seen.has(n.nodeId))];
  }, [known, qc]);

  const query = useQuery<FleetSnapshot>({
    queryKey: ["fleet"],
    enabled: known.length > 0,
    refetchInterval: FLEET_POLL_MS,
    refetchIntervalInBackground: true,
    retry: false,
    staleTime: FLEET_POLL_MS,
    placeholderData: (prev) => prev,
    queryFn: async () => {
      const nodes = candidates();
      let tries = nodes.length;
      let lastErr: unknown;
      while (tries-- > 0) {
        const pick = pickAnchor(nodes, last.current, failed.current);
        if (!pick) break;
        try {
          const data = await readFleet(pick, known);
          last.current = pick;
          failed.current.clear();
          return { ...data, anchor: pick, at: Date.now() };
        } catch (e) {
          lastErr = e;
          failed.current.add(pick.nodeId);
          if (failed.current.size >= nodes.length) failed.current.clear();
        }
      }
      throw lastErr ?? new Error("no Node to ask");
    },
  });

  const refresh = useCallback(() => qc.invalidateQueries({ queryKey: ["fleet"] }), [qc]);
  return { ...query, refresh };
}

/** One Service as the sidebar sees it: Fleet record plus the running Node's last status. */
export interface FleetService {
  spec: ServiceSpec;
  status?: ServiceStatus;
  node?: Node;
  nodeStatus?: FleetNodeStatus;
  /** Decision 135 words. */
  word: "Online" | "Starting" | "Stopped" | "Attention";
  nodeUnreachable: boolean;
}

export type NodeWord = "Healthy" | "Unreachable" | "Provisioning" | "Updating" | "Attention";

export function nodeWord(n: FleetNodeStatus | undefined, now: number): NodeWord {
  // A Node still being installed is reported by the anchor from its
  // Installer until it first talks.
  if (n?.provisioning && !n.reachable) return "Provisioning";
  if (!n || !n.reachable || now - Number(n.lastSeenUnixMs) > UNREACHABLE_MS) return "Unreachable";
  // UpdateStatus.State: 2 updating, 3 failed, 4 rebooting.
  if (n.updateState === 2 || n.updateState === 4) return "Updating";
  // A peer that answered with errors (ADR 0007) needs attention; it is
  // still reachable, and still updatable.
  if (n.updateState === 3 || n.lastRollback || n.metrics?.poolFallback || n.problems.length > 0) return "Attention";
  return "Healthy";
}

/** The Node cannot be called: unreachable, or not installed yet. */
export function nodeDown(word: NodeWord) {
  return word === "Unreachable" || word === "Provisioning";
}

/** What a Node still being installed is doing, e.g. "40% · writing the disk". */
export function provisioningLine(n: FleetNodeStatus | undefined): string {
  const p = n?.provisioning;
  if (!p) return "";
  // Provisioning.Phase: 4 rebooting, 5 failed.
  if (p.phase === 5) return `install failed${p.message ? `: ${p.message}` : ""}`;
  if (p.phase === 4) return "restarting into Maître Server";
  return [`${p.percent}%`, p.message].filter(Boolean).join(" · ");
}

export function serviceWord(s: ServiceStatus | undefined): FleetService["word"] {
  if (!s) return "Stopped";
  switch (s.state) {
    case 3:
      return s.healthy || s.restarts < 3 ? (s.healthy ? "Online" : "Attention") : "Attention";
    case 2:
      return "Starting";
    case 4:
      return "Attention";
    default:
      return "Stopped";
  }
}

export function useFleetServices(snap: FleetSnapshot | undefined, now = Date.now()): FleetService[] {
  return useMemo(() => {
    if (!snap) return [];
    return snap.specs
      .map((spec) => {
        const node = snap.nodes.find((n) => n.nodeId === spec.nodeId);
        const nodeStatus = snap.status.get(spec.nodeId);
        const status = nodeStatus?.services.find((s) => s.spec?.name === spec.name);
        const unreachable = nodeDown(nodeWord(nodeStatus, now));
        return { spec, status, node, nodeStatus, word: unreachable ? "Stopped" : serviceWord(status), nodeUnreachable: unreachable } as FleetService;
      })
      .sort((a, b) => a.spec.name.localeCompare(b.spec.name));
  }, [snap, now]);
}
