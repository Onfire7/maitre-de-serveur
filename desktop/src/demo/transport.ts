// A Connect Transport that answers from the demo Fleet (decision 153). The
// target address picks the Node (or the Installer) being asked, so the
// anchor logic, unreachable Nodes and per-Node calls behave as they would
// against real machines. Every RPC the Console uses is here; an unknown one
// fails with Unimplemented so a prototype never silently gets empty data.
import { volumeDir } from "@/files/layout";
import { create, fromBinary, type DescMessage, type DescMethodUnary, type MessageInitShape, type MessageShape } from "@bufbuild/protobuf";
import { Code, ConnectError, type Transport, type UnaryResponse } from "@connectrpc/connect";
import { ControlServiceRequest_Action, ServiceSpecSchema, ServiceStatus_State, type ServiceSpec } from "@/gen/maitre/v1/service_pb";
import { FleetDomainSchema, FleetSettingsSchema, GrantsNeededSchema, type EntryNode } from "@/gen/maitre/v1/fleet_pb";
import { Provisioning_Phase } from "@/gen/maitre/v1/installer_pb";
import { UpdateStatus_State } from "@/gen/maitre/v1/node_pb";
import bundledCatalog from "@/catalog/index.json";
import { type DemoFleet, type DemoNode, demoFleet, NODE_A, protectedOptions, renderDemo } from "./fleet";
import { demoHistory } from "./metrics";
import { cancelDrain, checkParity, copiesStatuses, drainsOf, makeMirrored, mirrorStatuses, parityStatus, planDrain, protectNow, replaceDrive, setParity, startDrain } from "./protection";

type Handler = (fleet: DemoFleet, node: DemoNode | undefined, input: Record<string, unknown>, addr: string) => unknown;

const fail = (code: Code, msg: string) => new ConnectError(msg, code);

function needNode(node: DemoNode | undefined, addr: string): DemoNode {
  if (!node) throw fail(Code.Unavailable, `demo: nothing answers at ${addr}`);
  if (!node.reachable) throw fail(Code.Unavailable, `dial tcp ${node.addr}: no route to host`);
  return node;
}

/** FailedPrecondition with a GrantsNeeded detail, as a Node refuses an ungranted option (decision 189). */
/** A Setup as the demo reads it off the wire. */
interface DemoSetup {
  networkKind?: string;
  networkTarget?: string;
  fleetReach?: boolean;
  ports?: string[];
  address?: string;
  networkInternal?: boolean;
  pages?: { name: string; label?: string; component?: string; port?: number; path?: string; exposure?: string; publicHost?: string; healthHttp?: string; hostPort?: number }[];
  overrides?: { component?: string; key: string; value?: string; remove?: boolean }[];
}

