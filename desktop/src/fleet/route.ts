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
  const r = loadRoutes();
  if (r.get(nodeId) === addr) return;
  r.set(nodeId, addr);
  write(ROUTES_KEY, Object.fromEntries(r));
}

/** Forget a way that stopped answering, so the next pick starts over. */
export function forgetRoute(nodeId: string, addr: string) {
  const r = loadRoutes();
  if (r.get(nodeId) !== addr) return;
  r.delete(nodeId);
  write(ROUTES_KEY, Object.fromEntries(r));
}

/** For tests. */
export function resetRoutes() {
  routes = undefined;
}

/**
 * The address to use for a Node: the one that answered last; else one of
 * the same kind as the way the anchor was reached, since a device away
 * from home that reached one Node over Tailscale reaches the others the
 * same way; else the first.
 */
export function chooseAddress(nodeId: string, addresses: string[], anchorKind: RouteKind | undefined, domainAddr = ""): string | undefined {
  if (addresses.length === 0) return undefined;
  const last = routeFor(nodeId);
  if (last && addresses.includes(last)) return last;
  if (anchorKind) {
    const same = addresses.find((a) => routeKind(a, domainAddr) === anchorKind);
    if (same) return same;
  }
  return addresses[0];
}
