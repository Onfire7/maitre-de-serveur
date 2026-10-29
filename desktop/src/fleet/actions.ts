// Direct calls to the Node that owns a Service (decision 160).
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { clone } from "@bufbuild/protobuf";
import { ServiceService, ServiceSpecSchema, ControlServiceRequest_Action } from "@/gen/maitre/v1/service_pb";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { transportFor, type Target } from "@/transport";
import type { FleetSnapshot } from "./useFleet";
import type { KnownNode } from "@/state";
import { withGrants } from "./grants";
import { COMPONENTS_FEATURE, isMulti } from "./components";
import type { ServiceSpec } from "@/gen/maitre/v1/service_pb";
import type { CatalogEntry } from "@/services";

/** Where to reach a Node: the synced record's first address, else the Console's own list. */
export function targetFor(snap: FleetSnapshot | undefined, known: KnownNode[], nodeId: string): Target | undefined {
  const rec = snap?.nodes.find((n) => n.nodeId === nodeId);
  const addr = rec?.addresses[0] ?? known.find((n) => n.node_id === nodeId)?.addresses[0];
  return addr ? { addr, fingerprint: nodeId } : undefined;
}

export type ServiceAction = "start" | "stop" | "restart" | "update" | "rollback";

const actionCode: Record<ServiceAction, ControlServiceRequest_Action> = {
  start: ControlServiceRequest_Action.START,
  stop: ControlServiceRequest_Action.STOP,
  restart: ControlServiceRequest_Action.RESTART,
  update: ControlServiceRequest_Action.UPDATE,
  rollback: ControlServiceRequest_Action.ROLLBACK,
};

/** The calls a Node may refuse for want of a grant (decision 189). */
const GRANTED_CALLS = new Set(["installService", "controlService", "setFolders", "moveVolume", "setExposure", "setPortForwarding", "setSettings", "setImage"]);

/**
 * Install a Catalog entry's Service (G8): the question about grants uses
 * the entry's words, and can install it without its optional features.
 */
export async function installEntry(t: Target, entry: CatalogEntry, spec: ServiceSpec) {
  const raw = createClient(ServiceService, transportFor(t));
  const off = (features: string[]) => {
    const s = clone(ServiceSpecSchema, spec);
    for (const f of features) s.optionalGrants[f] = false;
    return raw.installService({ spec: s });
  };
  return withGrants(t, () => raw.installService({ spec }), { entry, without: off });
}

/**
 * This Node's devices for a device setting (G5); undefined from an older
 * Node, which has no ListDevices, so the Console asks for a path instead.
 */
export async function listDevices(t: Target, kind: string) {
  try {
    return (await createClient(ServiceService, transportFor(t)).listDevices({ kind: kind === "any" ? "" : kind })).devices;
  } catch (e) {
    if (e instanceof ConnectError && e.code === Code.Unimplemented) return undefined;
    throw e;
  }
}

/**
 * A ServiceService client for one Node. The calls that may need grants ask
 * for them and retry once (withGrants); declining throws GrantsDeclined.
 */
export function services(t: Target) {
  const client = createClient(ServiceService, transportFor(t));
  return new Proxy(client, {
    get(target, prop, receiver) {
      const v = Reflect.get(target, prop, receiver);
      if (typeof prop !== "string" || !GRANTED_CALLS.has(prop) || typeof v !== "function") return v;
      return (...args: unknown[]) => withGrants(t, () => v.apply(target, args));
    },
  });
}

export function node(t: Target) {
  return createClient(NodeService, transportFor(t));
}

export async function controlService(t: Target, name: string, action: ServiceAction) {
  return (await services(t).controlService({ name, action: actionCode[action] })).service;
}

export async function removeService(t: Target, name: string, deleteData: boolean) {
  return (await services(t).controlService({ name, action: ControlServiceRequest_Action.REMOVE, deleteData })).service;
}

/**
 * Change a Service's settings and, when given, whether it updates by itself
 * (decision 193). A Node from before SetSettings says it needs updating.
 */
export async function setSettings(t: Target, name: string, settings: Record<string, string>, autoUpdate?: boolean, nodeName = "its Node") {
  try {
    return (await services(t).setSettings({ name, settings, autoUpdate })).service;
  } catch (e) {
    throw tooOld(e, `Changing settings needs a newer Maître Server on ${nodeName}. Update it on the Nodes tab.`);
  }
}

/**
 * Runs a Service on another image (decision 195). In a Service of several
 * components, component names the one to change.
 */
export async function setImage(t: Target, name: string, image: string, nodeName = "its Node", component = "") {
  try {
    return (await services(t).setImage({ name, image, component })).service;
  } catch (e) {
    throw tooOld(e, `Changing the image needs a newer Maître Server on ${nodeName}. Update it on the Nodes tab.`);
  }
}

/**
 * A Service of several components runs only on a Node that says it can
 * (plan C5); an older one would install just the spec's empty single
 * container. Throws what to do instead.
 */
export async function needComponents(t: Target, spec: ServiceSpec, nodeName = "its Node") {
  if (!isMulti(spec)) return;
  const info = (await node(t).getNodeInfo({})).node;
  if (!info?.features.includes(COMPONENTS_FEATURE)) throw new Error(`${spec.name} runs several containers, which needs a newer Maître Server on ${nodeName}. Update it on the Nodes tab.`);
}

/** A Node that lacks a call answers Unimplemented: say what to do instead. */
export function tooOld(e: unknown, message: string): unknown {
  return e instanceof ConnectError && e.code === Code.Unimplemented ? new Error(message) : e;
}
