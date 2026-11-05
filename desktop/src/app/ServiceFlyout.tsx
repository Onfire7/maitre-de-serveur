// The flyout that slides out from behind the sidebar next to a Service row
// (decisions 131, 142, 143). It follows the pointer from row to row. Its
// top row holds the page's browser controls (decision 286): Back, Forward,
// Stop or Refresh, Home, and Copy address. The flyout is a panel the full
// height of the window (decision 293), and the Service page slides aside
// to make room for it, so nothing of the Console ever floats over the page.
import { useEffect } from "react";
import { motion } from "motion/react";
import { toast } from "sonner";
import { ArrowLeft, ArrowRight, Copy, ExternalLink, House, Play, Square, RotateCw, ArrowUpCircle, Undo2, ScrollText, Globe, Router, SlidersHorizontal, HardDrive, ShieldAlert, Trash2, Truck, X } from "lucide-react";
import { cn } from "@/lib/utils";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { FleetService } from "@/fleet/useFleet";
import { nodeWord } from "@/fleet/useFleet";
import { canRollBack } from "@/fleet/components";
import { catalogById, StatusWord } from "./status";
import { mainPage, pageViewOf, pageViews, type PageView } from "@/fleet/pages";
import { canGo, frameCommand, idleRemaining, useFrame, type FrameCommand } from "./frames";
import { closePanel, openPanel } from "./layout";
import { useRoute } from "./routes";

/** The panel's width, which the page slides aside by. */
export const FLYOUT_WIDTH = 240;

export type FlyoutItem = "open" | "start" | "stop" | "restart" | "update" | "rollback" | "logs" | "access" | "setup" | "ports" | "storage" | "grants" | "move" | "remove";

export function ServiceFlyout({
  service,
  top,
  collapsed,
  onItem,
  onEnter,
  onLeave,
}: {
  service: FleetService;
  top: number;
  collapsed: boolean;
  onItem: (item: FlyoutItem) => void;
  onEnter: () => void;
  onLeave: () => void;
}) {
  const s = service.status;
  const nw = nodeWord(service.nodeStatus, Date.now());
  const items = flyoutItems(service);
  useEffect(() => {
    openPanel(FLYOUT_WIDTH);
    return closePanel;
  }, []);
  return (
    <motion.div
      role="menu"
      aria-label={`${service.spec.name} actions`}
      onMouseEnter={onEnter}
      onMouseLeave={onLeave}
      initial={{ x: -24, opacity: 0 }}
      animate={{ x: 0, opacity: 1 }}
      exit={{ x: -24, opacity: 0 }}
      transition={{ duration: 0.15, ease: "easeOut" }}
      className="absolute inset-y-0 left-full z-10 border-r bg-popover text-popover-foreground shadow-lg"
      style={{ width: FLYOUT_WIDTH }}
    >
    <motion.div
      data-testid="flyout-content"
      initial={false}
      animate={{ top }}
      transition={{ top: { type: "spring", stiffness: 500, damping: 40 } }}
      className="absolute inset-x-0 border-y"
      style={{ top }}
    >
      <FrameControls service={service} />
      <div className="border-b px-3 py-2">
        {collapsed && <div className="truncate text-sm font-medium">{service.spec.name}</div>}
        <div className="truncate text-xs text-muted-foreground">
          <StatusWord word={service.word} />
          {" · "}
          {service.node ? (
            <>
              runs on <span className="text-foreground">{service.node.name}</span> ({nw})
            </>
          ) : (
            "Node unknown"
          )}
        </div>
        {service.nodeUnreachable && service.node && <div className="mt-1 text-xs text-status-attention">Node {service.node.name} is unreachable</div>}
        {!service.nodeUnreachable && s?.message && service.word !== "Online" && <div className="mt-1 truncate text-xs text-muted-foreground">{s.message}</div>}
      </div>
      <ul className="py-1">
        {items.map((it) => (
          <li key={it.id} className="flex items-center">
            <button
              role="menuitem"
              disabled={it.disabled}
              onClick={() => onItem(it.id)}
              className={cn(
                "flex min-w-0 flex-1 items-center gap-2 px-3 py-1.5 text-left text-sm hover:bg-accent focus:bg-accent focus:outline-none disabled:opacity-40 disabled:hover:bg-transparent [&>svg]:size-4 [&>svg]:text-muted-foreground",
                it.danger && "text-destructive [&>svg]:text-destructive",
              )}
            >
              {it.icon}
              {it.label}
            </button>
          </li>
        ))}
      </ul>
    </motion.div>
    </motion.div>
  );
}

/** The address of the page the frame is on, said in the Page's own address (not the proxy's). */
export function pageAddress(page: PageView | undefined, path: string | undefined): string | undefined {
  const base = page?.url;
  if (!base) return undefined;
  try {
    return new URL(path ?? "/", base).href;
  } catch {
    return base;
  }
}

