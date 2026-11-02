// Routes to a Node (decision 284): a Node's record lists the ways to reach
// it, LAN addresses first, then its Tailscale address, and the Fleet's
// domain is appended by the Console. This module names each kind for the
// user, keeps the way that answered last for every Node, and keeps the
// user's pinned connection, all on this device.
import type { Target } from "@/transport";

export type RouteKind = "home" | "tailscale" | "domain" | "other";

const PIN_KEY = "maitre.fleet.pin";
const LAST_KEY = "maitre.fleet.lastAnchor";
const ROUTES_KEY = "maitre.fleet.routes";
const DEMOTED_KEY = "maitre.fleet.demoted";

/** The host part of host:port, [v6]:port or a bare host. */
export function hostOf(addr: string): string {
  const m = /^\[([^\]]+)\](?::\d+)?$/.exec(addr);
  if (m) return m[1];
  const i = addr.lastIndexOf(":");
  if (i > 0 && !addr.slice(0, i).includes(":")) return addr.slice(0, i);
  return addr;
}

/** True for Tailscale's own ranges: 100.64.0.0/10 and fd7a:115c:a1e0::/48, or a .ts.net name. */
export function isTailscale(host: string): boolean {
  const h = host.toLowerCase().replace(/\.$/, "");
  if (h.endsWith(".ts.net")) return true;
  const v4 = /^100\.(\d+)\.\d+\.\d+$/.exec(h);
  if (v4) return Number(v4[1]) >= 64 && Number(v4[1]) <= 127;
  return h.startsWith("fd7a:115c:a1e0:");
}

function isHome(host: string): boolean {
  const h = host.toLowerCase().replace(/\.$/, "");
  if (h.endsWith(".local") || h.endsWith(".lan") || h.endsWith(".home.arpa") || h.endsWith(".internal")) return true;
  if (/^10\.\d+\.\d+\.\d+$/.test(h) || /^192\.168\.\d+\.\d+$/.test(h) || /^169\.254\.\d+\.\d+$/.test(h)) return true;
  const m = /^172\.(\d+)\.\d+\.\d+$/.exec(h);
  if (m) return Number(m[1]) >= 16 && Number(m[1]) <= 31;
  return h.startsWith("fe80:") || h.startsWith("fc") || h.startsWith("fd");
}

/** What kind of way to a Node this address is; `domain` is the Fleet's domain (decision 218). */
export function routeKind(addr: string, domainAddr = ""): RouteKind {
  if (domainAddr && addr === domainAddr) return "domain";
  const host = hostOf(addr);
  if (isTailscale(host)) return "tailscale";
  if (isHome(host)) return "home";
  return "other";
}

/** Plain words for a route, as the chooser and the header say them. */
export function routeLabel(kind: RouteKind): string {
  switch (kind) {
    case "home":
      return "at home";
    case "tailscale":
      return "over Tailscale";
    case "domain":
      return "at your domain";
    default:
      return "by address";
  }
}

function read<T>(key: string): T | undefined {
  try {
    const raw = localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : undefined;
  } catch {
    return undefined;
  }
}
function write(key: string, value: unknown) {
  try {
    if (value === undefined) localStorage.removeItem(key);
    else localStorage.setItem(key, JSON.stringify(value));
  } catch {
    /* no storage */
  }
}

/** The connection the user chose, kept until they let the app choose again or it fails. */
export function pinned(): Target | undefined {
  const p = read<Target>(PIN_KEY);
  return p?.addr && p.fingerprint ? p : undefined;
}
export function setPinned(t: Target | undefined) {
  write(PIN_KEY, t);
}

/** The anchor that answered last, so the next start tries it first (away from home too). */
export function lastAnchor(): Target | undefined {
  const p = read<Target>(LAST_KEY);
  return p?.addr && p.fingerprint ? p : undefined;
}
export function setLastAnchor(t: Target | undefined) {
  write(LAST_KEY, t);
}

let routes: Map<string, string> | undefined;
function loadRoutes(): Map<string, string> {
  if (!routes) routes = new Map(Object.entries(read<Record<string, string>>(ROUTES_KEY) ?? {}));
  return routes;
}

/** The address that answered last for this Node from this device, if any. */
export function routeFor(nodeId: string): string | undefined {
  return loadRoutes().get(nodeId);
}

/** Note that `addr` answered for this Node. */
export function rememberRoute(nodeId: string, addr: string) {
  const d = loadDemoted();
  if (d.get(nodeId)?.includes(addr)) {
    d.set(nodeId, d.get(nodeId)!.filter((a) => a !== addr));
    saveDemoted();
  }
  const r = loadRoutes();
  if (r.get(nodeId) === addr) return;
  r.set(nodeId, addr);
  write(ROUTES_KEY, Object.fromEntries(r));
}

let demoted: Map<string, string[]> | undefined;
function loadDemoted(): Map<string, string[]> {
  if (!demoted) demoted = new Map(Object.entries(read<Record<string, string[]>>(DEMOTED_KEY) ?? {}));
  return demoted;
}
function saveDemoted() {
  write(DEMOTED_KEY, Object.fromEntries(loadDemoted()));
}

/** The ways to this Node that did not answer, oldest failure first. */
export function demotedRoutes(nodeId: string): string[] {
  return loadDemoted().get(nodeId) ?? [];
}

/**
 * Put a way that did not answer last in line for this Node. It is not
 * forgotten: a way is rarely wrong, only out of reach from where the
 * device is now, so it is tried again after the others.
 */
export function demoteRoute(nodeId: string, addr: string) {
  const r = loadRoutes();
  if (r.get(nodeId) === addr) {
    r.delete(nodeId);
    write(ROUTES_KEY, Object.fromEntries(r));
  }
  const d = loadDemoted();
  d.set(nodeId, [...(d.get(nodeId) ?? []).filter((a) => a !== addr), addr]);
  saveDemoted();
}

/** For tests. */
export function resetRoutes() {
  routes = undefined;
  demoted = undefined;
}

/**
 * Every way to a Node in the order to try them: the one that answered
 * last; then the ones of the same kind as the way the anchor was reached,
 * since a device away from home that reached one Node over Tailscale
 * reaches the others the same way (an anchor reached at the domain or by
 * a public address says the device is away, so Tailscale comes first:
 * the domain reaches only the Node holding the app's forward); then the
 * rest in the record's order; and the ways that did not answer last.
 */
export function routeOrder(nodeId: string, addresses: string[], anchorKind: RouteKind | undefined, domainAddr = ""): string[] {
  const out: string[] = [];
  const add = (a: string | undefined) => {
    if (a && addresses.includes(a) && !out.includes(a)) out.push(a);
  };
  const down = new Set(demotedRoutes(nodeId));
  const rest = addresses.filter((a) => !down.has(a));
  add(routeFor(nodeId));
  const kinds: RouteKind[] = anchorKind === "home" || anchorKind === "tailscale" ? [anchorKind] : anchorKind ? ["tailscale"] : [];
  for (const k of kinds) rest.filter((a) => routeKind(a, domainAddr) === k).forEach(add);
  rest.forEach(add);
  demotedRoutes(nodeId).forEach(add);
  return out;
}

/** The address to use for a Node: the first of routeOrder. */
export function chooseAddress(nodeId: string, addresses: string[], anchorKind: RouteKind | undefined, domainAddr = ""): string | undefined {
  return routeOrder(nodeId, addresses, anchorKind, domainAddr)[0];
}
