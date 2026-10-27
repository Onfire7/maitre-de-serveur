// The demo Fleet's state and its clockwork (decision 153). Two Nodes, five
// Services in mixed states, two Spaces, Drives with unsorted files, a device
// asking to join. `tick()` advances the script one second; the transport in
// ./transport.ts answers RPCs from this state, and `window.demo` exposes it
// so a prototype can be nudged from the console.
import { create, toBinary, fromBinary, type MessageInitShape } from "@bufbuild/protobuf";
import {
  FleetSchema,
  NodeSchema,
  TrustedDeviceSchema,
  UpdatePolicySchema,
  type Record as Rec,
  RecordSchema,
} from "@/gen/maitre/v1/fleet_pb";
import { ServiceSpecSchema, ServiceStatus_State, type ServiceSpec, type ServiceStatus, PublicSettingsSchema } from "@/gen/maitre/v1/service_pb";
import { DriveRecordSchema, SpaceSchema, type Drive, type Space, type DriveRecord } from "@/gen/maitre/v1/storage_pb";
import type { NodeMetrics } from "@/gen/maitre/v1/node_pb";
import { UpdateStatus_State, type UpdateStatus } from "@/gen/maitre/v1/node_pb";
import type { Entry, Job, MoveRequest } from "@/gen/maitre/v1/files_pb";
import { TailscaleConfigSchema } from "@/gen/maitre/v1/tailscale_pb";
import bundledCatalog from "@/catalog/index.json";

export const NODE_A = "a1".repeat(32);
export const NODE_B = "b2".repeat(32);
export const DEVICE_ID = "c3".repeat(32);
export const RECOVERY_ID = "d4".repeat(32);
export const INSTALLER_ID = "e5".repeat(32);
export const FLEET_ID = "f6".repeat(16);

const GiB = 1024 ** 3;
const TiB = 1024 ** 4;

export interface DemoNode {
  nodeId: string;
  name: string;
  letter: string;
  addr: string;
  /** False while the script has it switched off or rebooting. */
  reachable: boolean;
  /** When this Node was last heard from by anyone, frozen while unreachable. */
  lastSeenUnixMs: number;
  metrics: NodeMetrics;
  metricsUnixMs: number;
  update: UpdateStatus;
  rebootAt?: number;
  imageRef: string;
  imageDigest: string;
  bootedUnixMs: number;
  publicIp: string;
  portMapping: string;
  /** What the other Node has trouble calling on this one (ADR 0007). */
  problems?: string[];
}

export interface DemoService {
  spec: ServiceSpec;
  state: ServiceStatus_State;
  healthy: boolean;
  restarts: number;
  message: string;
  startedUnixMs: number;
  forwardState: string;
  /** Script: the tick at which a Starting Service comes up. */
  readyAt?: number;
  /** Script: restart-looping; bumps restarts every few seconds. */
  looping?: boolean;
  statusUnixMs: number;
}

export interface DemoDrive extends Omit<Drive, "$typeName" | "record" | "folders"> {
  nodeId: string;
  record?: DriveRecord;
  folders: { name: string; kind: "space" | "orphan" | "unsorted"; spaceId: string }[];
}

interface FileNode {
  name: string;
  dir: boolean;
  size: number;
  modified: number;
  /** Drive ids that hold a copy (Pool scope) or the one Drive (Drive scope). */
  drives: string[];
  nodeId: string;
  /** Copies on other Nodes (a folder of a spanning Space exists on each). */
  also?: { nodeId: string; drives: string[] }[];
  children?: FileNode[];
}

export interface DemoJob extends Omit<Job, "$typeName" | "request"> {
  request: MoveRequest;
  nodeId: string;
  onDone?: () => void;
}

function metrics(init: Partial<Omit<NodeMetrics, "memoryTotalBytes" | "memoryUsedBytes" | "poolTotalBytes" | "poolUsedBytes">> & { memoryTotalBytes?: number; memoryUsedBytes?: number; poolTotalBytes?: number; poolUsedBytes?: number }): NodeMetrics {
  const big = (v: number | undefined, d: bigint) => (v === undefined ? d : BigInt(Math.round(v)));
  return {
    cpuPercent: 0,
    poolFallback: false,
    netRxBytesPerSec: 0,
    netTxBytesPerSec: 0,
    temperatureC: 40,
    load1m: 0.3,
    ...init,
    memoryTotalBytes: big(init.memoryTotalBytes, BigInt(16 * GiB)),
    memoryUsedBytes: big(init.memoryUsedBytes, BigInt(4 * GiB)),
    poolTotalBytes: big(init.poolTotalBytes, 0n),
    poolUsedBytes: big(init.poolUsedBytes, 0n),
  } as NodeMetrics;
}

function idle(image: string, digest: string): UpdateStatus {
  return {
    booted: { image, digest, version: "0.9.3", timestamp: "2026-10-20T03:12:00Z" },
    state: UpdateStatus_State.IDLE,
    message: "",
    progress: 0,
    target: "",
    supported: true,
    lastRollback: "",
    rollout: "",
    available: "",
    rollbackQueued: false,
    rollback: { image, digest: "sha256:" + "7e".repeat(32), version: "0.9.2", timestamp: "2026-10-13T03:10:00Z" },
  } as UpdateStatus;
}

