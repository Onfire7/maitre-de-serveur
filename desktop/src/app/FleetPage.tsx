// The Fleet page (decisions 132, 148-150): one route, seven tabs. The tabs
// not yet redesigned embed the existing panels; step 4 migrates them.
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import type { FleetService, FleetSnapshot } from "@/fleet/useFleet";
import type { FlyoutItem } from "./ServiceFlyout";
import { ServicesTab } from "./ServicesTab";
import type { ConsoleState, DeviceInfo, Found } from "@/state";
import { LogsTab } from "./LogsTab";
import { FLEET_TABS, FLEET_TAB_LABEL, useRoute, type FleetTab } from "./routes";
import { NodesTab } from "./NodesTab";
import { StorageTab } from "./StorageTab";
import { UpdatesTab } from "./UpdatesTab";
import { DevicesTab } from "./DevicesTab";
import { RemoteTab } from "./RemoteTab";
import { FilesTab } from "./FilesTab";
import type { Notice } from "./notices";
import { OverviewTab } from "./OverviewTab";

export function FleetPage({
  tab,
  nodeId,
  files,
  logs,
  snap,
  state,
  device,
  found,
  notices,
  onState,
  onPromote,
  scoped,
  services,
  onAct,
  now,
}: {
  tab: FleetTab;
  nodeId?: string;
  files?: { nodeId: string; spaceId?: string; driveId?: string; path?: string; service?: string; folder?: string };
  logs?: { nodeId: string; source: string };
  snap: FleetSnapshot | undefined;
  state: ConsoleState;
  device: DeviceInfo;
  found: Found[];
  notices: Notice[];
  onState: (s: ConsoleState) => Promise<void>;
  onPromote: () => void;
  scoped: boolean;
  services: FleetService[];
  onAct: (s: FleetService, item: FlyoutItem) => void;
  now: number;
}) {
  const { go } = useRoute();
  const anchorName = snap ? snap.nodes.find((n) => n.nodeId === snap.anchor.nodeId)?.name : undefined;
  return (
    <div className="flex h-full flex-col">
      <header className="shrink-0 border-b px-6 pt-5">
        <div className="flex items-baseline gap-3">
          <h1 className="text-xl font-semibold">{state.fleet?.name ?? "Fleet"}</h1>
          {tab === "overview" && anchorName && <span className="text-xs text-muted-foreground">connected through {anchorName}</span>}
        </div>
        <Tabs value={tab} onValueChange={(v) => go({ name: "fleet", tab: v as FleetTab })} className="mt-3">
          <TabsList className="h-9 bg-transparent p-0">
            {FLEET_TABS.map((t) => {
              const count = notices.filter((n) => n.tab === t).length;
              return (
                <TabsTrigger key={t} value={t} className="rounded-none border-b-2 border-transparent px-3 data-[state=active]:border-primary data-[state=active]:shadow-none">
                  {FLEET_TAB_LABEL[t]}
                  {count > 0 && <Badge className="ml-1 h-4 min-w-4 bg-status-attention px-1 text-[10px] text-black">{count}</Badge>}
                </TabsTrigger>
              );
            })}
          </TabsList>
        </Tabs>
      </header>
      <div className={cn("min-h-0 flex-1 overflow-y-auto p-6", tab !== "files" && tab !== "logs" && "[&_section]:mb-6")}>
        {tab === "overview" && <OverviewTab snap={snap} device={device} notices={notices} state={state} services={services} now={now} onPromote={onPromote} />}
        {tab === "nodes" && <NodesTab snap={snap} state={state} found={found} selected={nodeId} onState={onState} />}
        {tab === "services" && <ServicesTab snap={snap} state={state} services={services} onAct={onAct} now={now} />}
        {tab === "storage" && <StorageTab snap={snap} state={state} />}
        {tab === "files" && <FilesTab snap={snap} state={state} initial={files} services={services} />}
        {tab === "logs" && <LogsTab snap={snap} state={state} initial={logs} />}
        {tab === "devices" && <DevicesTab snap={snap} device={device} found={found} onPromote={onPromote} scoped={scoped} />}
        {tab === "updates" && <UpdatesTab snap={snap} state={state} />}
        {tab === "remote" && <RemoteTab snap={snap} state={state} />}
      </div>
    </div>
  );
}

