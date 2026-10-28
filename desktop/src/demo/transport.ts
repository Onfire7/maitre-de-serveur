// A Connect Transport that answers from the demo Fleet (decision 153). The
// target address picks the Node (or the Installer) being asked, so the
// anchor logic, unreachable Nodes and per-Node calls behave as they would
// against real machines. Every RPC the Console uses is here; an unknown one
// fails with Unimplemented so a prototype never silently gets empty data.
import { volumeDir } from "@/files/layout";
import { create, type DescMessage, type DescMethodUnary, type MessageInitShape, type MessageShape } from "@bufbuild/protobuf";
import { Code, ConnectError, type Transport, type UnaryResponse } from "@connectrpc/connect";
import { ControlServiceRequest_Action, ServiceStatus_State, type ServiceSpec } from "@/gen/maitre/v1/service_pb";
import { GrantsNeededSchema } from "@/gen/maitre/v1/fleet_pb";
import { Provisioning_Phase } from "@/gen/maitre/v1/installer_pb";
import { UpdateStatus_State } from "@/gen/maitre/v1/node_pb";
import bundledCatalog from "@/catalog/index.json";
import { type DemoFleet, type DemoNode, demoFleet } from "./fleet";

type Handler = (fleet: DemoFleet, node: DemoNode | undefined, input: Record<string, unknown>, addr: string) => unknown;

const fail = (code: Code, msg: string) => new ConnectError(msg, code);

function needNode(node: DemoNode | undefined, addr: string): DemoNode {
  if (!node) throw fail(Code.Unavailable, `demo: nothing answers at ${addr}`);
  if (!node.reachable) throw fail(Code.Unavailable, `dial tcp ${node.addr}: no route to host`);
  return node;
}

/** FailedPrecondition with a GrantsNeeded detail, as a Node refuses an ungranted option (decision 189). */
function needGrants(f: DemoFleet, spec: ServiceSpec, nodeId: string) {
  const need = f.grantsNeeded(spec, nodeId);
  if (need) throw new ConnectError(`${spec.name} needs ${need.options.length === 1 ? "a grant" : "grants"} for ${need.options.join(", ")}`, Code.FailedPrecondition, undefined, [{ desc: GrantsNeededSchema, value: need }]);
}

const actions: Record<number, "start" | "stop" | "restart" | "remove" | "update" | "rollback"> = {
  [ControlServiceRequest_Action.START]: "start",
  [ControlServiceRequest_Action.STOP]: "stop",
  [ControlServiceRequest_Action.RESTART]: "restart",
  [ControlServiceRequest_Action.REMOVE]: "remove",
  [ControlServiceRequest_Action.UPDATE]: "update",
  [ControlServiceRequest_Action.ROLLBACK]: "rollback",
};