const catalogEntries = (bundledCatalog as { entries: { id: string; name: string; image: string; quadlet: string; exposure?: string; infrastructure?: boolean; host_ports?: string[]; web?: { port: number; path?: string }; health?: { http?: string }; volumes?: { name: string; path: string; size?: string }[] }[] }).entries;

type SpecInit = Exclude<MessageInitShape<typeof ServiceSpecSchema>, ServiceSpec>;

function specFromCatalog(id: string, name: string, nodeId: string, webHostPort: number, extra: Partial<SpecInit> = {}): ServiceSpec {
  const e = catalogEntries.find((c) => c.id === id);
  if (!e) throw new Error(`demo: no Catalog entry ${id}`);
  return create(ServiceSpecSchema, {
    name,
    catalogId: id,
    image: e.image,
    nodeId,
    settings: {},
    exposure: e.exposure ?? "private",
    volumeSpaces: Object.fromEntries((e.volumes ?? []).map((v) => [v.name, v.name === "media" ? "media" : "general"])),
    createdUnixMs: BigInt(Date.now() - 14 * 86400_000),
    webHostPort,
    autoUpdate: true,
    quadlet: e.quadlet,
    infrastructure: !!e.infrastructure,
    webPort: e.web?.port ?? 0,
    webPath: e.web?.path ?? "/",
    healthHttp: e.health?.http ?? "",
    volumes: (e.volumes ?? []).map((v) => ({ name: v.name, path: v.path, size: v.size ?? "" })),
    hostPorts: e.host_ports ?? [],
    ...extra,
  } as SpecInit);
}

export class DemoFleet {
  now = Date.now();
  tickCount = 0;
  fleetName = "Home";
  nodes: DemoNode[];
  services: DemoService[];
  spaces: Space[];
  drives: DemoDrive[];
  records: Rec[] = [];
  jobs: DemoJob[] = [];
  trustedDevices = [
    { deviceId: DEVICE_ID, name: "Sean's laptop", recoveryOnly: false, addedUnixMs: Date.now() - 30 * 86400_000, nodeScope: "" },
    { deviceId: RECOVERY_ID, name: "Recovery Phrase", recoveryOnly: true, addedUnixMs: Date.now() - 30 * 86400_000, nodeScope: "" },
  ];
  /** A device that scanned a code and waits for approval (shows on Devices). */
  pendingDevice = { id: "9a".repeat(32), name: "Kitchen tablet", machine: "iPad" };
  /** An Installer on the LAN (shows under Nodes > Add a Node). */
  installer = { id: INSTALLER_ID, name: "Cedar", machine: "Intel NUC", addr: "10.0.0.9:7443", pairingCode: "482 913", provisioning: false, progress: 0, log: [] as string[] };
  files = new Map<string, FileNode[]>();
  private listeners = new Set<() => void>();
  private nextJob = 1;

