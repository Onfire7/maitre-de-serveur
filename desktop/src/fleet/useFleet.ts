// The Console's one Fleet-wide query (decisions 159 and 161): records plus
// GetFleetStatus through the anchor Node every 3 s, with failover. Page-
// scoped calls to a particular Node (logs, files, Drives...) use
// `transportFor` directly and are not routed through here.
import { useCallback, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError, createClient, type Transport } from "@connectrpc/connect";
import { create, fromBinary } from "@bufbuild/protobuf";
import { NodeService, FleetNodeStatusSchema, type FleetNodeStatus, type SyncGap } from "@/gen/maitre/v1/node_pb";
import { NodeSchema, FleetSchema, GrantSchema, TrustedDeviceSchema, UpdatePolicySchema, type Record as Rec, type Node, type TrustedDevice, type UpdatePolicy } from "@/gen/maitre/v1/fleet_pb";
import { ServiceService, ServiceSpecSchema, type ServiceSpec, type ServiceStatus } from "@/gen/maitre/v1/service_pb";
import { SpaceSchema, DriveRecordSchema, type Space, type DriveRecord } from "@/gen/maitre/v1/storage_pb";
import { transportFor, type Target } from "@/transport";
import { type KnownNode } from "@/state";
import { allPicks, attemptKey, pickAnchor, type AnchorPick } from "./anchor";
import { chooseAddress, demoteRoute, lastAnchor, pinned, rememberRoute, routeKind, routeOrder, setLastAnchor, setPinned } from "./route";
import { domainAddr, rememberDomain } from "./domain";

export const FLEET_POLL_MS = 3000;
/** Metrics older than this are shown dimmed (decision 158). */
export const STALE_MS = 30_000;
/** A Node the anchor has not heard from for this long is Unreachable (decision 162). */
export const UNREACHABLE_MS = 60_000;
/**
 * A Node that says it is updating is shown as Updating, reachable or not,
 * for this long (decision 214); after that, as whatever it is.
 */
export const UPDATE_LIMIT_MS = 60 * 60_000;

/**
 * A Node's gossiped status, plus when it started updating if its own
 * record says it is (decision 214): it writes that before the update and
 * clears it when it is back, and no one else may change it.
 */
export type NodeStatus = FleetNodeStatus & { updatingSince?: number };

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
  status: Map<string, NodeStatus>;
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
      const addr = id === anchor.nodeId ? anchor.addr : chooseAddress(id, nodes.find((n) => n.nodeId === id)?.addresses ?? known.find((k) => k.node_id === id)?.addresses ?? [], routeKind(anchor.addr, domainAddr())) || peers.get(id)?.address;
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

/** The status map, with each updating Node's start from its record (decision 214). */
function withUpdating(statuses: FleetNodeStatus[], nodes: Node[]): Map<string, NodeStatus> {
  const out = new Map<string, NodeStatus>(statuses.map((n) => [n.nodeId, n]));
  for (const n of nodes) {
    const since = Number(n.updating?.startedUnixMs ?? 0n);
    if (!since) continue;
    const st = out.get(n.nodeId) ?? create(FleetNodeStatusSchema, { nodeId: n.nodeId });
    out.set(n.nodeId, { ...st, updatingSince: since });
  }
  return out;
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
    status: withUpdating(statuses, nodes),
    records,
  };
}

/** One try at reading the Fleet this round (decision 284), for the connection chooser. */
export interface Attempt {
  nodeId: string;
  addr: string;
  /** "" when it answered. */
  error: string;
  at: number;
}

/** The error text as the chooser shows it: the Node's words, without the Connect code. */
function errorText(e: unknown): string {
  const msg = e instanceof ConnectError ? e.rawMessage : String((e as Error)?.message ?? e);
  return msg || "did not answer";
}

/**
 * Poll the Fleet through the anchor. `known` is the Console's own Node list
 * (addresses to try before the first answer); after that the synced Node
 * records supply addresses, so a renamed or re-addressed Node is followed.
 * Every address of every Node is tried in turn (decision 284): the user's
 * pin first, then the way that answered last, then by letter.
 */