const handlers: Record<string, Handler> = {
  // NodeService
  "NodeService/GetNodeInfo": (f, n, _i, addr) => {
    const me = needNode(n, addr);
    return {
      node: {
        nodeId: me.nodeId,
        name: me.name,
        fleetId: f.records.find((r) => r.kind === "fleet")?.id ?? "",
        version: "0.9.3",
        uptimeSeconds: BigInt(Math.max(0, Math.floor((f.now - me.bootedUnixMs) / 1000))),
        devMode: false,
        peers: f.nodes.filter((p) => p !== me).map((p) => ({ nodeId: p.nodeId, name: p.name, address: p.addr, online: p.reachable, lastSeenUnixMs: BigInt(p.lastSeenUnixMs), queued: p.reachable ? 0 : 1 })),
        portMapping: me.portMapping,
        publicIp: me.publicIp,
        tailscale: me.letter === "A" ? { hostname: me.name.toLowerCase(), state: "Running", loginUrl: "", dnsName: `${me.name.toLowerCase()}.home-fleet.ts.net.`, addresses: ["100.64.0.5"], tailnet: "home-fleet.ts.net", error: "" } : undefined,
      },
    };
  },
  "NodeService/ListRecords": (f, n, i, addr) => {
    needNode(n, addr);
    const kind = (i.kind as string) || "";
    return { records: f.records.filter((r) => !kind || r.kind === kind) };
  },
  "NodeService/PutRecord": (f, n, i, addr) => {
    needNode(n, addr);
    const r = i.record as { kind: string; id: string; body?: Uint8Array; deleted?: boolean; signedUnixMs?: bigint };
    const owner = f.records.find((x) => x.kind === r.kind && x.id === r.id)?.ownerNodeId;
    const ownerNode = owner ? f.nodes.find((x) => x.nodeId === owner) : undefined;
    if (ownerNode && !ownerNode.reachable) return { record: { ...r, ownerNodeId: owner }, queued: true };
    try {
      return { record: f.putRecord(r), queued: false };
    } catch (e) {
      throw fail(Code.FailedPrecondition, (e as Error).message);
    }
  },
  "NodeService/GetNodeLogs": (f, n, i, addr) => {
    const me = needNode(n, addr);
    const sources = [
      { id: "agent", label: "Maître agent" },
      { id: "pool", label: "Storage pool" },
      { id: "caddy", label: "Web routes (Caddy)" },
      { id: "health", label: "Boot health checks" },
      { id: "kernel", label: "Kernel" },
      { id: "system", label: "Everything since boot" },
      ...f.services.filter((s) => s.spec.nodeId === me.nodeId).map((s) => ({ id: `service:${s.spec.name}`, label: s.spec.name })),
    ];
    const source = (i.source as string) || "agent";
    if (!sources.some((s) => s.id === source)) throw fail(Code.NotFound, `no log source "${source}" on this Node`);
    if (source.startsWith("service:")) return { lines: f.logs(source.slice(8), 120), sources };
    const host = me.name.toLowerCase();
    const msgs: Record<string, string[]> = {
      agent: ["peers: synced with basalt (3 records)", "gossip: pushed status to 1 peer", "fleet status: answered GetFleetStatus", "files: listed media/Documents", "rollout: automatic updates are off", "warn: peer cedar silent for 62s"],
      pool: ["mergerfs: media over /var/mnt/drives/sda/media:/var/mnt/drives/sdb/media", "nfs: exported media to 10.0.0.6"],
      caddy: ["route jellyfin.local -> 127.0.0.1:20001", "certificate for remote access renewed"],
      health: ["greenboot: all required checks passed", "boot marked healthy"],
      kernel: ["EXT4-fs (sdb1): mounted filesystem", "r8169 0000:03:00.0 enp3s0: Link is Up - 1Gbps/Full"],
      system: ["Started maitre-agent.service - Maître agent.", "podman[812]: jellyfin started", "systemd-resolved: Using degraded feature set UDP for DNS server 10.0.0.1"],
    };
    const base = f.now - 60_000;
    const lines = Array.from({ length: 80 }, (_, k) => `${new Date(base + k * 750).toISOString().slice(0, 19)} ${host} ${source === "kernel" ? "kernel" : source === "agent" ? "maitre" : source}[${400 + k}]: ${msgs[source][k % msgs[source].length]}`);
    return { lines, sources };
  },
  "NodeService/GetNodeMetrics": (f, n, _i, addr) => ({ metrics: f.metricsOf(needNode(n, addr)) }),
  "NodeService/GetFleetStatus": (f, n, _i, addr) => {
    const me = needNode(n, addr);
    const entry = (p: DemoNode) => ({
      nodeId: p.nodeId,
      reachable: p === me || (p.reachable && f.now - p.lastSeenUnixMs < 60_000),
      lastSeenUnixMs: BigInt(p.lastSeenUnixMs),
      metrics: f.metricsOf(p),
      metricsUnixMs: BigInt(p.metricsUnixMs),
      services: f.servicesOn(p.nodeId),
      servicesUnixMs: BigInt(f.services.filter((s) => s.spec.nodeId === p.nodeId).reduce((m, s) => Math.max(m, s.statusUnixMs), 0)),
      updateState: p.update.state,
      updateMessage: p.update.message,
      updateProgress: p.update.progress,
      updateStaged: !!p.update.staged && !p.update.rollbackQueued,
      lastRollback: p.update.lastRollback,
      updateAvailable: !!p.update.available,
      version: "0.9.3",
      problems: p === me ? [] : (p.problems ?? []),
      syncGaps: p === me ? f.syncGaps : [],
    });
    // The Installer's Node once its record is in: still being installed,
    // reported from the Installer as a real Node does.
    const installing = f.installer.provisioning && !f.nodes.some((p) => p.nodeId === f.installer.id) && f.records.some((r) => r.kind === "node" && r.id === f.installer.id && !r.deleted) ? [{ nodeId: f.installer.id, reachable: false, provisioning: installerProvisioning(f) }] : [];
    return { nodeId: me.nodeId, nodes: [entry(me), ...f.nodes.filter((p) => p !== me).map(entry), ...installing] };
  },
  "NodeService/GetUpdateStatus": (_f, n, _i, addr) => ({ status: needNode(n, addr).update }),
  "NodeService/UpdateImage": (f, n, i, addr) => {
    const me = needNode(n, addr);
    if (me.update.state === UpdateStatus_State.UPDATING) throw fail(Code.FailedPrecondition, "an update is already running");
    f.startUpdate(me.nodeId, i.image as string);
    if (i.reboot) f.reboot(me.nodeId);
    return {};
  },
  "NodeService/Rollback": (f, n, i, addr) => {
    const me = needNode(n, addr);
    me.update.rollbackQueued = true;
    if (i.reboot) f.reboot(me.nodeId);
    return {};
  },
  "NodeService/Reboot": (f, n, _i, addr) => {
    f.reboot(needNode(n, addr).nodeId);
    return {};
  },
  "NodeService/RenameNode": (f, n, i, addr) => {
    const me = needNode(n, addr);
    const name = (i.name as string).trim();
    if (!name) throw fail(Code.InvalidArgument, "a name is needed");
    if (f.nodes.some((x) => x !== me && x.name.toLowerCase() === name.toLowerCase())) throw fail(Code.AlreadyExists, `a Node named ${name} already exists`);
    f.rename(me.nodeId, name);
    return { node: { nodeId: me.nodeId, name, letter: me.letter, addresses: [me.addr] } };
  },
  "NodeService/PushStatus": (_f, n, _i, addr) => {
    needNode(n, addr);
    return {};
  },
  "NodeService/PushRecords": (f, n, i, addr) => {
    needNode(n, addr);
    for (const r of (i.records as { kind: string; id: string; body?: Uint8Array; deleted?: boolean }[]) ?? []) f.putRecord(r);
    return { applied: ((i.records as unknown[]) ?? []).length };
  },

  // ServiceService
  "ServiceService/GetCatalog": (f, n, _i, addr) => {
    needNode(n, addr);
    return { indexJson: new TextEncoder().encode(JSON.stringify(bundledCatalog)), source: "https://catalog.maitre.dev/index.json", fetchedUnixMs: BigInt(f.now - 3600_000) };
  },
  "ServiceService/ListServices": (f, n, _i, addr) => ({ services: f.servicesOn(needNode(n, addr).nodeId) }),
  "ServiceService/InstallService": (f, n, i, addr) => {
    const me = needNode(n, addr);
    const spec = i.spec as { name: string; quadlet: string; image: string };
    if (!f.services.some((s) => s.spec.name === spec.name)) needGrants(f, spec as never, me.nodeId);
    try {
      return { service: f.install(i.spec as never, me.nodeId) };
    } catch (e) {
      throw fail(Code.FailedPrecondition, (e as Error).message);
    }
  },
  "ServiceService/ControlService": (f, n, i, addr) => {
    needNode(n, addr);
    const a = actions[i.action as number];
    if (!a) throw fail(Code.InvalidArgument, "unknown action");
    const s = f.services.find((x) => x.spec.name === i.name);
    if (s && (a === "start" || a === "restart" || a === "update")) needGrants(f, s.spec, s.spec.nodeId);
    try {
      return { service: f.control(i.name as string, a) };
    } catch (e) {
      throw fail(Code.NotFound, (e as Error).message);
    }
  },
  "ServiceService/GetServiceLogs": (f, n, i, addr) => {
    needNode(n, addr);
    return { lines: f.logs(i.name as string, (i.lines as number) || 200) };
  },
  "ServiceService/SetExposure": (f, n, i, addr) => {
    needNode(n, addr);
    const s = f.services.find((x) => x.spec.name === i.name);
    if (!s) throw fail(Code.NotFound, `no Service ${i.name}`);
    s.spec.exposure = i.exposure as string;
    s.spec.publicHost = (i.publicHost as string) ?? "";
    f.rebuildRecords();
    return { service: f.status(s) };
  },
  "ServiceService/SetSettings": (f, n, i, addr) => {
    needNode(n, addr);
    const s = f.services.find((x) => x.spec.name === i.name);
    if (!s) throw fail(Code.NotFound, `no Service ${i.name}`);
    const settings = (i.settings as Record<string, string>) ?? {};
    for (const [k, v] of Object.entries(settings)) {
      if (!/^[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(k)) throw fail(Code.InvalidArgument, `setting name "${k}": use letters, digits and underscores`);
      if (/[\n\r\0]/.test(v)) throw fail(Code.InvalidArgument, `setting ${k} has a line break`);
    }
    // As the Node renders the unit: a setting the template needs must be there.
    for (const m of s.spec.quadlet.matchAll(/\.Settings\.(\w+)/g)) if (!(m[1] in settings)) throw fail(Code.InvalidArgument, `container definition: map has no entry for key "${m[1]}"`);
    s.spec.settings = settings;
    if (i.autoUpdate !== undefined) s.spec.autoUpdate = i.autoUpdate as boolean;
    const running = s.state === ServiceStatus_State.RUNNING || s.state === ServiceStatus_State.STARTING;
    f.rebuildRecords();
    return { service: running ? f.control(s.spec.name, "restart") : f.status(s) };
  },
  "ServiceService/SetImage": (f, n, i, addr) => {
    needNode(n, addr);
    const s = f.services.find((x) => x.spec.name === i.name);
    if (!s) throw fail(Code.NotFound, `no Service ${i.name}`);
    const image = String(i.image ?? "").trim();
    if (!/^[a-z0-9]+([._-][a-z0-9]+)*(\.[a-z0-9-]+)*(:[0-9]{1,5})?(\/[a-z0-9]+([._-]+[a-z0-9]+)*)+(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(@sha256:[a-f0-9]{64})?$/.test(image)) throw fail(Code.InvalidArgument, `"${image}" is not an image reference such as docker.io/library/nginx:1.27`);
    // As the Node pins a tag to the digest it pulls.
    const pinned = image.includes("@") ? image : `${image}@sha256:${[...image].reduce((h, c) => (h * 31 + c.charCodeAt(0)) >>> 0, 7).toString(16).padStart(8, "0").repeat(8)}`;
    if (pinned !== s.spec.image) {
      s.spec.previousImage = s.spec.image;
      s.spec.image = pinned;
    }
    const running = s.state === ServiceStatus_State.RUNNING || s.state === ServiceStatus_State.STARTING;
    f.rebuildRecords();
    return { service: running ? f.control(s.spec.name, "restart") : f.status(s) };
  },
  "ServiceService/SetFolders": (f, n, i, addr) => {
    needNode(n, addr);
    const s = f.services.find((x) => x.spec.name === i.name);
    if (!s) throw fail(Code.NotFound, `no Service ${i.name}`);
    const folders = (i.volumeFolders as Record<string, string>) ?? {};
    const mounts = (i.mounts as { spaceId: string; folder: string; path: string; readOnly: boolean }[]) ?? [];
    const used = new Set(s.spec.volumes.map((v) => v.path));
    for (const m of mounts) {
      if (!m.path?.startsWith("/") || used.has(m.path)) throw fail(Code.InvalidArgument, `${m.path || "a folder"}: not a free path inside the container`);
      used.add(m.path);
    }
    s.spec.volumeSpaces = { ...s.spec.volumeSpaces, ...((i.volumeSpaces as Record<string, string>) ?? {}) };
    s.spec.volumeFolders = folders;
    s.spec.mounts = mounts.map((m) => ({ ...m, $typeName: "maitre.v1.ServiceMount" })) as never;
    f.rebuildRecords();
    return { service: f.status(s) };
  },
  "ServiceService/SetPortForwarding": (f, n, i, addr) => {
    needNode(n, addr);
    const s = f.services.find((x) => x.spec.name === i.name);
    if (!s) throw fail(Code.NotFound, `no Service ${i.name}`);
    s.spec.forwardPorts = !!i.enabled;
    s.forwardState = "";
    f.rebuildRecords();
    return { service: f.status(s) };
  },

  // StorageService
  "StorageService/ListDrives": (f, n, _i, addr) => {
    const me = needNode(n, addr);
    return { drives: f.drives.filter((d) => d.nodeId === me.nodeId).map(({ nodeId: _n, ...d }) => d) };
  },
  "StorageService/DriveContents": () => ({ entries: ["Downloads", "old-backups"], usedBytes: 130n * 1024n ** 3n, empty: false }),
  "StorageService/AdoptDrive": () => {
    throw fail(Code.Unimplemented, "demo: no unadopted Drives on this Node");
  },
  "StorageService/FormatDrive": () => {
    throw fail(Code.Unimplemented, "demo: no unadopted Drives on this Node");
  },
  "StorageService/SetDriveTags": (f, n, i, addr) => {
    const me = needNode(n, addr);
    const d = f.drives.find((x) => x.nodeId === me.nodeId && x.driveId === i.driveId);
    if (!d?.record) throw fail(Code.NotFound, "no such Drive");
    d.record.tags = (i.tags as string[]) ?? [];
    if (i.name) d.record.name = i.name as string;
    f.rebuildRecords();
    const { nodeId: _n, ...out } = d;
    return { drive: out };
  },
  "StorageService/ForgetDrive": (f, n, i, addr) => {
    const me = needNode(n, addr);
    const d = f.drives.find((x) => x.nodeId === me.nodeId && x.driveId === i.driveId);
    if (!d) throw fail(Code.NotFound, "no such Drive");
    if (d.record?.origin === "internal") throw fail(Code.FailedPrecondition, "the built-in Drive cannot be forgotten");
    f.drives = f.drives.filter((x) => x !== d);
    f.rebuildRecords();
    return {};
  },

  // FilesService
  "FilesService/ListDir": (f, n, i, addr) => {
    const me = needNode(n, addr);
    try {
      return { entries: f.listDir((i.scope as { spaceId?: string; driveId?: string }) ?? {}, me.nodeId, (i.path as string) ?? "") };
    } catch (e) {
      throw fail(Code.NotFound, (e as Error).message);
    }
  },
  "FilesService/Usage": (f, n, i, addr) => {
    const me = needNode(n, addr);
    return { ...f.usage((i.scope as { spaceId?: string; driveId?: string }) ?? {}, me.nodeId, (i.path as string) ?? ""), inProgress: false };
  },
  "FilesService/MakeDir": (f, n, i, addr) => {
    const me = needNode(n, addr);
    try {
      f.makeDir((i.scope as { spaceId?: string; driveId?: string }) ?? {}, me.nodeId, (i.path as string) ?? "");
      return {};
    } catch (e) {
      throw fail((e as { exists?: boolean }).exists ? Code.AlreadyExists : Code.InvalidArgument, (e as Error).message);
    }
  },
  "FilesService/Zip": (f, n, i, addr) => {
    const me = needNode(n, addr);
    const scope = (i.scope as { spaceId?: string; driveId?: string }) ?? {};
    const names = (i.names as string[]) ?? [];
    if (names.length === 0) throw fail(Code.InvalidArgument, "pick something to zip");
    const want = ((i.name as string) || (names.length === 1 ? names[0] : "Archive")).replace(/\.zip$/i, "");
    const name = f.freeName(scope, me.nodeId, (i.folder as string) ?? "", want, ".zip");
    return { jobId: f.archiveJob("zip", scope, me.nodeId, (i.folder as string) ?? "", names, name), name };
  },
  "FilesService/Unzip": (f, n, i, addr) => {
    const me = needNode(n, addr);
    const scope = (i.scope as { spaceId?: string; driveId?: string }) ?? {};
    const path = (i.path as string) ?? "";
    const parts = path.split("/");
    const file = parts.pop() ?? "";
    const stem = file.replace(/(\.tar\.gz|\.tgz|\.tar\.bz2|\.tbz2|\.tar|\.zip)$/i, "");
    if (stem === file) throw fail(Code.InvalidArgument, `${file}: only zip, tar, tar.gz and tar.bz2 unpack here`);
    const folder = f.freeName(scope, me.nodeId, parts.join("/"), stem, "");
    return { jobId: f.archiveJob("unzip", scope, me.nodeId, parts.join("/"), [file], folder), folder };
  },
  "FilesService/Delete": (f, n, i, addr) => {
    const me = needNode(n, addr);
    const names = (i.names as string[]) ?? [];
    if (names.length === 0) throw fail(Code.InvalidArgument, "pick something to delete");
    const folder = (i.folder as string) ?? "";
    const scope = (i.scope as { spaceId?: string; driveId?: string }) ?? {};
    // Like the agent: a folder a Service uses stays.
    for (const n of names) {
      const p = [folder, n].filter(Boolean).join("/");
      for (const s of f.services) {
        const used = s.spec.volumes.map((v) => (v.name in s.spec.volumeFolders ? { space: s.spec.volumeSpaces[v.name], dir: s.spec.volumeFolders[v.name] } : { space: s.spec.volumeSpaces[v.name], dir: volumeDir(s.spec.name, v.name) }));
        const hit = used.find((u) => u.space === scope.spaceId && (u.dir === p || u.dir.startsWith(p + "/")));
        if (hit) throw fail(Code.FailedPrecondition, `${s.spec.name} uses ${[scope.spaceId, hit.dir].filter(Boolean).join("/")}; change its folders first`);
      }
    }
    return { jobId: f.deleteJob(scope, me.nodeId, folder, names) };
  },
  "FilesService/Move": (f, n, i, addr) => ({ jobId: f.move(i as never, needNode(n, addr).nodeId) }),
  "FilesService/ListJobs": (f, n, _i, addr) => {
    const me = needNode(n, addr);
    return { jobs: f.jobs.filter((j) => j.nodeId === me.nodeId).map(({ nodeId: _n, onDone: _d, ...j }) => j) };
  },
  "ServiceService/MoveVolume": (f, n, i, addr) => {
    const me = needNode(n, addr);
    try {
      return { jobId: f.moveVolume(i.name as string, i.volume as string, i.toSpace as string, me.nodeId) };
    } catch (e) {
      throw fail(Code.FailedPrecondition, (e as Error).message);
    }
  },
  "FilesService/CancelJob": (f, n, i, addr) => {
    needNode(n, addr);
    f.cancelJob(i.jobId as string);
    return {};
  },

  // InstallerService (the Installer on the LAN, at its own address)
  "InstallerService/GetInstallerStatus": (f, _n, _i, addr) => {
    const ins = f.installer;
    if (addr !== ins.addr) throw fail(Code.Unavailable, `demo: nothing answers at ${addr}`);
    return {
      status: {
        installerId: ins.id,
        version: "0.9.3",
        addresses: [ins.addr],
        pairingCode: ins.pairingCode,
        disks: [
          { path: "/dev/nvme0n1", model: "WD Blue SN580 1TB", sizeBytes: 1000n * 1000n ** 3n, removable: false, isInstallerMedia: false },
          { path: "/dev/sda", model: "SanDisk Ultra USB", sizeBytes: 32n * 1000n ** 3n, removable: true, isInstallerMedia: true },
        ],
        provisioning: installerProvisioning(f),
        machineName: ins.machine,
        image: "ghcr.io/onfire7/maitre-server:stable",
        source: "ghcr.io",
        insecureRegistry: "",
        backups: [],
      },
    };
  },
  "InstallerService/Provision": (f, _n, _i, addr) => {
    const ins = f.installer;
    if (addr !== ins.addr) throw fail(Code.Unavailable, `demo: nothing answers at ${addr}`);
    ins.provisioning = true;
    ins.progress = 0;
    ins.log = ["partitioning"];
    return {};
  },

  // RecoveryService
  "RecoveryService/Authorize": () => ({ nodeOnly: false }),
};

function installerProvisioning(f: DemoFleet) {
  const ins = f.installer;
  const phase = ins.provisioning ? (ins.progress < 0.7 ? Provisioning_Phase.INSTALLING : ins.progress < 1 ? Provisioning_Phase.CONFIGURING : Provisioning_Phase.REBOOTING) : Provisioning_Phase.IDLE;
  return { phase, message: ins.log.at(-1) ?? "", percent: Math.round(ins.progress * 100) };
}

/** A Transport answering as the demo machine at `addr`. */
export function createDemoTransport(addr: string, fleet: DemoFleet = demoFleet()): Transport {
  return {
    async unary<I extends DescMessage, O extends DescMessage>(method: DescMethodUnary<I, O>, signal: AbortSignal | undefined, _timeoutMs: number | undefined, _header: HeadersInit | undefined, input: MessageInitShape<I>): Promise<UnaryResponse<I, O>> {
      // A little latency so loading states are visible; none in tests.
      if (typeof window !== "undefined" && !("__vitest_worker__" in globalThis)) await new Promise((r) => setTimeout(r, 40 + Math.random() * 80));
      if (signal?.aborted) throw fail(Code.Canceled, "cancelled");
      const key = `${method.parent.name}/${method.name}`;
      const h = handlers[key];
      if (!h) throw fail(Code.Unimplemented, `demo: ${key} is not scripted`);
      const node = fleet.nodeByAddr(addr);
      const out = h(fleet, node, create(method.input, input) as unknown as Record<string, unknown>, addr) as MessageInitShape<O>;
      return { stream: false, service: method.parent, method, header: new Headers(), trailer: new Headers(), message: create(method.output, out) as MessageShape<O> };
    },
    async stream() {
      throw fail(Code.Unimplemented, "demo: no streaming RPCs");
    },
  };
}