  constructor() {
    const t = this.now;
    this.nodes = [
      {
        nodeId: NODE_A,
        name: "Alder",
        letter: "A",
        addr: "10.0.0.5:7443",
        reachable: true,
        lastSeenUnixMs: t,
        metrics: metrics({ cpuPercent: 12, memoryTotalBytes: 32 * GiB, memoryUsedBytes: 9.5 * GiB, poolTotalBytes: 3.6 * TiB, poolUsedBytes: 1.9 * TiB, temperatureC: 47, load1m: 0.8, netRxBytesPerSec: 1.2e6, netTxBytesPerSec: 4.8e6 }),
        metricsUnixMs: t,
        update: idle("ghcr.io/onfire7/maitre-server:stable", "sha256:" + "3a".repeat(32)),
        imageRef: "ghcr.io/onfire7/maitre-server:stable",
        imageDigest: "sha256:" + "3a".repeat(32),
        bootedUnixMs: t - 6 * 86400_000,
        publicIp: "203.0.113.5",
        portMapping: "router: eero",
      },
      {
        nodeId: NODE_B,
        name: "Basalt",
        letter: "B",
        addr: "10.0.0.6:7443",
        reachable: true,
        lastSeenUnixMs: t,
        metrics: metrics({ cpuPercent: 61, memoryTotalBytes: 16 * GiB, memoryUsedBytes: 11 * GiB, poolTotalBytes: 1.8 * TiB, poolUsedBytes: 0.4 * TiB, temperatureC: 58, load1m: 2.4, netRxBytesPerSec: 0.3e6, netTxBytesPerSec: 0.1e6 }),
        metricsUnixMs: t,
        // The registry has a newer stable image for Basalt (its Update button).
        update: { ...idle("ghcr.io/onfire7/maitre-server:stable", "sha256:" + "3a".repeat(32)), available: "sha256:" + "4b".repeat(32) } as UpdateStatus,
        imageRef: "ghcr.io/onfire7/maitre-server:stable",
        imageDigest: "sha256:" + "3a".repeat(32),
        bootedUnixMs: t - 2 * 3600_000,
        publicIp: "203.0.113.5",
        portMapping: "router: eero",
      },
    ];
    this.spaces = [
      create(SpaceSchema, { spaceId: "general", name: "General", placement: "local_first", requireTags: [], nodeIds: [], priority: 0, createdUnixMs: BigInt(t - 30 * 86400_000), spanNodes: false, exclusive: false, defaultForNew: true }),
      create(SpaceSchema, { spaceId: "media", name: "Media", placement: "most_free", requireTags: ["hdd"], nodeIds: [], priority: 10, createdUnixMs: BigInt(t - 20 * 86400_000), spanNodes: true, exclusive: true, defaultForNew: false }),
    ];
    const drive = (nodeId: string, id: string, name: string, model: string, size: number, free: number, { record: recOpts, ...opts }: Partial<DemoDrive> = {}): DemoDrive => ({
      nodeId,
      device: opts.device ?? `/dev/${id}`,
      driveId: id,
      model,
      serial: "SN" + id.toUpperCase(),
      sizeBytes: BigInt(Math.round(size)),
      removable: false,
      rotational: opts.rotational ?? true,
      transport: opts.transport ?? "sata",
      fstype: "ext4",
      label: "",
      system: false,
      adopted: true,
      mountPath: `/var/mnt/drives/${id}`,
      autoTags: opts.autoTags ?? ["hdd"],
      freeBytes: BigInt(Math.round(free)),
      suggestedName: "",
      spaces: opts.spaces ?? ["general"],
      folders: opts.folders ?? [{ name: "general", kind: "space", spaceId: "general" }],
      record: create(DriveRecordSchema, { driveId: id, nodeId, name, model, serial: "SN" + id.toUpperCase(), sizeBytes: BigInt(Math.round(size)), fstype: "ext4", autoTags: opts.autoTags ?? ["hdd"], tags: recOpts?.tags ?? [], origin: recOpts?.origin ?? "formatted", addedUnixMs: BigInt(t - 25 * 86400_000) }),
      ...opts,
    });
    this.drives = [
      drive(NODE_A, "nvme0n1", "Apollo", "Samsung 980 PRO", 0.9 * TiB, 0.55 * TiB, { device: "/dev/nvme0n1", rotational: false, transport: "nvme", autoTags: ["ssd", "internal"], spaces: ["general"], record: { origin: "internal", tags: [] } as unknown as DriveRecord }),
      drive(NODE_A, "sda", "Atlas", "WD Red Plus 4TB", 4 * TiB, 0.8 * TiB, { spaces: ["media"], folders: [{ name: "media", kind: "space", spaceId: "media" }] }),
      drive(NODE_A, "sdb", "Aspen", "WD Red Plus 4TB", 4 * TiB, 2.9 * TiB, { spaces: ["media"], folders: [{ name: "media", kind: "space", spaceId: "media" }, { name: "photos-2019", kind: "orphan", spaceId: "photos" }] }),
      drive(NODE_B, "nvme0n1", "Birch", "Kingston NV2", 0.5 * TiB, 0.42 * TiB, { device: "/dev/nvme0n1", rotational: false, transport: "nvme", autoTags: ["ssd", "internal"], record: { origin: "internal", tags: [] } as unknown as DriveRecord }),
      drive(NODE_B, "sda", "Basin", "Seagate IronWolf 8TB", 8 * TiB, 0.55 * TiB, { spaces: ["media"], folders: [{ name: "media", kind: "space", spaceId: "media" }, { name: "Downloads", kind: "unsorted", spaceId: "" }, { name: "old-backups", kind: "unsorted", spaceId: "" }], record: { origin: "adopted", tags: [] } as unknown as DriveRecord }),
    ];
    this.services = [
      this.service(specFromCatalog("jellyfin", "jellyfin", NODE_A, 20001), ServiceStatus_State.RUNNING, { healthy: true, startedUnixMs: t - 6 * 86400_000 }),
      this.service(specFromCatalog("adguard-home", "adguard", NODE_A, 20002), ServiceStatus_State.RUNNING, { healthy: true, startedUnixMs: t - 6 * 86400_000 }),
      this.service(specFromCatalog("uptime-kuma", "uptime", NODE_B, 20001), ServiceStatus_State.STARTING, { readyAt: 25, message: "pulling image" }),
      this.service(specFromCatalog("minecraft", "minecraft", NODE_B, 0, { settings: { EULA: "false" }, forwardPorts: true }), ServiceStatus_State.FAILED, { restarts: 7, looping: true, message: "exited with code 1: you must accept the EULA (setting EULA=true)", forwardState: "25565/tcp forwarded by eero" }),
      this.service(create(ServiceSpecSchema, { name: "paperless", catalogId: "paperless-ngx", image: "ghcr.io/paperless-ngx/paperless-ngx:2.14@sha256:" + "9b".repeat(32), nodeId: NODE_A, exposure: "remote", volumeSpaces: { data: "general", media: "general" }, createdUnixMs: BigInt(t - 40 * 86400_000), webHostPort: 20003, webPort: 8000, webPath: "/", quadlet: "[Container]\nImage={{ .Image }}\n", volumes: [{ name: "data", path: "/usr/src/paperless/data", size: "" }, { name: "media", path: "/usr/src/paperless/media", size: "" }], autoUpdate: false }), ServiceStatus_State.STOPPED, { message: "stopped by you" }),
    ];
    this.seedFiles();
    this.rebuildRecords();
  }

  private service(spec: ServiceSpec, state: ServiceStatus_State, init: Partial<DemoService>): DemoService {
    return { spec, state, healthy: false, restarts: 0, message: "", startedUnixMs: 0, forwardState: "", statusUnixMs: this.now, ...init };
  }

  // --- records -----------------------------------------------------------

  private rec(kind: string, id: string, owner: string, body: Uint8Array, deleted = false): Rec {
    const old = this.records.find((r) => r.kind === kind && r.id === id);
    return create(RecordSchema, { kind, id, ownerNodeId: owner, version: BigInt(Number(old?.version ?? 0n) + 1), updatedUnixMs: BigInt(this.now), body, deleted });
  }

