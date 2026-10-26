// The Console's one Fleet-wide query (decisions 159 and 161): records plus
// GetFleetStatus through the anchor Node every 3 s, with failover. Page-
// scoped calls to a particular Node (logs, files, Drives...) use
// `transportFor` directly and are not routed through here.
import { useCallback, useMemo, useRef } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { fromBinary } from "@bufbuild/protobuf";
import { NodeService, type FleetNodeStatus } from "@/gen/maitre/v1/node_pb";
import { NodeSchema, FleetSchema, TrustedDeviceSchema, UpdatePolicySchema, type Record as Rec, type Node, type TrustedDevice, type UpdatePolicy } from "@/gen/maitre/v1/fleet_pb";
import { ServiceSpecSchema, type ServiceSpec, type ServiceStatus } from "@/gen/maitre/v1/service_pb";
import { SpaceSchema, DriveRecordSchema, type Space, type DriveRecord } from "@/gen/maitre/v1/storage_pb";
import { transportFor } from "@/transport";
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
}

function decode<T>(records: Rec[], kind: string, schema: Parameters<typeof fromBinary>[0]): T[] {
  return records.filter((r) => r.kind === kind && !r.deleted).map((r) => fromBinary(schema, r.body) as T);
}

export async function readFleet(anchor: AnchorPick): Promise<Omit<FleetSnapshot, "anchor" | "at">> {
  const client = createClient(NodeService, transportFor({ addr: anchor.addr, fingerprint: anchor.nodeId }));
  const [recs, st] = await Promise.all([client.listRecords({}), client.getFleetStatus({})]);
  const records = recs.records;
  const fleet = decode<{ fleetId: string; name: string }>(records, "fleet", FleetSchema)[0];
  return {
    fleet,
    nodes: decode<Node>(records, "node", NodeSchema).sort((a, b) => a.letter.localeCompare(b.letter)),
    devices: decode<TrustedDevice>(records, "trusted_device", TrustedDeviceSchema),
    spaces: decode<Space>(records, "space", SpaceSchema),
    drives: decode<DriveRecord>(records, "drive", DriveRecordSchema),
    specs: decode<ServiceSpec>(records, "service", ServiceSpecSchema),
    policy: decode<UpdatePolicy>(records, "update_policy", UpdatePolicySchema)[0],
    status: new Map(st.nodes.map((n) => [n.nodeId, n])),
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
          const data = await readFleet(pick);
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

export function nodeWord(n: FleetNodeStatus | undefined, now: number): "Healthy" | "Unreachable" | "Attention" {
  if (!n || !n.reachable || now - Number(n.lastSeenUnixMs) > UNREACHABLE_MS) return "Unreachable";
  if (n.metrics?.poolFallback) return "Attention";
  return "Healthy";
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
        const unreachable = nodeWord(nodeStatus, now) === "Unreachable";
        return { spec, status, node, nodeStatus, word: unreachable ? "Stopped" : serviceWord(status), nodeUnreachable: unreachable } as FleetService;
      })
      .sort((a, b) => a.spec.name.localeCompare(b.spec.name));
  }, [snap, now]);
}