export function useFleet(known: KnownNode[]) {
  const qc = useQueryClient();
  const last = useRef<AnchorPick | undefined>(undefined);
  const failed = useRef(new Set<string>());
  const [pin, setPinState] = useState<Target | undefined>(() => pinned());
  const [attempts, setAttempts] = useState<Attempt[]>([]);
  if (last.current === undefined) {
    const l = lastAnchor();
    if (l) last.current = { nodeId: l.fingerprint, addr: l.addr };
  }

  const candidates = useCallback(() => {
    const snap = qc.getQueryData<FleetSnapshot>(["fleet"]);
    // The Fleet's domain (decision 218) is every Node's last address: it
    // reaches whichever Node holds the app's forward, so a read through it
    // keeps going there while it answers.
    const dom = domainAddr();
    // The ways that did not answer from this device go last (routeOrder).
    const withDomain = (id: string, a: string[]) => routeOrder(id, dom && !a.includes(dom) ? [...a, dom] : a, undefined, dom);
    const fromRecords = snap?.nodes.map((n) => ({ nodeId: n.nodeId, letter: n.letter, addresses: withDomain(n.nodeId, n.addresses) })) ?? [];
    const fromKnown = known.map((n) => ({ nodeId: n.node_id, letter: "", addresses: withDomain(n.node_id, n.addresses) }));
    const seen = new Set(fromRecords.map((n) => n.nodeId));
    const out = [...fromRecords, ...fromKnown.filter((n) => !seen.has(n.nodeId))];
    // A pinned or remembered way the records do not list (typed by the
    // user, or a Node whose record lost it) stays a way to that Node.
    for (const extra of [pin && { nodeId: pin.fingerprint, addr: pin.addr }, last.current]) {
      if (!extra) continue;
      const n = out.find((c) => c.nodeId === extra.nodeId);
      if (n && !n.addresses.includes(extra.addr)) n.addresses = [...n.addresses, extra.addr];
      else if (!n) out.push({ nodeId: extra.nodeId, letter: "", addresses: [extra.addr] });
    }
    return out;
  }, [known, qc, pin]);

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
      const total = allPicks(nodes).length;
      let tries = total;
      let lastErr: unknown;
      const round: Attempt[] = [];
      const tried = new Set<string>();
      // The pin comes first every round; a pin that does not answer is let
      // go, so the app finds another way and says so.
      const picks = (): AnchorPick | undefined => {
        if (pin && !tried.has(attemptKey({ nodeId: pin.fingerprint, addr: pin.addr }))) return { nodeId: pin.fingerprint, addr: pin.addr };
        return pickAnchor(nodes, last.current, failed.current);
      };
      while (tries-- > 0) {
        const pick = picks();
        if (!pick || tried.has(attemptKey(pick))) break;
        tried.add(attemptKey(pick));
        try {
          const data = await readFleet(pick, known, (t) => transportFor(t, { fallback: false }));
          round.push({ nodeId: pick.nodeId, addr: pick.addr, error: "", at: Date.now() });
          setAttempts(round);
          last.current = pick;
          failed.current.clear();
          setLastAnchor({ fingerprint: pick.nodeId, addr: pick.addr });
          rememberRoute(pick.nodeId, pick.addr);
          const snap = { ...data, anchor: pick, at: Date.now() };
          rememberDomain(snap);
          return snap;
        } catch (e) {
          lastErr = e;
          round.push({ nodeId: pick.nodeId, addr: pick.addr, error: errorText(e), at: Date.now() });
          failed.current.add(attemptKey(pick));
          demoteRoute(pick.nodeId, pick.addr);
          if (pin && pick.nodeId === pin.fingerprint && pick.addr === pin.addr) {
            setPinned(undefined);
            setPinState(undefined);
          }
          if (failed.current.size >= total) failed.current.clear();
        }
      }
      setAttempts(round);
      throw lastErr ?? new Error("no Node to ask");
    },
  });

  const refresh = useCallback(() => qc.invalidateQueries({ queryKey: ["fleet"] }), [qc]);
  /**
   * Read the Fleet through this Node from now on (decision 169), by this
   * address when given (decision 284): the choice is pinned on this device
   * until it fails or the user lets the app choose again.
   */
  const readThrough = useCallback(
    (nodeId: string, addr?: string) => {
      const c = candidates().find((n) => n.nodeId === nodeId);
      const a = addr ?? chooseAddress(nodeId, c?.addresses ?? [], undefined, domainAddr());
      if (!a) return;
      const t = { fingerprint: nodeId, addr: a };
      setPinned(t);
      setPinState(t);
      last.current = { nodeId, addr: a };
      failed.current.clear();
      void refresh();
    },
    [candidates, refresh],
  );
  /** Let the app choose again. */
  const unpin = useCallback(() => {
    setPinned(undefined);
    setPinState(undefined);
    failed.current.clear();
  }, []);
  return { ...query, refresh, readThrough, unpin, pin, attempts, candidates };
}

/**
 * Every out-of-sync pair any Node reported (decision 168): each Node gossips
 * the gaps it found in both directions, so the same gap can come from both
 * sides; the larger count wins.
 */
export function syncGaps(snap: FleetSnapshot | undefined): SyncGap[] {
  const byPair = new Map<string, SyncGap>();
  for (const st of snap?.status.values() ?? []) {
    for (const g of st.syncGaps ?? []) {
      const key = `${g.nodeId}>${g.aheadNodeId}`;
      const prev = byPair.get(key);
      if (!prev || g.count > prev.count) byPair.set(key, g);
    }
  }
  return [...byPair.values()];
}

/** "2 records (service jellyfin, service uptime-kuma)". */
export function gapLine(g: SyncGap): string {
  const more = g.count > g.records.length ? ", …" : "";
  return `${g.count} ${g.count === 1 ? "record" : "records"} (${g.records.join(", ")}${more})`;
}

/** One Service as the sidebar sees it: Fleet record plus the running Node's last status. */
export interface FleetService {
  spec: ServiceSpec;
  status?: ServiceStatus;
  node?: Node;
  nodeStatus?: FleetNodeStatus;
  /** How many protected options it was granted (decision 189). */
  grants?: number;
  /** Decision 135 words. */
  word: "Online" | "Starting" | "Stopped" | "Attention";
  nodeUnreachable: boolean;
}

export type NodeWord = "Healthy" | "Unreachable" | "Provisioning" | "Updating" | "Attention";

export function nodeWord(n: NodeStatus | undefined, now: number): NodeWord {
  // A Node still being installed is reported by the anchor from its
  // Installer until it first talks.
  if (n?.provisioning && !n.reachable) return "Provisioning";
  // Its own record says it is updating (decision 214): so it is, even while
  // it restarts and nobody can reach it.
  if (n?.updatingSince && now - n.updatingSince < UPDATE_LIMIT_MS) return "Updating";
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
        const grants = snap.records.filter((r) => r.kind === "grant" && !r.deleted && fromBinary(GrantSchema, r.body).service === spec.name).length;
        return { spec, status, node, nodeStatus, grants, word: unreachable ? "Stopped" : serviceWord(status), nodeUnreachable: unreachable } as FleetService;
      })
      .sort((a, b) => a.spec.name.localeCompare(b.spec.name));
  }, [snap, now]);
}