  /** Rebuild the synced database from the model. Versions bump on every call. */
  rebuildRecords() {
    const out: Rec[] = [];
    out.push(this.rec("fleet", FLEET_ID, NODE_A, toBinary(FleetSchema, create(FleetSchema, { fleetId: FLEET_ID, name: this.fleetName, createdUnixMs: BigInt(this.now - 30 * 86400_000) }))));
    for (const n of this.nodes) {
      out.push(this.rec("node", n.nodeId, n.nodeId, toBinary(NodeSchema, create(NodeSchema, { nodeId: n.nodeId, name: n.name, publicKeyPem: `-----BEGIN PUBLIC KEY-----\ndemo ${n.letter}\n-----END PUBLIC KEY-----\n`, addresses: [n.addr, `${n.name.toLowerCase()}.local:7443`], provisionedUnixMs: BigInt(this.now - 30 * 86400_000), imageRef: n.imageRef, imageDigest: n.imageDigest, bootedUnixMs: BigInt(n.bootedUnixMs), letter: n.letter }))));
    }
    for (const d of this.trustedDevices) {
      out.push(this.rec("trusted_device", d.deviceId, NODE_A, toBinary(TrustedDeviceSchema, create(TrustedDeviceSchema, { deviceId: d.deviceId, name: d.name, publicKeyPem: "demo", recoveryOnly: d.recoveryOnly, addedUnixMs: BigInt(d.addedUnixMs), nodeScope: d.nodeScope }))));
    }
    for (const s of this.spaces) out.push(this.rec("space", s.spaceId, NODE_A, toBinary(SpaceSchema, s)));
    for (const d of this.drives) if (d.record) out.push(this.rec("drive", d.driveId + "@" + d.nodeId.slice(0, 4), d.nodeId, toBinary(DriveRecordSchema, d.record)));
    for (const s of this.services) out.push(this.rec("service", s.spec.name, s.spec.nodeId, toBinary(ServiceSpecSchema, s.spec)));
    out.push(this.rec("update_policy", "fleet", NODE_A, toBinary(UpdatePolicySchema, create(UpdatePolicySchema, { automatic: true, windowStartHour: 3, windowEndHour: 5, stageHours: 24, paused: false }))));
    out.push(this.rec("tailscale", "fleet", NODE_A, toBinary(TailscaleConfigSchema, create(TailscaleConfigSchema, { oauthClientId: "k7Hs2demoClientId", oauthClientSecret: "tskey-client-demo", tag: "tag:maitre", addedUnixMs: BigInt(this.now - 12 * 86400_000) }))));
    out.push(this.rec("public_settings", "fleet", NODE_A, toBinary(PublicSettingsSchema, create(PublicSettingsSchema, { acmeEmail: "" }))));
    // Keep tombstones.
    for (const r of this.records) if (r.deleted && !out.some((o) => o.kind === r.kind && o.id === r.id)) out.push(r);
    this.records = out;
    this.changed();
  }

  /** PutRecord as the owner would apply it: body replaces, version bumps, model follows for kinds it knows. */
  putRecord(r: { kind: string; id: string; body?: Uint8Array; deleted?: boolean }): Rec {
    const owner = this.records.find((x) => x.kind === r.kind && x.id === r.id)?.ownerNodeId ?? NODE_A;
    const rec = this.rec(r.kind, r.id, owner, r.body ?? new Uint8Array(), !!r.deleted);
    this.records = [...this.records.filter((x) => !(x.kind === r.kind && x.id === r.id)), rec];
    if (r.kind === "space") {
      this.spaces = this.spaces.filter((s) => s.spaceId !== r.id);
      if (!r.deleted && r.body) {
        const sp = fromBinary(SpaceSchema, r.body);
        if (sp.defaultForNew) for (const s of this.spaces) s.defaultForNew = false;
        this.spaces.push(sp);
      }
    } else if (r.kind === "fleet" && r.body) {
      this.fleetName = fromBinary(FleetSchema, r.body).name;
    } else if (r.kind === "trusted_device") {
      this.trustedDevices = this.trustedDevices.filter((d) => d.deviceId !== r.id);
      if (!r.deleted && r.body) {
        const d = fromBinary(TrustedDeviceSchema, r.body);
        this.trustedDevices.push({ deviceId: d.deviceId, name: d.name, recoveryOnly: d.recoveryOnly, addedUnixMs: Number(d.addedUnixMs), nodeScope: d.nodeScope });
        if (d.deviceId === this.pendingDevice.id) this.pendingDevice = { id: "", name: "", machine: "" };
      }
    } else if (r.kind === "node" && r.deleted) {
      this.nodes = this.nodes.filter((n) => n.nodeId !== r.id);
      this.services = this.services.filter((s) => s.spec.nodeId !== r.id);
    }
    this.changed();
    return rec;
  }

  // --- status -------------------------------------------------------------

  node(id: string): DemoNode {
    const n = this.nodes.find((x) => x.nodeId === id);
    if (!n) throw new Error(`demo: no Node ${id}`);
    return n;
  }

  nodeByAddr(addr: string): DemoNode | undefined {
    return this.nodes.find((n) => n.addr === addr || `${n.name.toLowerCase()}.local:7443` === addr);
  }