/** The spec a Setup would leave, checked as the Node checks it (ADR 0010). */
function withSetup(f: DemoFleet, cur: ServiceSpec, setup: DemoSetup): ServiceSpec {
  const kind = setup.networkKind ?? "";
  const target = setup.networkTarget ?? "";
  if (cur.tunnel && kind !== "") throw fail(Code.InvalidArgument, `${cur.name} is a Tunnel; it stays on its own network`);
  if (kind === "tunnel" || kind === "service") {
    const t = f.services.find((x) => x.spec.name === target);
    if (!t) throw fail(Code.InvalidArgument, `there is no Service named ${target}`);
    if (t.spec.nodeId !== cur.nodeId) throw fail(Code.InvalidArgument, `${target} runs on another Node; a Service joins only a Service of its own Node`);
    if (kind === "tunnel" && !t.spec.tunnel) throw fail(Code.InvalidArgument, `${target} is not a Tunnel`);
    if (kind === "service" && t.spec.components.length === 0) throw fail(Code.InvalidArgument, `${target} has no network of its own: it is already reachable as ${target} on the shared network`);
  } else if (target) throw fail(Code.InvalidArgument, "a Service on its own network names no other");
  for (const p of setup.ports ?? []) if (!/^\d{1,5}\/(tcp|udp)$/.test(p)) throw fail(Code.InvalidArgument, `port "${p}": write it as 5432/tcp or 1194/udp`);
  const addr = setup.address ?? "";
  if (addr && !/^10\.90\.\d{1,3}\.\d{1,3}$/.test(addr)) throw fail(Code.InvalidArgument, `"${addr}" is not an address of the Fleet's network 10.90.0.0/16`);
  if (addr && f.services.some((x) => x.spec.name !== cur.name && x.spec.address === addr)) throw fail(Code.InvalidArgument, `${f.services.find((x) => x.spec.name !== cur.name && x.spec.address === addr)!.spec.name} already has the address ${addr}`);
  const seen = new Set<string>();
  const had = new Map(cur.pages.map((p) => [p.name, p.hostPort]));
  const pages = (setup.pages ?? []).map((p, i) => {
    if (!/^[a-z0-9][a-z0-9-]{0,40}$/.test(p.name)) throw fail(Code.InvalidArgument, `page name "${p.name}": use lowercase letters, digits and dashes`);
    if (seen.has(p.name)) throw fail(Code.InvalidArgument, `two pages named "${p.name}"`);
    seen.add(p.name);
    if (!p.port || p.port > 65535) throw fail(Code.InvalidArgument, `page ${p.name}: port ${p.port ?? 0} is out of range`);
    return { ...p, hostPort: had.get(p.name) ?? 20090 + i, $typeName: "maitre.v1.ServicePage" as const };
  });
  for (const o of setup.overrides ?? []) if (!/^[A-Za-z][A-Za-z0-9]{0,40}$/.test(o.key)) throw fail(Code.InvalidArgument, `option "${o.key}": a [Container] key is letters and digits`);
  return create(ServiceSpecSchema, {
    ...cur,
    $typeName: undefined,
    networkKind: kind,
    networkTarget: target,
    fleetReach: !!setup.fleetReach,
    ports: setup.ports ?? [],
    address: addr || cur.address,
    networkInternal: !!setup.networkInternal,
    pages,
    webPort: pages[0]?.port ?? 0,
    webPath: pages[0]?.path ?? "",
    webComponent: pages[0]?.component ?? "",
    webHostPort: pages[0]?.hostPort ?? 0,
    overrides: (setup.overrides ?? []).map((o) => ({ component: o.component ?? "", key: o.key, value: o.value ?? "", remove: !!o.remove })),
  } as never);
}

/** The Services a Setup restarts: this one, the Tunnel it joins or
 * leaves, and that Tunnel's other Services. */
function restartsFor(f: DemoFleet, cur: ServiceSpec, next: ServiceSpec): string[] {
  const out = new Set<string>([next.name]);
  for (const t of [cur.networkTarget, next.networkTarget]) {
    if (!t || !f.services.some((x) => x.spec.name === t && x.spec.tunnel)) continue;
    out.add(t);
    for (const d of f.services) if (d.spec.networkKind === "tunnel" && d.spec.networkTarget === t) out.add(d.spec.name);
  }
  if (next.tunnel) for (const d of f.services) if (d.spec.networkKind === "tunnel" && d.spec.networkTarget === next.name) out.add(d.spec.name);
  const [first, ...rest] = [...out];
  return [first, ...rest.sort()];
}

/** The protected options of a spec's rendered units, as the Node lists
 * them: of the bundle's own lines and the person's overrides, never of
 * the lines the agent adds itself, and never of the folders Maître
 * mounts for the Service (decision 189, ADR 0010). */
