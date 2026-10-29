// The Fleet's own domain (decision 218): the app reaches the Fleet at
// <domain>:7443 through whichever Node holds the "app" role, and Caddy on
// the "web" Node answers Services published at <service>.<domain>. Kept on
// this device too, so a Console started away from home knows where to go
// before it has read the Fleet.
import { fromBinary } from "@bufbuild/protobuf";
import { FleetDomainSchema, type FleetDomain, type Record as Rec } from "@/gen/maitre/v1/fleet_pb";
import type { FleetSnapshot } from "./useFleet";

export const DOMAIN_KIND = "fleet_domain";
export const APP_PORT = 7443;
export type Role = "app" | "web";

const DOMAIN_KEY = "maitre.fleet.domain";
const HOLDER_KEY = "maitre.fleet.appHolder";
const NAME_KEY = "maitre.fleet.domainName";

export function fleetDomain(records: Rec[] | undefined): FleetDomain | undefined {
  const r = records?.find((x) => x.kind === DOMAIN_KIND && !x.deleted);
  return r ? fromBinary(FleetDomainSchema, r.body) : undefined;
}

/** Which Node holds a role and how its forwarding stands, from gossip. */
export function holderOf(snap: FleetSnapshot | undefined, role: Role): { nodeId: string; state: string } | undefined {
  for (const [nodeId, st] of snap?.status ?? []) {
    const h = st.entries?.find((e) => e.role === role);
    if (h) return { nodeId, state: h.state };
  }
  return undefined;
}

/** Remembers the domain and the app's Node for the next start away from home. */
export function rememberDomain(snap: FleetSnapshot) {
  const d = fleetDomain(snap.records);
  const app = d?.app?.enabled && d.domain ? d.domain : "";
  try {
    if (app) localStorage.setItem(DOMAIN_KEY, app);
    else localStorage.removeItem(DOMAIN_KEY);
    if (d?.domain) localStorage.setItem(NAME_KEY, d.domain);
    else localStorage.removeItem(NAME_KEY);
    const h = holderOf(snap, "app");
    if (h) localStorage.setItem(HOLDER_KEY, h.nodeId);
  } catch {
    /* no storage */
  }
}

/** The address the app tries when no Node answers at home: <domain>:7443, or "". */
export function domainAddr(): string {
  try {
    const d = localStorage.getItem(DOMAIN_KEY);
    return d ? `${d}:${APP_PORT}` : "";
  } catch {
    return "";
  }
}

/** Node ids in the order to try through the domain: the last holder first. */
export function domainOrder(ids: string[]): string[] {
  let first = "";
  try {
    first = localStorage.getItem(HOLDER_KEY) ?? "";
  } catch {
    /* no storage */
  }
  return [...ids].sort((a, b) => Number(b === first) - Number(a === first));
}

const hostRE = /^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/;

/** A usable public name, as the Node checks it (direct.ValidHost). */
export function validDomain(h: string): boolean {
  if ([".local", ".localhost", ".home.arpa", ".internal", ".lan"].some((s) => h.endsWith(s))) return false;
  return h.length <= 253 && hostRE.test(h);
}

/** The Fleet's domain as last read, "" for none: Services publish under it. */
export function fleetDomainName(): string {
  try {
    return localStorage.getItem(NAME_KEY) ?? "";
  } catch {
    return "";
  }
}