  status(s: DemoService): ServiceStatus {
    const node = this.nodes.find((n) => n.nodeId === s.spec.nodeId);
    const web = s.spec.webPort > 0;
    return {
      spec: s.spec,
      state: s.state,
      healthy: s.healthy,
      restarts: s.restarts,
      message: s.message,
      url: web ? `http://${s.spec.name}.local` : "",
      startedUnixMs: BigInt(s.startedUnixMs),
      remoteUrl: web && s.spec.exposure !== "private" ? `https://${s.spec.name}.home-fleet.ts.net` : "",
      remoteState: s.spec.exposure !== "private" ? "up" : "",
      forwardState: s.forwardState || (s.spec.forwardPorts && node ? `${s.spec.hostPorts.join(", ")} forwarded by ${node.portMapping.replace("router: ", "")}` : ""),
    } as ServiceStatus;
  }

  /** A Node's metrics with its Drives' fill, as the agent reports them. */
  metricsOf(n: DemoNode): NodeMetrics {
    return { ...n.metrics, drives: this.drives.filter((d) => d.nodeId === n.nodeId && d.adopted).map((d) => ({ driveId: d.driveId, totalBytes: d.sizeBytes, usedBytes: d.sizeBytes - d.freeBytes })) } as NodeMetrics;
  }

  servicesOn(nodeId: string): ServiceStatus[] {
    return this.services.filter((s) => s.spec.nodeId === nodeId).map((s) => this.status(s));
  }

  // --- script -------------------------------------------------------------

  /** Switch a Node off or on, as if the plug was pulled. */
  /** Out-of-sync copies the Nodes report (decision 168), e.g. after an old Node refused a tombstone. */
  syncGaps: { nodeId: string; aheadNodeId: string; count: number; records: string[] }[] = [];

  setSyncGaps(gaps: { nodeId: string; aheadNodeId: string; count: number; records: string[] }[]) {
    this.syncGaps = gaps;
    this.changed();
  }

  /** Calls the other Node gets errors from, e.g. an older version (ADR 0007). */
  setProblems(nodeId: string, problems: string[]) {
    this.node(nodeId).problems = problems;
    this.changed();
  }

  setReachable(nodeId: string, reachable: boolean) {
    const n = this.node(nodeId);
    n.reachable = reachable;
    if (reachable) n.lastSeenUnixMs = this.now;
    this.changed();
  }

  /** One second of the Fleet's life. */
  tick(nowMs = Date.now()) {
    this.now = nowMs;
    this.tickCount++;
    for (const n of this.nodes) {
      if (n.rebootAt && this.now >= n.rebootAt) {
        n.rebootAt = undefined;
        n.reachable = true;
        n.bootedUnixMs = this.now;
        for (const s of this.services) if (s.spec.nodeId === n.nodeId && s.state === ServiceStatus_State.RUNNING) s.startedUnixMs = this.now;
      }
      if (!n.reachable) continue;
      n.lastSeenUnixMs = this.now;
      const m = n.metrics;
      const drift = (v: number, lo: number, hi: number, step: number) => Math.min(hi, Math.max(lo, v + (Math.random() - 0.5) * step));
      m.cpuPercent = drift(m.cpuPercent, 2, 98, 8);
      m.load1m = drift(m.load1m, 0.05, 8, 0.3);
      m.temperatureC = drift(m.temperatureC, 35, 80, 1);
      m.netRxBytesPerSec = drift(m.netRxBytesPerSec, 0, 50e6, 0.6e6);
      m.netTxBytesPerSec = drift(m.netTxBytesPerSec, 0, 50e6, 0.6e6);
      n.metricsUnixMs = this.now;
      const u = n.update;
      if (u.state === UpdateStatus_State.UPDATING) {
        u.progress = Math.min(1, u.progress + 0.04);
        u.message = u.progress < 1 ? `downloading ${Math.round(u.progress * 100)}%` : "staged; reboot to apply";
        if (u.progress >= 1) {
          u.state = UpdateStatus_State.IDLE;
          u.staged = { image: u.target, digest: "sha256:" + "4b".repeat(32), version: "0.9.4", timestamp: "2026-10-26T03:00:00Z" } as UpdateStatus["staged"];
          if (u.available === u.staged!.digest) u.available = "";
        }
      }
    }
    for (const s of this.services) {
      const n = this.nodes.find((x) => x.nodeId === s.spec.nodeId);
      if (!n?.reachable) continue;
      if (s.readyAt !== undefined && this.tickCount >= s.readyAt) {
        s.readyAt = undefined;
        s.state = ServiceStatus_State.RUNNING;
        s.healthy = true;
        s.message = "";
        s.startedUnixMs = this.now;
        s.statusUnixMs = this.now;
      }
      if (s.looping && this.tickCount % 15 === 0) {
        s.restarts++;
        s.statusUnixMs = this.now;
      }
    }
    for (const j of this.jobs) {
      if (j.state !== "running") continue;
      j.bytesDone = j.bytesDone + BigInt(Math.min(Number(j.bytesTotal - j.bytesDone), 180_000_000));
      if (j.bytesDone >= j.bytesTotal) {
        j.state = "done";
        j.finishedUnixMs = BigInt(this.now);
        j.message = "verified";
        if (j.onDone) j.onDone();
        else this.applyMove(j);
      } else j.message = "copying";
    }
    if (this.installer.provisioning && this.installer.progress < 1) {
      this.installer.progress = Math.min(1, this.installer.progress + 0.02);
      const steps = ["partitioning", "writing image", "sealing keys", "first boot"];
      const step = steps[Math.min(steps.length - 1, Math.floor(this.installer.progress * steps.length))];
      if (this.installer.log.at(-1) !== step) this.installer.log.push(step);
    }
    this.changed();
  }