function protectedFor(f: DemoFleet, spec: ServiceSpec) {
  const repo = (x: string) => x.replace(/@.*$/, "").replace(/:[^/:]*$/, "");
  const parts = spec.components.length ? spec.components.map((c) => ({ name: c.name, image: c.image })) : [{ name: "", image: spec.image }];
  const managed = `/var/mnt/pools/`;
  return f.unitsOf(spec).flatMap((u) => {
    const part = parts.find((p) => u.name === `${p.name ? `${spec.name}-${p.name}` : spec.name}.container`);
    if (!part) return [];
    const body = u.body
      .split("\n")
      .filter((l, i) => u.sources[i] !== "agent" || /^\[\w+\]$/.test(l.trim()))
      .join("\n");
    return protectedOptions(body)
      .filter((o) => !o.startsWith(`Volume=${managed}`))
      .map((option) => ({ component: part.name, imageRepository: repo(part.image), option }));
  });
}

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
        features: ["components"],
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
      ...f.services.filter((s) => s.spec.nodeId === me.nodeId).flatMap((s) => [{ id: `service:${s.spec.name}`, label: s.spec.name }, ...s.spec.components.map((c) => ({ id: `service:${s.spec.name}/${c.name}`, label: `${s.spec.name}: ${c.name}` }))]),
    ];
    const source = (i.source as string) || "agent";
    if (!sources.some((s) => s.id === source)) throw fail(Code.NotFound, `no log source "${source}" on this Node`);
    if (source.startsWith("service:")) {
      const [name, component = ""] = source.slice(8).split("/");
      return { lines: f.logs(name, 120, component), sources };
    }
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
  "NodeService/GetMetricsHistory": (f, n, i, addr) => demoHistory(f, needNode(n, addr), i),
  // A Service's history moves with it (decision 210): handed over as made-up files.
  "NodeService/ExportMetrics": (f, n, i, addr) => {
    const me = needNode(n, addr);
    if (!f.services.some((s) => s.spec.name === i.service)) return { files: {} };
    return { files: Object.fromEntries(["cpu", "memory", "disk_read", "disk_write"].map((k) => [k, new TextEncoder().encode(`${i.service}/${k} from ${me.name}`)])) };
  },
  "NodeService/ImportMetrics": (_f, n, _i, addr) => {
    needNode(n, addr);
    return {};
  },
  "NodeService/GetFleetStatus": (f, n, _i, addr) => {
    const fleetZone = (f: DemoFleet) => {
      const r = f.records.find((x) => x.kind === "fleet_settings" && !x.deleted);
      return (r && fromBinary(FleetSettingsSchema, r.body).timeZone) || "UTC";
    };
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
      entries: demoEntries(f, p),
      parity: parityStatus(f, p),
      drains: drainsOf(f, p.nodeId).filter((j) => j.state === "queued" || j.state === "running"),
      copies: copiesStatuses(f, p),
      mirrors: mirrorStatuses(f, p),
      // Decision 282: every demo Node has taken the Fleet's zone; UTC when none is set.
      timeZone: fleetZone(f),
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
    return { lines: f.logs(i.name as string, (i.lines as number) || 200, (i.component as string) || "") };
  },
  "ServiceService/SetExposure": (f, n, i, addr) => {
    needNode(n, addr);
    const s = f.services.find((x) => x.spec.name === i.name);
    if (!s) throw fail(Code.NotFound, `no Service ${i.name}`);
    // A Page named changes that Page's level; otherwise the Service's,
    // which every Page without one of its own follows (ADR 0010).
    const page = (i.page as string) ?? "";
    if (page) {
      const p = s.spec.pages.find((x) => x.name === page);
      if (!p) throw fail(Code.NotFound, `${s.spec.name} has no Page "${page}"`);
      p.exposure = i.exposure as string;
      p.publicHost = (i.publicHost as string) ?? "";
    } else {
      s.spec.exposure = i.exposure as string;
      s.spec.publicHost = (i.publicHost as string) ?? "";
    }
    f.rebuildRecords();
    return { service: f.status(s) };
  },
  // ADR 0010: what the Node would write with a Setup, and applying one.
  "ServiceService/PreviewSetup": (f, n, i, addr) => {
    needNode(n, addr);
    const s = f.services.find((x) => x.spec.name === i.name);
    if (!s) throw fail(Code.NotFound, `no Service ${i.name}`);
    const setup = i.setup as DemoSetup | undefined;
    const spec = setup ? withSetup(f, s.spec, setup) : s.spec;
    return { units: f.unitsOf(spec), protected: protectedFor(f, spec), restarts: setup ? restartsFor(f, s.spec, spec) : [] };
  },
  "ServiceService/SetSetup": (f, n, i, addr) => {
    needNode(n, addr);
    const s = f.services.find((x) => x.spec.name === i.name);
    if (!s) throw fail(Code.NotFound, `no Service ${i.name}`);
    const setup = i.setup as DemoSetup | undefined;
    if (!setup) throw fail(Code.InvalidArgument, "no setup");
    const next = withSetup(f, s.spec, setup);
    needGrants(f, next, s.spec.nodeId);
    s.spec = next;
    const running = s.state === ServiceStatus_State.RUNNING || s.state === ServiceStatus_State.STARTING;
    f.rebuildRecords();
    return { service: running ? f.control(s.spec.name, "restart") : f.status(s) };
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
  // Plan C10: the Node's check of a spec, without installing it.
  "ServiceService/CheckService": (_f, n, i, addr) => {
    needNode(n, addr);
    const spec = i.spec as ServiceSpec;
    const parts = spec.components?.length ? spec.components.map((c) => ({ name: c.name, image: c.image, quadlet: c.quadlet })) : [{ name: "", image: spec.image, quadlet: spec.quadlet }];
    if (parts.some((p) => !p.quadlet.includes("[Container]"))) throw fail(Code.InvalidArgument, `${spec.name}: Quadlet rejected the unit: no [Container] group`);
    return {
      units: Object.fromEntries(parts.map((p) => [`${p.name ? `${spec.name}-${p.name}` : spec.name}.container`, p.quadlet])),
      protected: parts.flatMap((p) => protectedOptions(renderDemo(p.quadlet, spec)).map((option) => ({ component: p.name, imageRepository: p.image.replace(/@.*$/, "").replace(/:[^/:]*$/, ""), option }))),
    };
  },
  // G5: Node A has a Zigbee stick and a GPU plugged in; the others nothing.
  "ServiceService/ListDevices": (_f, n, i, addr) => {
    const node = needNode(n, addr);
    const all = node.nodeId === NODE_A ? [
      { path: "/dev/serial/by-id/usb-ITead_Sonoff_Zigbee_3.0_USB_Dongle_Plus_4c004e-if00-port0", kind: "serial", name: "usb-ITead_Sonoff_Zigbee_3.0_USB_Dongle_Plus_4c004e-if00-port0" },
      { path: "/dev/dri/renderD128", kind: "video", name: "GPU 128" },
    ] : [];
    const kind = (i as { kind?: string }).kind ?? "";
    return { devices: all.filter((d) => !kind || d.kind === kind) };
  },
  "ServiceService/SetImage": (f, n, i, addr) => {
    needNode(n, addr);
    const s = f.services.find((x) => x.spec.name === i.name);
    if (!s) throw fail(Code.NotFound, `no Service ${i.name}`);
    const image = String(i.image ?? "").trim();
    if (!/^[a-z0-9]+([._-][a-z0-9]+)*(\.[a-z0-9-]+)*(:[0-9]{1,5})?(\/[a-z0-9]+([._-]+[a-z0-9]+)*)+(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(@sha256:[a-f0-9]{64})?$/.test(image)) throw fail(Code.InvalidArgument, `"${image}" is not an image reference such as docker.io/library/nginx:1.27`);
    // As the Node pins a tag to the digest it pulls.
    const pinned = image.includes("@") ? image : `${image}@sha256:${[...image].reduce((h, c) => (h * 31 + c.charCodeAt(0)) >>> 0, 7).toString(16).padStart(8, "0").repeat(8)}`;
    // One component, named or else every one on the same repository (plan C1).
    const repo = (x: string) => x.replace(/@.*$/, "").replace(/:[^/:]*$/, "");
    let holders: { image: string; previousImage: string }[] = [s.spec];
    if (s.spec.components.length) {
      holders = i.component ? s.spec.components.filter((c) => c.name === i.component) : s.spec.components.filter((c) => repo(c.image) === repo(pinned));
      if (i.component && holders.length === 0) throw fail(Code.InvalidArgument, `${s.spec.name} has no component "${i.component}"`);
      if (holders.length === 0) holders = s.spec.components;
    }
    for (const h of holders) {
      if (pinned !== h.image) {
        h.previousImage = h.image;
        h.image = pinned;
      }
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
    return { drives: f.drives.filter((d) => d.nodeId === me.nodeId && !d.missing).map(({ nodeId: _n, missing: _m, ...d }) => d) };
  },
  // ADR 0009: Drain, Parity Drives, Replace, Mirrored Drives, copies.
  "StorageService/PlanDrain": (f, n, i, addr) => ({ plan: planDrain(f, needNode(n, addr), i.driveId as string, (i.looseToSpace as string) ?? "") }),
  "StorageService/DrainDrive": (f, n, i, addr) => ({ job: startDrain(f, needNode(n, addr), i.driveId as string, (i.looseToSpace as string) ?? "", (i.confirm as string) ?? "", (i.purpose as string) ?? "") }),
  "StorageService/ListDrains": (f, n, _i, addr) => ({ jobs: drainsOf(f, needNode(n, addr).nodeId) }),
  "StorageService/CancelDrain": (f, n, i, addr) => (needNode(n, addr), cancelDrain(f, i.jobId as string), {}),
  "StorageService/CheckParityDrive": (f, n, i, addr) => checkParity(f, needNode(n, addr), (i.driveId as string) ?? "", (i.device as string) ?? "", (i.looseToSpace as string) ?? ""),
  "StorageService/SetParityDrive": (f, n, i, addr) => setParity(f, needNode(n, addr), i as Parameters<typeof setParity>[2]),
  "StorageService/ProtectNow": (f, n, _i, addr) => (protectNow(f, needNode(n, addr)), {}),
  "StorageService/ReplaceDrive": (f, n, i, addr) => ({ drive: replaceDrive(f, needNode(n, addr), i.failedDriveId as string, i.device as string, (i.confirm as string) ?? "") }),
  "StorageService/GetParityStatus": (f, n, _i, addr) => ({ status: parityStatus(f, needNode(n, addr)) }),
  "StorageService/MakeMirroredDrive": (f, n, i, addr) => ({ drive: makeMirrored(f, needNode(n, addr), (i.devices as string[]) ?? [], (i.name as string) ?? "", (i.tags as string[]) ?? [], (i.confirm as string) ?? "") }),
  "StorageService/ReplaceMirrorDisk": () => {
    throw fail(Code.FailedPrecondition, "demo: both disks of every Mirrored Drive are present");
  },
  "StorageService/CopiesNow": (_f, n, _i, addr) => (needNode(n, addr), {}),
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
        disks: DEMO_DISKS,
        provisioning: installerProvisioning(f),
        machineName: ins.machine,
        image: "ghcr.io/onfire7/maitre-server:stable",
        source: "ghcr.io",
        insecureRegistry: "",
        backups: [],
      },
    };
  },
  "InstallerService/ListFolder": (f, _n, i, addr) => {
    if (addr !== f.installer.addr) throw fail(Code.Unavailable, `demo: nothing answers at ${addr}`);
    const part = DEMO_DISKS.flatMap((d) => d.partitions).find((p) => p.device === i.device);
    if (!part?.browsable) throw fail(Code.FailedPrecondition, `${i.device as string} holds no filesystem this Installer can open`);
    const path = ((i.path as string) ?? "").split("/").filter(Boolean).join("/");
    const names = DEMO_FOLDERS[`${part.device}:${path}`] ?? [];
    const when = BigInt(Date.parse("2025-08-14T18:20:00Z"));
    return {
      entries: names.map((n) => (n.endsWith("/") ? { name: n.slice(0, -1), dir: true, modifiedUnixMs: when } : { name: n.slice(0, n.lastIndexOf(" ")), sizeBytes: BigInt(n.slice(n.lastIndexOf(" ") + 1)), modifiedUnixMs: when })),
      totalBytes: part.sizeBytes,
      usedBytes: part.sizeBytes / 3n,
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

  "InstallerService/GetLogs": (f, _n, _i, addr) => {
    const ins = f.installer;
    if (addr !== ins.addr) throw fail(Code.Unavailable, `demo: nothing answers at ${addr}`);
    return { lines: ['time=2026-10-31T19:26:12Z level=INFO msg="maitre installer" installer_id=' + ins.id, "time=2026-10-31T19:26:12Z level=INFO msg=listening addr=0.0.0.0:7443", ...ins.log.map((l) => `time=2026-10-31T19:27:00Z level=INFO msg="bootc" out="${l}"`)] };
  },
  "InstallerService/Restart": (f, _n, i, addr) => {
    const ins = f.installer;
    if (addr !== ins.addr) throw fail(Code.Unavailable, `demo: nothing answers at ${addr}`);
    if (ins.provisioning && !i.force) throw fail(Code.FailedPrecondition, "installing; the machine restarts by itself when done");
    ins.provisioning = false;
    ins.progress = 0;
    ins.log = [];
    return {};
  },

  // RecoveryService
  "RecoveryService/Authorize": () => ({ nodeOnly: false }),
};

// The demo machine's disks (decision 262): an SSD that held Windows, a
// media disk and the installer stick.
const GB = 1000n ** 3n;
const DEMO_DISKS = [
  {
    path: "/dev/nvme0n1",
    model: "WD Blue SN580 1TB",
    sizeBytes: 1000n * GB,
    removable: false,
    isInstallerMedia: false,
    partitions: [
      { device: "/dev/nvme0n1p1", sizeBytes: 100n * 1000n ** 2n, fstype: "vfat", label: "EFI system partition", browsable: true },
      { device: "/dev/nvme0n1p2", sizeBytes: 16n * 1000n ** 2n, fstype: "", label: "Microsoft reserved partition", browsable: false },
      { device: "/dev/nvme0n1p3", sizeBytes: 999n * GB, fstype: "ntfs", label: "Windows", browsable: true },
    ],
  },
  { path: "/dev/sdb", model: "Seagate IronWolf 4TB", sizeBytes: 4000n * GB, removable: false, isInstallerMedia: false, partitions: [{ device: "/dev/sdb1", sizeBytes: 4000n * GB, fstype: "ext4", label: "Media", browsable: true }] },
  { path: "/dev/sda", model: "SanDisk Ultra USB", sizeBytes: 32n * GB, removable: true, isInstallerMedia: true, partitions: [{ device: "/dev/sda1", sizeBytes: 2n * GB, fstype: "vfat", label: "MAITRELIVE", browsable: false }] },
];

/** Folders by "<device>:<path>"; "name/" is a folder, "name <bytes>" a file. */
const DEMO_FOLDERS: Record<string, string[]> = {
  "/dev/nvme0n1p1:": ["EFI/"],
  "/dev/nvme0n1p1:EFI": ["Boot/", "Microsoft/"],
  "/dev/nvme0n1p3:": ["Program Files/", "Users/", "Windows/", "hiberfil.sys 6871947673", "pagefile.sys 8589934592"],
  "/dev/nvme0n1p3:Users": ["Public/", "sam/"],
  "/dev/nvme0n1p3:Users/sam": ["Desktop/", "Documents/", "Pictures/", "NTUSER.DAT 3407872"],
  "/dev/nvme0n1p3:Users/sam/Pictures": ["Holiday 2025/", "IMG_0412.jpg 4194304", "IMG_0413.jpg 3984588"],
  "/dev/sdb1:": ["Films/", "Music/", "Photos/", "lost+found/", "backup-2024.tar 21474836480"],
  "/dev/sdb1:Films": ["Arrival (2016).mkv 8053063680", "Paddington 2 (2017).mkv 6442450944"],
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

// Decision 218 in the demo: the Node elected for the app and the web, as
// the Nodes elect it (pinned while reachable, else the least busy in steps
// of 20%, then by letter), with its router's answer.
function demoEntries(f: DemoFleet, p: DemoNode) {
  const rec = f.records.find((r) => r.kind === "fleet_domain" && !r.deleted);
  if (!rec) return [];
  const d = fromBinary(FleetDomainSchema, rec.body);
  const up = f.nodes.filter((n) => n.reachable);
  const busy = (n: DemoNode) => Math.floor(f.metricsOf(n).cpuPercent / 20);
  const elect = (e: EntryNode | undefined) => {
    if (!e?.enabled || up.length === 0) return "";
    if (up.some((n) => n.nodeId === e.nodeId)) return e.nodeId;
    return [...up].sort((a, b) => busy(a) - busy(b) || a.letter.localeCompare(b.letter))[0].nodeId;
  };
  const router = p.portMapping.replace("router: ", "");
  const out: { role: string; state: string }[] = [];
  if (elect(d.app) === p.nodeId) out.push({ role: "app", state: `forwarded 7443/tcp at ${router}` });
  if (elect(d.web) === p.nodeId) out.push({ role: "web", state: `forwarded 443/tcp, 80/tcp at ${router}; waiting for DNS: point *.${d.domain} at ${p.publicIp}` });
  return out;
}
