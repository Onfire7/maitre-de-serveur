// The Service-first sidebar (decisions 130-137, 142, 145-147): every Service
// in the Fleet as one flat list, the Fleet entry always at the bottom with
// the Node list beneath it when the Fleet page is open.
import { useCallback, useEffect, useRef, useState } from "react";
import { AnimatePresence, motion } from "motion/react";
import {
  ChevronRight,
  Plus,
  PanelLeftClose,
  PanelLeftOpen,
  Settings2,
  Boxes,
} from "lucide-react";
import { cn } from "@/lib/utils";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import type { FleetService, FleetSnapshot } from "@/fleet/useFleet";
import { nodeWord } from "@/fleet/useFleet";
import { ServiceIcon, NodeMark, type NodeWord } from "./status";
import { ServiceFlyout, type FlyoutItem } from "./ServiceFlyout";
import { useRoute } from "./routes";

export const SIDEBAR_WIDTH = 240;
export const SIDEBAR_COLLAPSED = 56;
const HOVER_DELAY = 600;
const LEAVE_DELAY = 300;

export interface SidebarNode {
  nodeId: string;
  name: string;
  word: NodeWord;
}

export function Sidebar({
  services,
  nodes,
  fleetName,
  badge,
  collapsed,
  onToggle,
  canAddService,
  onFlyoutItem,
  onPreferences,
  provisioning,
}: {
  services: FleetService[];
  nodes: SidebarNode[];
  fleetName: string;
  badge: number;
  collapsed: boolean;
  onToggle: () => void;
  canAddService: boolean;
  onFlyoutItem: (service: FleetService, item: FlyoutItem) => void;
  onPreferences: () => void;
  provisioning?: { name: string };
}) {
  const { route, go } = useRoute();
  const wrapRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const [fly, setFly] = useState<{
    service: FleetService;
    el: HTMLElement;
    top: number;
  } | null>(null);
  const hoverTimer = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  );
  const leaveTimer = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  );

  const clearTimers = () => {
    clearTimeout(hoverTimer.current);
    clearTimeout(leaveTimer.current);
  };
  const topOf = (el: HTMLElement) =>
    el.getBoundingClientRect().top -
    (wrapRef.current?.getBoundingClientRect().top ?? 0);
  const openFor = useCallback(
    (service: FleetService, el: HTMLElement) =>
      setFly({ service, el, top: topOf(el) }),
    [],
  );
  // The flyout sits beside the sidebar, not inside the scrolling list, so it
  // follows its row when the list scrolls.
  useEffect(() => {
    const list = listRef.current;
    if (!list || !fly) return;
    const on = () => setFly((f) => (f ? { ...f, top: topOf(f.el) } : f));
    list.addEventListener("scroll", on);
    return () => list.removeEventListener("scroll", on);
  }, [fly?.el]);
  const rowEnter = (service: FleetService, el: HTMLElement) => {
    clearTimers();
    if (fly) openFor(service, el);
    else
      hoverTimer.current = setTimeout(() => openFor(service, el), HOVER_DELAY);
  };
  const scheduleClose = () => {
    clearTimers();
    leaveTimer.current = setTimeout(() => setFly(null), LEAVE_DELAY);
  };
  const keepOpen = () => clearTimers();
  useEffect(() => () => clearTimers(), []);
  // `?flyout=<service>` opens the flyout on load (screenshot fixture).
  const fixture = useRef(typeof window !== "undefined" ? new URLSearchParams(window.location.search).get("flyout") : null);
  useEffect(() => {
    const name = fixture.current;
    if (!name || services.length === 0) return;
    const svc = services.find((s) => s.spec.name === name);
    const el = listRef.current?.querySelector<HTMLElement>(`[data-service="${name}"]`);
    if (svc && el) {
      fixture.current = null;
      setTimeout(() => openFor(svc, el), 400);
    }
  }, [services, openFor]);
  // The flyout follows its row; when the list changes under it, close it.
  useEffect(() => {
    if (fly && !services.some((s) => s.spec.name === fly.service.spec.name))
      setFly(null);
  }, [services, fly]);

  const width = collapsed ? SIDEBAR_COLLAPSED : SIDEBAR_WIDTH;
  const fleetOpen = route.name === "fleet";
  const selectedService = route.name === "service" ? route.service : undefined;
  const selectedNode = route.name === "fleet" ? route.nodeId : undefined;

  return (
    <motion.div
      ref={wrapRef}
      initial={{ x: -SIDEBAR_WIDTH, opacity: 0 }}
      animate={{ x: 0, opacity: 1, width }}
      exit={{ x: -SIDEBAR_WIDTH, opacity: 0 }}
      transition={{
        x: { duration: 0.32, ease: "easeOut" },
        opacity: { duration: 0.2 },
        width: { duration: 0.2, ease: "easeInOut" },
      }}
      className="relative h-full shrink-0"
      style={{ width }}
    >
      <aside
        className="relative z-20 flex h-full w-full flex-col overflow-hidden border-r bg-sidebar text-sidebar-foreground"
        aria-label="Services"
      >
        <div
          className={cn(
            "flex h-11 shrink-0 items-center border-b px-2",
            collapsed ? "justify-center" : "justify-between",
          )}
        >
          {!collapsed && (
            <span className="truncate pl-1 text-sm font-semibold text-muted-foreground">
              Services
            </span>
          )}
          <Tooltip>
            <TooltipTrigger asChild>
              <button
                onClick={onToggle}
                className="rounded-md p-1.5 text-muted-foreground hover:bg-sidebar-accent hover:text-foreground"
                aria-label={
                  collapsed ? "Expand the sidebar" : "Collapse the sidebar"
                }
              >
                {collapsed ? (
                  <PanelLeftOpen className="size-4" />
                ) : (
                  <PanelLeftClose className="size-4" />
                )}
              </button>
            </TooltipTrigger>
            <TooltipContent side="right">
              {collapsed ? "Expand" : "Collapse"} (Ctrl+B)
            </TooltipContent>
          </Tooltip>
        </div>

        <div
          ref={listRef}
          className="min-h-0 flex-1 overflow-y-auto py-1"
          onMouseLeave={scheduleClose}
          onMouseEnter={keepOpen}
        >
          <ul className="px-1.5">
            {services.map((s, i) => (
              <motion.li
                key={s.spec.name}
                initial={{ opacity: 0, x: -12 }}
                animate={{ opacity: 1, x: 0 }}
                transition={{ delay: i * 0.03, duration: 0.2 }}
              >
                <ServiceRow
                  service={s}
                  collapsed={collapsed}
                  selected={selectedService === s.spec.name}
                  onClick={() => go({ name: "service", service: s.spec.name })}
                  onEnter={(el) => rowEnter(s, el)}
                  onChevron={(el) => {
                    clearTimers();
                    openFor(s, el);
                  }}
                  onKeyOpen={(el) => openFor(s, el)}
                />
              </motion.li>
            ))}
            <li>
              <Tooltip>
                <TooltipTrigger asChild>
                  <button
                    disabled={!canAddService}
                    onClick={() => go({ name: "catalog" })}
                    onMouseEnter={scheduleClose}
                    className={cn(
                      "group flex w-full items-center gap-2 rounded-md px-1.5 py-1.5 text-sm text-muted-foreground hover:bg-sidebar-accent hover:text-foreground disabled:opacity-50 disabled:hover:bg-transparent",
                      route.name === "catalog" &&
                        "bg-sidebar-accent text-foreground",
                      collapsed && "justify-center",
                    )}
                  >
                    <span className="inline-flex size-7 shrink-0 items-center justify-center rounded-lg border-2 border-dashed border-border">
                      <Plus className="size-4" />
                    </span>
                    {!collapsed && (
                      <span className="truncate">Add a Service</span>
                    )}
                  </button>
                </TooltipTrigger>
                {(collapsed || !canAddService) && (
                  <TooltipContent side="right">
                    {canAddService
                      ? "Add a Service"
                      : "Create a Storage Space first"}
                  </TooltipContent>
                )}
              </Tooltip>
            </li>
          </ul>
        </div>

        <div className="shrink-0 border-t p-1.5">
          <button
            onClick={() =>
              go({ name: "fleet", tab: fleetOpen ? route.tab : "overview" })
            }
            className={cn(
              "flex w-full items-center gap-2 rounded-md px-1.5 py-1.5 text-sm hover:bg-sidebar-accent",
              fleetOpen && !selectedNode && "bg-sidebar-accent",
              collapsed && "justify-center",
            )}
            aria-current={fleetOpen ? "page" : undefined}
          >
            <span className="relative inline-flex size-7 shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground">
              <Boxes className="size-4" />
              {badge > 0 && (
                <span className="absolute -right-1.5 -top-1.5 min-w-4 rounded-full bg-status-attention px-1 text-center text-[10px] font-semibold leading-4 text-black">
                  {badge}
                </span>
              )}
            </span>
            {!collapsed && (
              <span className="min-w-0 flex-1 text-left">
                <span className="block truncate font-medium">
                  {fleetName || "Fleet"}
                </span>
                <span className="block truncate text-xs text-muted-foreground">
                  {nodes.length} {nodes.length === 1 ? "Node" : "Nodes"}
                </span>
              </span>
            )}
          </button>
          <AnimatePresence initial={false}>
            {fleetOpen && (
              <motion.ul
                initial={{ height: 0, opacity: 0 }}
                animate={{ height: "auto", opacity: 1 }}
                exit={{ height: 0, opacity: 0 }}
                transition={{ duration: 0.2, ease: "easeInOut" }}
                className="overflow-hidden"
              >
                {nodes.map((n) => (
                  <li key={n.nodeId}>
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <button
                          onClick={() =>
                            go({
                              name: "fleet",
                              tab: "nodes",
                              nodeId: n.nodeId,
                            })
                          }
                          className={cn(
                            "flex w-full items-center gap-2 rounded-md py-1 pr-1.5 text-sm hover:bg-sidebar-accent",
                            selectedNode === n.nodeId && "bg-sidebar-accent",
                            collapsed ? "justify-center pl-1.5" : "pl-3",
                          )}
                        >
                          <NodeMark word={n.word} />
                          {!collapsed && (
                            <span className="min-w-0 flex-1 truncate text-left">
                              {n.name}
                              {n.word !== "Healthy" && (
                                <span className="ml-1 text-xs text-muted-foreground">
                                  {n.word}
                                </span>
                              )}
                            </span>
                          )}
                        </button>
                      </TooltipTrigger>
                      <TooltipContent side="right">
                        {n.name} · {n.word}
                      </TooltipContent>
                    </Tooltip>
                  </li>
                ))}
                {provisioning && (
                  <li
                    className={cn(
                      "flex items-center gap-2 py-1 pr-1.5 text-sm text-muted-foreground",
                      collapsed ? "justify-center pl-1.5" : "pl-3",
                    )}
                  >
                    <NodeMark word="Provisioning" />
                    {!collapsed && (
                      <span className="truncate">
                        {provisioning.name} · Provisioning
                      </span>
                    )}
                  </li>
                )}
              </motion.ul>
            )}
          </AnimatePresence>
          <button
            onClick={onPreferences}
            className={cn(
              "mt-0.5 flex w-full items-center gap-2 rounded-md px-1.5 py-1 text-xs text-muted-foreground hover:bg-sidebar-accent hover:text-foreground",
              collapsed && "justify-center",
            )}
            aria-label="Preferences"
          >
            <Settings2 className="size-3.5" />
            {!collapsed && "Preferences"}
          </button>
        </div>
      </aside>
      <AnimatePresence>
        {fly && (
          <ServiceFlyout
            key="flyout"
            service={
              services.find((s) => s.spec.name === fly.service.spec.name) ??
              fly.service
            }
            top={fly.top}
            collapsed={collapsed}
            onEnter={keepOpen}
            onLeave={scheduleClose}
            onItem={(item) => {
              setFly(null);
              onFlyoutItem(fly.service, item);
            }}
          />
        )}
      </AnimatePresence>
    </motion.div>
  );
}