  // --- controls -----------------------------------------------------------

  control(name: string, action: "start" | "stop" | "restart" | "remove" | "update" | "rollback"): ServiceStatus {
    const s = this.services.find((x) => x.spec.name === name);
    if (!s) throw new Error(`no Service ${name}`);
    s.statusUnixMs = this.now;
    switch (action) {
      case "start":
      case "restart":
        s.state = ServiceStatus_State.STARTING;
        s.healthy = false;
        s.message = "";
        s.looping = false;
        s.readyAt = this.tickCount + 6;
        break;
      case "stop":
        s.state = ServiceStatus_State.STOPPED;
        s.healthy = false;
        s.looping = false;
        s.readyAt = undefined;
        s.message = "stopped by you";
        break;
      case "remove":
        this.services = this.services.filter((x) => x !== s);
        this.records = this.records.map((r) => (r.kind === "service" && r.id === name ? { ...r, deleted: true, version: r.version + 1n } : r));
        s.state = ServiceStatus_State.REMOVING;
        break;
      case "update":
        s.spec.previousImage = s.spec.image;
        s.spec.image = s.spec.image.replace(/@sha256:.*/, "@sha256:" + "5c".repeat(32));
        s.state = ServiceStatus_State.STARTING;
        s.readyAt = this.tickCount + 10;
        s.message = "pulling image";
        break;
      case "rollback":
        if (s.spec.previousImage) [s.spec.image, s.spec.previousImage] = [s.spec.previousImage, ""];
        s.state = ServiceStatus_State.STARTING;
        s.readyAt = this.tickCount + 6;
        break;
    }
    this.changed();
    return this.status(s);
  }

  install(spec: ServiceSpec, nodeId: string): ServiceStatus {
    if (this.services.some((s) => s.spec.name === spec.name)) throw new Error(`a Service named ${spec.name} already exists`);
    const def = this.spaces.find((s) => s.defaultForNew);
    if (!def) throw new Error("no Storage Space is marked as the default for new Services");
    const full = create(ServiceSpecSchema, { ...spec, nodeId, createdUnixMs: BigInt(this.now), webHostPort: spec.webPort ? 20010 + this.services.length : 0 });
    for (const v of full.volumes) if (!full.volumeSpaces[v.name]) full.volumeSpaces[v.name] = def.spaceId;
    const s = this.service(full, ServiceStatus_State.STARTING, { readyAt: this.tickCount + 8, message: "pulling image" });
    this.services.push(s);
    this.rebuildRecords();
    return this.status(s);
  }

  logs(name: string, lines: number): string[] {
    const s = this.services.find((x) => x.spec.name === name);
    if (!s) return [];
    const out: string[] = [];
    const base = this.now - lines * 1000;
    for (let i = 0; i < lines; i++) {
      const ts = new Date(base + i * 1000).toISOString();
      if (s.looping && i % 5 === 4) out.push(`${ts} [FATAL] you must accept the EULA (setting EULA=true)`);
      else if (i % 7 === 0) out.push(`${ts} [INFO] ${s.spec.name}: listening on :${s.spec.webPort || s.spec.hostPorts[0] || "0"}`);
      else out.push(`${ts} [INFO] ${s.spec.name}: request handled in ${Math.round(Math.random() * 90)}ms`);
    }
    return out;
  }

  startUpdate(nodeId: string, image: string) {
    const n = this.node(nodeId);
    n.update.state = UpdateStatus_State.UPDATING;
    n.update.target = image;
    n.update.progress = 0;
    n.update.message = "downloading";
    this.changed();
  }

  reboot(nodeId: string) {
    const n = this.node(nodeId);
    n.reachable = false;
    n.rebootAt = this.now + 20_000;
    if (n.update.staged) {
      n.update.booted = n.update.staged;
      n.update.staged = undefined;
      n.imageDigest = n.update.booted!.digest;
    }
    this.changed();
  }

  rename(nodeId: string, name: string) {
    this.node(nodeId).name = name;
    this.rebuildRecords();
  }

  // --- files --------------------------------------------------------------

