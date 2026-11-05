// A Service's Setup (ADR 0010): what a person changes beyond its install
// form, kept as part of its record. The Console shows what the Node would
// write before it writes it (previewSetup) and then applies it
// (applySetup), which restarts the Service along with the Tunnel it joins
// or leaves and that Tunnel's other Services.
import { create } from "@bufbuild/protobuf";
import { ServiceService, SetupSchema, type RenderedUnit, type ServiceSpec, type Setup, type UnitOverride } from "@/gen/maitre/v1/service_pb";
import { createClient } from "@connectrpc/connect";
import { transportFor, type Target } from "@/transport";
import { withGrants } from "./grants";
import { pagesOf } from "./pages";
import type { FleetService } from "./useFleet";

/** Which network a Service is on (ServiceSpec.networkKind). */
export const NETWORK_OWN = "";
export const NETWORK_TUNNEL = "tunnel";
export const NETWORK_SERVICE = "service";
export const NETWORK_HOST = "host";

/** The Setup a Service has now, which the page starts from. */
export function setupOf(spec: ServiceSpec): Setup {
  return create(SetupSchema, {
    networkKind: spec.networkKind,
    networkTarget: spec.networkTarget,
    fleetReach: spec.fleetReach,
    ports: [...spec.ports],
    address: spec.address,
    networkInternal: spec.networkInternal,
    pages: pagesOf(spec).map((p) => ({ ...p })),
    overrides: spec.overrides.map((o) => ({ ...o })),
  });
}

/** What the Node would write with this Setup, or as the Service is now. */
export async function previewSetup(t: Target, name: string, setup?: Setup) {
  return createClient(ServiceService, transportFor(t)).previewSetup({ name, setup });
}

/** Apply a Setup: saved, written and restarted. A protected option an
 * override adds needs a grant like any of the bundle's (decision 189). */
export async function applySetup(t: Target, name: string, setup: Setup) {
  const client = createClient(ServiceService, transportFor(t));
  return withGrants(t, async () => (await client.setSetup({ name, setup })).service);
}

/** Where one Page is reachable; "" on a Page follows the Service. */
export async function setPageExposure(t: Target, name: string, page: string, exposure: string, publicHost: string) {
  const client = createClient(ServiceService, transportFor(t));
  return withGrants(t, async () => (await client.setExposure({ name, page, exposure, publicHost })).service);
}

/** A unit file's lines with where each came from, as the page shows them. */
export function unitLines(u: RenderedUnit): { text: string; source: string }[] {
  const lines = u.body.endsWith("\n") ? u.body.slice(0, -1).split("\n") : u.body.split("\n");
  return lines.map((text, i) => ({ text, source: u.sources[i] ?? "" }));
}

/** The networks a Service may join on its Node (ADR 0010): the Tunnels and
 * the Services of several containers that run beside it. A Service on
 * another Node is offered too, with the move it would need. */
export interface NetworkChoice {
  name: string;
  kind: typeof NETWORK_TUNNEL | typeof NETWORK_SERVICE;
  /** The Node it runs on, when that is not this Service's. */
  otherNode?: string;
  /** What else would have to move with it. */
  withIt: string[];
}

export function networkChoices(service: FleetService, all: FleetService[]): NetworkChoice[] {
  const out: NetworkChoice[] = [];
  for (const o of all) {
    if (o.spec.name === service.spec.name) continue
    // A Service already on another's network or in a Tunnel is not a
    // network of its own to join.
    if (o.spec.networkKind === NETWORK_TUNNEL || o.spec.networkKind === NETWORK_HOST) continue;
    const kind = o.spec.tunnel ? NETWORK_TUNNEL : o.spec.components.length > 0 ? NETWORK_SERVICE : undefined;
    if (!kind) continue;
    const otherNode = o.spec.nodeId !== service.spec.nodeId ? (o.node?.name ?? "another Node") : undefined;
    out.push({ name: o.spec.name, kind, otherNode, withIt: dependantsOf(o.spec.name, all) });
  }
  return out.sort((a, b) => a.name.localeCompare(b.name));
}

/** The Services that join this one's network or Tunnel. */
export function dependantsOf(name: string, all: FleetService[]): string[] {
  return all
    .filter((s) => s.spec.networkTarget === name && s.spec.name !== name && (s.spec.networkKind === NETWORK_TUNNEL || s.spec.networkKind === NETWORK_SERVICE))
    .map((s) => s.spec.name)
    .sort();
}

/** The whole set that has to sit on one Node with `name`: it, what joins
 * it, and what those join. Used when Setup offers to move a stack. */
export function stackOf(name: string, all: FleetService[]): string[] {
  const seen = new Set<string>([name]);
  for (let added = true; added; ) {
    added = false;
    for (const s of all) {
      const joins = s.spec.networkKind === NETWORK_TUNNEL || s.spec.networkKind === NETWORK_SERVICE;
      if (!joins) continue;
      if (seen.has(s.spec.networkTarget) && !seen.has(s.spec.name)) {
        seen.add(s.spec.name);
        added = true;
      }
      if (seen.has(s.spec.name) && s.spec.networkTarget && !seen.has(s.spec.networkTarget)) {
        seen.add(s.spec.networkTarget);
        added = true;
      }
    }
  }
  return [...seen].sort();
}

/** An override as one line of a unit, the way the page lists it. */
export function overrideLine(o: UnitOverride): string {
  return o.remove ? `remove ${o.key}${o.value ? `=${o.value}` : ""}` : `${o.key}=${o.value}`;
}