function ServiceRow({
  service,
  collapsed,
  selected,
  onClick,
  onEnter,
  onChevron,
  onKeyOpen,
}: {
  service: FleetService;
  collapsed: boolean;
  selected: boolean;
  onClick: () => void;
  onEnter: (el: HTMLElement) => void;
  onChevron: (el: HTMLElement) => void;
  onKeyOpen: (el: HTMLElement) => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const stopped = service.word === "Stopped";
  const row = (
    <div
      ref={ref}
      data-service={service.spec.name}
      className={cn(
        "group relative flex items-center rounded-md hover:bg-sidebar-accent",
        selected && "bg-sidebar-accent",
        collapsed && "justify-center",
      )}
      onMouseEnter={() => ref.current && onEnter(ref.current)}
    >
      <button
        onClick={onClick}
        onKeyDown={(e) => {
          if (e.key === "ArrowRight" && ref.current) {
            e.preventDefault();
            onKeyOpen(ref.current);
          }
        }}
        aria-current={selected ? "page" : undefined}
        className={cn(
          "flex min-w-0 flex-1 items-center gap-2 px-1.5 py-1.5 text-left text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded-md",
          collapsed && "flex-none",
        )}
      >
        <ServiceIcon catalogId={service.spec.catalogId} word={service.word} />
        {!collapsed && (
          <span className={cn("truncate", stopped && "text-muted-foreground")}>
            {service.spec.name}
          </span>
        )}
      </button>
      {!collapsed && (
        <button
          aria-label={`${service.spec.name} actions`}
          onMouseEnter={() => ref.current && onChevron(ref.current)}
          onClick={() => ref.current && onChevron(ref.current)}
          className="mr-1 rounded p-1 text-muted-foreground opacity-0 transition-opacity hover:bg-background/60 group-hover:opacity-100 focus:opacity-100"
        >
          <ChevronRight className="size-4" />
        </button>
      )}
    </div>
  );
  if (!collapsed) return row;
  return (
    <Tooltip>
      <TooltipTrigger asChild>{row}</TooltipTrigger>
      <TooltipContent side="right">
        {service.spec.name} · {service.word}
        {service.nodeUnreachable && service.node
          ? ` · Node ${service.node.name} is unreachable`
          : ""}
      </TooltipContent>
    </Tooltip>
  );
}

export function sidebarNodes(
  snap: FleetSnapshot | undefined,
  now = Date.now(),
): SidebarNode[] {
  if (!snap) return [];
  return snap.nodes.map((n) => ({
    nodeId: n.nodeId,
    name: n.name,
    word: nodeWord(snap.status.get(n.nodeId), now),
  }));
}