  private seedFiles() {
    const t = this.now;
    const f = (name: string, size: number, drives: string[], nodeId: string, daysAgo = 10): FileNode => ({ name, dir: false, size, modified: t - daysAgo * 86400_000, drives, nodeId });
    const d = (name: string, children: FileNode[], drives: string[], nodeId: string): FileNode => ({ name, dir: true, size: 0, modified: t - 3 * 86400_000, drives, nodeId, children });
    this.files.set("space:media", [
      { name: "Movies", dir: true, size: 0, modified: t - 3 * 86400_000, drives: ["sda", "sdb"], nodeId: NODE_A, children: [f("Arrival (2016).mkv", 8.2 * GiB, ["sda"], NODE_A), f("Dune (2021).mkv", 14.1 * GiB, ["sdb"], NODE_A), f("Paddington 2 (2017).mkv", 6.4 * GiB, ["sda"], NODE_B)], also: [{ nodeId: NODE_B, drives: ["sda"] }] },
      { name: "Photos", dir: true, size: 0, modified: t - 9 * 86400_000, drives: ["sda", "sdb", "sdc", "sdd", "sde", "sdf", "sdg", "sdh", "sdi", "sdj", "sdk", "sdl"], nodeId: NODE_A, children: [f("2019.zip", 2.2 * GiB, ["sda"], NODE_A, 400)], also: [{ nodeId: NODE_B, drives: ["sda", "sdb", "sdc", "sdd", "sde", "sdf", "sdg", "sdh"] }] },
      d("Shows", [d("Severance", [f("S01E01.mkv", 2.1 * GiB, ["sda"], NODE_B), f("S01E02.mkv", 2.0 * GiB, ["sda"], NODE_B)], ["sda"], NODE_B)], ["sda"], NODE_B),
      d("Music", [f("library.db", 42e6, ["sdb"], NODE_A)], ["sdb"], NODE_A),
    ]);
    this.files.set("space:general", [
      d("jellyfin", [d("config", [f("system.xml", 12e3, ["nvme0n1"], NODE_A)], ["nvme0n1"], NODE_A)], ["nvme0n1"], NODE_A),
      d("paperless", [d("data", [f("index.db", 220e6, ["nvme0n1"], NODE_A)], ["nvme0n1"], NODE_A)], ["nvme0n1"], NODE_A),
      d("uptime", [f("kuma.db", 9e6, ["nvme0n1"], NODE_B)], ["nvme0n1"], NODE_B),
    ]);
    this.files.set("drive:sda@" + NODE_B, [
      d("media", [], ["sda"], NODE_B),
      d("Downloads", [f("ubuntu-24.04.iso", 5.9 * GiB, ["sda"], NODE_B, 200), f("family-reunion.mp4", 3.2 * GiB, ["sda"], NODE_B, 90), d("photos-phone", [f("IMG_2041.jpg", 4.1e6, ["sda"], NODE_B, 60), f("IMG_2042.jpg", 3.9e6, ["sda"], NODE_B, 60)], ["sda"], NODE_B)], ["sda"], NODE_B),
      d("old-backups", [f("2021-laptop.tar", 120 * GiB, ["sda"], NODE_B, 700)], ["sda"], NODE_B),
    ]);
    this.files.set("drive:sdb@" + NODE_A, [d("media", [], ["sdb"], NODE_A), d("photos-2019", [f("holiday.zip", 2.2 * GiB, ["sdb"], NODE_A, 400)], ["sdb"], NODE_A)]);
  }

  private tree(scope: { spaceId?: string; driveId?: string }, nodeId: string): FileNode[] {
    if (scope.spaceId) return this.files.get("space:" + scope.spaceId) ?? [];
    return this.files.get(`drive:${scope.driveId}@${nodeId}`) ?? [];
  }

  private walk(root: FileNode[], path: string): FileNode[] | undefined {
    let cur = root;
    for (const part of path.split("/").filter(Boolean)) {
      const next = cur.find((n) => n.name === part && n.dir);
      if (!next) return undefined;
      cur = next.children ?? [];
    }
    return cur;
  }

  listDir(scope: { spaceId?: string; driveId?: string }, nodeId: string, path: string): Entry[] {
    const dir = this.walk(this.tree(scope, nodeId), path);
    if (!dir) throw new Error(`no such folder: ${path || "/"}`);
    return dir.map((n) => ({ name: n.name, dir: n.dir, sizeBytes: BigInt(Math.round(n.size)), modifiedUnixMs: BigInt(n.modified), locations: [...n.drives.map((d) => ({ nodeId: n.nodeId, driveId: d })), ...(n.also ?? []).flatMap((a) => a.drives.map((d) => ({ nodeId: a.nodeId, driveId: d })))] })) as Entry[];
  }

  makeDir(scope: { spaceId?: string; driveId?: string }, nodeId: string, path: string) {
    const parts = path.split("/").filter(Boolean);
    const name = parts.pop();
    if (!name) throw new Error("name the new folder");
    if (name.startsWith(".")) throw new Error(`"${name}": folder names cannot start with a dot`);
    const key = scope.spaceId ? "space:" + scope.spaceId : `drive:${scope.driveId}@${nodeId}`;
    if (!this.files.has(key)) this.files.set(key, []);
    const parent = this.walk(this.files.get(key)!, parts.join("/"));
    if (!parent) throw new Error(`${path}: the folder it goes in does not exist`);
    if (parent.some((n) => n.name === name)) throw Object.assign(new Error(`${path} already exists`), { exists: true });
    const drive = scope.driveId ?? this.drives.find((d) => d.nodeId === nodeId)?.driveId ?? "";
    parent.push({ name, dir: true, size: 0, modified: this.now, drives: drive ? [drive] : [], nodeId, children: [] });
  }

  usage(scope: { spaceId?: string; driveId?: string }, nodeId: string, path: string): { bytes: bigint; files: bigint } {
    const dir = this.walk(this.tree(scope, nodeId), path) ?? [];
    // A Space that does not span Nodes is one Pool per Node: count this Node's files only.
    const local = !!scope.spaceId && !this.spaces.find((s) => s.spaceId === scope.spaceId)?.spanNodes;
    let bytes = 0;
    let files = 0;
    const rec = (ns: FileNode[]) => {
      for (const n of ns) {
        if (n.dir) rec(n.children ?? []);
        else if (!local || n.nodeId === nodeId) {
          bytes += n.size;
          files++;
        }
      }
    };
    rec(dir);
    return { bytes: BigInt(Math.round(bytes)), files: BigInt(files) };
  }