function minutesLeft(remaining: number): string {
  const min = Math.ceil((remaining * 3600) / 60);
  return min >= 60 ? "an hour" : `${min} min`;
}

/** The Page the flyout's controls drive: the one shown when it is this
 * Service's, else its first (ADR 0010). */
function drivenPage(service: FleetService, route: ReturnType<typeof useRoute>["route"]): PageView | undefined {
  const name = route.name === "page" && route.service === service.spec.name ? route.page : "";
  return pageViewOf(service, name) ?? mainPage(service);
}

function FrameControls({ service }: { service: FleetService }) {
  const { route } = useRoute();
  const page = drivenPage(service, route);
  const host = page?.host ?? service.spec.name;
  const frame = useFrame(host);
  const web = !!page?.url && service.word === "Online";
  const live = !!frame?.live;
  const remaining = idleRemaining(frame);
  const address = pageAddress(page, frame?.path);
  const act = (cmd: FrameCommand) => frameCommand(host, cmd);
  const copy = () => {
    if (!address) return;
    navigator.clipboard?.writeText(address).then(
      () => toast(`Copied ${address}`),
      () => toast.error("Could not copy the address"),
    );
  };
  const controls: { label: string; icon: React.ReactNode; disabled: boolean; onClick: () => void }[] = [
    { label: "Back", icon: <ArrowLeft />, disabled: !canGo(frame, "back"), onClick: () => act("back") },
    { label: "Forward", icon: <ArrowRight />, disabled: !canGo(frame, "forward"), onClick: () => act("forward") },
    frame?.loading && live ? { label: "Stop", icon: <X />, disabled: false, onClick: () => act("stop") } : { label: "Refresh", icon: <RotateCw />, disabled: !live, onClick: () => act("reload") },
    { label: "Home", icon: <House />, disabled: !web, onClick: () => act("home") },
    { label: "Copy address", icon: <Copy />, disabled: !address, onClick: copy },
  ];
  return (
    <div className="border-b px-2 py-1.5">
      <div role="toolbar" aria-label={`${page && !page.first ? page.host : service.spec.name} page`} className="flex items-center justify-between">
        {controls.map((c) => (
          <Tooltip key={c.label}>
            <TooltipTrigger asChild>
              <button
                aria-label={c.label}
                disabled={c.disabled}
                onClick={c.onClick}
                className="rounded p-1.5 text-muted-foreground hover:bg-accent hover:text-foreground focus:bg-accent focus:outline-none disabled:opacity-40 disabled:hover:bg-transparent [&>svg]:size-4"
              >
                {c.icon}
              </button>
            </TooltipTrigger>
            <TooltipContent side="top">{c.label === "Copy address" && address ? address : c.label}</TooltipContent>
          </Tooltip>
        ))}
      </div>
      {remaining !== null && <div className="truncate px-1 pt-0.5 text-[11px] text-muted-foreground">Open in the background; closes in {minutesLeft(remaining)} unless you come back</div>}
    </div>
  );
}

export interface FlyoutEntry {
  id: FlyoutItem;
  label: string;
  icon: React.ReactNode;
  disabled?: boolean;
  danger?: boolean;
}

/** A Service's actions, shared by the flyout and the Services tab's menu. */
export function flyoutItems(service: FleetService): FlyoutEntry[] {
  const s = service.status;
  const running = s?.state === 3 || s?.state === 2;
  const entry = catalogById.get(service.spec.catalogId);
  const pages = pageViews(service);
  return [
    { id: "open", label: pages.length > 1 ? "Open this page in your browser" : "Open in your browser", icon: <ExternalLink />, disabled: pages.length === 0 || service.word !== "Online" },
    running ? { id: "stop", label: "Stop", icon: <Square />, disabled: service.nodeUnreachable } : { id: "start", label: "Start", icon: <Play />, disabled: service.nodeUnreachable },
    { id: "restart", label: "Restart", icon: <RotateCw />, disabled: service.nodeUnreachable },
    { id: "update", label: "Update", icon: <ArrowUpCircle />, disabled: service.nodeUnreachable },
    ...(canRollBack(service.spec) ? [{ id: "rollback" as const, label: "Roll back", icon: <Undo2 /> }] : []),
    { id: "logs", label: "Logs", icon: <ScrollText />, disabled: service.nodeUnreachable },
    { id: "access", label: "Access…", icon: <Globe /> },
    { id: "setup", label: "Setup…", icon: <SlidersHorizontal /> },
    ...(entry?.port_forward ? [{ id: "ports" as const, label: "Port forwarding…", icon: <Router /> }] : []),
    { id: "storage", label: "Storage…", icon: <HardDrive /> },
    ...(service.grants ? [{ id: "grants" as const, label: `Root powers (${service.grants})…`, icon: <ShieldAlert /> }] : []),
    { id: "move", label: "Move to another Node…", icon: <Truck />, disabled: service.nodeUnreachable },
    { id: "remove", label: "Remove…", icon: <Trash2 />, danger: true },
  ];
}
