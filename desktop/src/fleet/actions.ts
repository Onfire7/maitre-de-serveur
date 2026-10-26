// Direct calls to the Node that owns a Service (decision 160).
import { createClient } from "@connectrpc/connect";
import { ServiceService, ControlServiceRequest_Action } from "@/gen/maitre/v1/service_pb";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { transportFor, type Target } from "@/transport";
import type { FleetSnapshot } from "./useFleet";
import type { KnownNode } from "@/state";

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

export function services(t: Target) {
  return createClient(ServiceService, transportFor(t));
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