  move(req: MoveRequest, nodeId: string): string {
    const u = this.usage({ spaceId: req.from?.spaceId, driveId: req.from?.driveId }, nodeId, req.path);
    const parent = this.walk(this.tree({ spaceId: req.from?.spaceId, driveId: req.from?.driveId }, nodeId), req.path.split("/").slice(0, -1).join("/"));
    const item = parent?.find((n) => n.name === req.path.split("/").at(-1));
    const total = item && !item.dir ? BigInt(Math.round(item.size)) : u.bytes;
    const job: DemoJob = { jobId: `job-${this.nextJob++}`, state: "running", message: "starting", bytesDone: 0n, bytesTotal: total || 1n, current: req.path, startedUnixMs: BigInt(this.now), finishedUnixMs: 0n, request: req, nodeId };
    this.jobs.push(job);
    this.changed();
    return job.jobId;
  }

  private applyMove(j: DemoJob) {
    const req = j.request;
    const from = this.tree({ spaceId: req.from?.spaceId, driveId: req.from?.driveId }, j.nodeId);
    const parts = req.path.split("/").filter(Boolean);
    const parent = this.walk(from, parts.slice(0, -1).join("/"));
    const idx = parent?.findIndex((n) => n.name === parts.at(-1)) ?? -1;
    if (!parent || idx < 0) return;
    const [item] = parent.splice(idx, 1);
    const toNode = req.toNodeId || j.nodeId;
    const dest = this.walk(this.tree({ spaceId: req.toSpace }, toNode), req.toPath);
    const retarget = (n: FileNode) => {
      n.nodeId = toNode;
      n.drives = req.toDriveId ? [req.toDriveId] : [this.drives.find((d) => d.nodeId === toNode && d.spaces.includes(req.toSpace))?.driveId ?? "nvme0n1"];
      n.children?.forEach(retarget);
    };
    retarget(item);
    (dest ?? this.files.get("space:" + req.toSpace) ?? []).push(item);
    this.changed();
  }

  /** MoveVolume: stop, queue the folder move, follow the spec when done, restart. */
  moveVolume(name: string, volume: string, toSpace: string, nodeId: string): string {
    const s = this.services.find((x) => x.spec.name === name);
    if (!s) throw new Error(`no Service ${name}`);
    if (!s.spec.volumes.some((v) => v.name === volume)) throw new Error(`${name} has no volume ${volume}`);
    const from = s.spec.volumeSpaces[volume];
    if (from === toSpace) throw new Error(`volume ${volume} is already in Space ${toSpace}`);
    if (!this.spaces.some((sp) => sp.spaceId === toSpace)) throw new Error(`no Space ${toSpace}`);
    const wasRunning = s.state === ServiceStatus_State.RUNNING || s.state === ServiceStatus_State.STARTING;
    s.state = ServiceStatus_State.STOPPED;
    s.healthy = false;
    s.readyAt = undefined;
    s.message = `stopped while ${volume} moves to ${toSpace}`;
    s.statusUnixMs = this.now;
    const path = `${name}/${volume}`;
    const req = { from: { spaceId: from, driveId: "" }, path, toSpace, toPath: path, toNodeId: "", toDriveId: "", fromNodeId: "" } as MoveRequest;
    const job: DemoJob = { jobId: `job-${this.nextJob++}`, state: "running", message: "copying", bytesDone: 0n, bytesTotal: BigInt(Math.round(1.5 * GiB)), current: path, startedUnixMs: BigInt(this.now), finishedUnixMs: 0n, request: req, nodeId, onDone: () => {
      s.spec.volumeSpaces[volume] = toSpace;
      this.rebuildRecords();
      if (wasRunning) {
        s.state = ServiceStatus_State.STARTING;
        s.readyAt = this.tickCount + 4;
      }
      s.message = "";
      s.statusUnixMs = this.now;
    } };
    this.jobs.push(job);
    this.changed();
    return job.jobId;
  }

  cancelJob(id: string) {
    const j = this.jobs.find((x) => x.jobId === id);
    if (j && j.state === "running") {
      j.state = "cancelled";
      j.finishedUnixMs = BigInt(this.now);
      j.message = "cancelled; source untouched";
    }
    this.changed();
  }

  // --- subscriptions ------------------------------------------------------

  subscribe(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  private changed() {
    for (const fn of this.listeners) fn();
  }
}

let shared: DemoFleet | undefined;
let timer: ReturnType<typeof setInterval> | undefined;

/** The one demo Fleet of this Console, ticking once a second. */
export function demoFleet(): DemoFleet {
  if (!shared) {
    shared = new DemoFleet();
    // `?empty=spaces` starts the demo with no Storage Space (empty-state fixture).
    if (typeof window !== "undefined" && new URLSearchParams(window.location.search).get("empty") === "spaces") {
      shared.spaces = [];
      for (const d of shared.drives) d.spaces = [];
      shared.rebuildRecords();
    }
    if (typeof window !== "undefined") {
      timer = setInterval(() => shared!.tick(), 1000);
      (window as unknown as { demo: DemoFleet }).demo = shared;
    }
  }
  return shared;
}

/** For tests: a fresh Fleet, no clock. */
export function resetDemoFleet() {
  if (timer) clearInterval(timer);
  timer = undefined;
  shared = undefined;
}
