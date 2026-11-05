// Typed in-memory navigation (decision 152): no URL router in one Tauri window.
import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";

export const FLEET_TABS = ["overview", "nodes", "services", "storage", "protection", "files", "logs", "devices", "updates", "remote"] as const;
export type FleetTab = (typeof FLEET_TABS)[number];
export const FLEET_TAB_LABEL: Record<FleetTab, string> = {
  overview: "Overview",
  nodes: "Nodes",
  services: "Services",
  storage: "Storage",
  protection: "Protection",
  files: "Files",
  logs: "Logs",
  devices: "Devices",
  updates: "Updates",
  remote: "Remote",
};

/** A section of an open Service row that changes in place (194). */
export type ServiceEdit = "access" | "storage";

export type Route =
  // One Page of a Service (ADR 0010); `page` is its name, "" the first.
  | { name: "page"; service: string; page: string }
  // A Service's Setup: its network, Pages, address and container options.
  | { name: "setup"; service: string }
  | { name: "catalog" }
  | { name: "fleet"; tab: FleetTab; nodeId?: string; files?: { nodeId: string; spaceId?: string; driveId?: string; path?: string; service?: string; folder?: string }; logs?: { nodeId: string; source: string }; open?: { service: string; edit?: ServiceEdit } }
  | { name: "add-node"; addr: string; installerId: string }
  | { name: "code" };

interface RouteCtx {
  route: Route;
  go: (r: Route) => void;
}

const Ctx = createContext<RouteCtx>({ route: { name: "fleet", tab: "overview" }, go: () => {} });

/** The Page a route names, or undefined for any other route. */
export function routePage(r: Route): { service: string; page: string } | undefined {
  return r.name === "page" ? { service: r.service, page: r.page } : undefined;
}

export function RouteProvider({ initial, children }: { initial: Route; children: ReactNode }) {
  const [route, setRoute] = useState<Route>(initial);
  const go = useCallback((r: Route) => setRoute(r), []);
  const value = useMemo(() => ({ route, go }), [route, go]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

/** `?route=page:immich/admin`, `?route=service:jellyfin` (its first Page),
 * `?route=setup:qbittorrent`, `?route=catalog`, `?route=fleet:nodes` opens
 * there (screenshot fixtures). */
export function routeFromUrl(fallback: Route): Route {
  if (typeof window === "undefined") return fallback;
  const v = new URLSearchParams(window.location.search).get("route");
  if (!v) return fallback;
  const [name, arg] = v.split(":");
  if ((name === "page" || name === "service") && arg) {
    const [service, page] = arg.split("/");
    return { name: "page", service, page: page ?? "" };
  }
  if (name === "setup" && arg) return { name: "setup", service: arg };
  if (name === "catalog") return { name: "catalog" };
  if (name === "add-node" && arg) return { name: "add-node", addr: arg + (v.split(":")[2] ? ":" + v.split(":")[2] : ""), installerId: "" };
  if (name === "fleet") return { name: "fleet", tab: (FLEET_TABS as readonly string[]).includes(arg) ? (arg as FleetTab) : "overview" };
  return fallback;
}

export function useRoute() {
  return useContext(Ctx);
}
