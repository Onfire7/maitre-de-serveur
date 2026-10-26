// The Fleet page (decisions 132, 148-150): one route, seven tabs. The tabs
// not yet redesigned embed the existing panels; step 4 migrates them.
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Badge } from "@/components/ui/badge";
import { CircleAlert, Info } from "lucide-react";
import { cn } from "@/lib/utils";
import type { FleetSnapshot } from "@/fleet/useFleet";
import type { ConsoleState, DeviceInfo, Found } from "@/state";
import { FLEET_TABS, FLEET_TAB_LABEL, useRoute, type FleetTab } from "./routes";
import { NodesTab } from "./NodesTab";
import { StorageTab } from "./StorageTab";
import { UpdatesTab } from "./UpdatesTab";
import { DevicesTab } from "./DevicesTab";
import { RemoteTab } from "./RemoteTab";
import { FilesTab } from "./FilesTab";
import type { Notice } from "./notices";

export function FleetPage({
  tab,
  nodeId,
  files,
  snap,
  state,
  device,
  found,
  notices,
  onState,
  onPromote,
  scoped,
}: {
  tab: FleetTab;
  nodeId?: string;
  files?: { nodeId: string; spaceId?: string; driveId?: string; path?: string };
  snap: FleetSnapshot | undefined;
  state: ConsoleState;
  device: DeviceInfo;
  found: Found[];
  notices: Notice[];
  onState: (s: ConsoleState) => Promise<void>;
  onPromote: () => void;
  scoped: boolean;
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
      <div className={cn("min-h-0 flex-1 overflow-y-auto p-6", tab !== "files" && "[&_section]:mb-6")}>
        {tab === "overview" && <Overview snap={snap} device={device} notices={notices} state={state} onPromote={onPromote} />}
        {tab === "nodes" && <NodesTab snap={snap} state={state} found={found} selected={nodeId} onState={onState} />}
        {tab === "storage" && <StorageTab snap={snap} state={state} />}
        {tab === "files" && <FilesTab snap={snap} state={state} initial={files} />}
        {tab === "devices" && <DevicesTab snap={snap} device={device} found={found} onPromote={onPromote} scoped={scoped} />}
        {tab === "updates" && <UpdatesTab snap={snap} state={state} />}
        {tab === "remote" && <RemoteTab snap={snap} state={state} />}
      </div>
    </div>
  );
}

function Overview({ snap, device, notices, state, onPromote }: { snap: FleetSnapshot | undefined; device: DeviceInfo; notices: Notice[]; state: ConsoleState; onPromote: () => void }) {
  const { go } = useRoute();
  return (
    <div className="max-w-2xl space-y-6">
      <p className="text-sm text-muted-foreground">
        {snap ? (
          <>
            {snap.nodes.length} {snap.nodes.length === 1 ? "Node" : "Nodes"}, {snap.specs.length} {snap.specs.length === 1 ? "Service" : "Services"}, {snap.spaces.length} {snap.spaces.length === 1 ? "Storage Space" : "Storage Spaces"}. This device is <b className="text-foreground">{device.name}</b>.
          </>
        ) : state.nodes.length === 0 ? (
          "No Node answers yet."
        ) : (
          "Reaching the Fleet…"
        )}
      </p>
      {snap && (snap.nodes.length === 0 || snap.spaces.length === 0 || snap.specs.length === 0) && (
        <section>
          <h2 className="mb-2 text-base font-semibold">Getting started</h2>
          <ol className="space-y-1.5">
            {(
              [
                [snap.nodes.length > 0, "Set up a Node", "A machine booted from the Installer, on the Nodes tab.", "nodes"],
                [snap.spaces.length > 0, "Create a Storage Space", "Where Service data goes: pick the Drives, on the Storage tab.", "storage"],
                [snap.specs.length > 0, "Install a Service", "From the Catalog, with Add a Service in the sidebar.", undefined],
              ] as [boolean, string, string, FleetTab | undefined][]
            ).map(([done, title, detail, tab]) => (
              <li key={title}>
                <button disabled={done} onClick={() => (tab ? go({ name: "fleet", tab }) : go({ name: "catalog" }))} className={cn("flex w-full items-start gap-3 rounded-lg border p-3 text-left", done ? "opacity-60" : "hover:bg-accent/50")}>
                  <span className={cn("mt-0.5 inline-flex size-4 shrink-0 items-center justify-center rounded-full border text-[10px]", done && "border-status-online bg-status-online text-black")}>{done ? "✓" : ""}</span>
                  <span>
                    <span className={cn("block text-sm font-medium", done && "line-through")}>{title}</span>
                    <span className="block text-xs text-muted-foreground">{detail}</span>
                  </span>
                </button>
              </li>
            ))}
          </ol>
        </section>
      )}
      <section>
        <h2 className="mb-2 text-base font-semibold">Needs your attention</h2>
        {notices.length === 0 ? (
          <p className="text-sm text-muted-foreground">Nothing. Everything is as it should be.</p>
        ) : (
          <ul className="space-y-2">
            {notices.map((n) => (
              <li key={n.id}>
                <button onClick={() => (n.id === "scope" ? onPromote() : go({ name: "fleet", tab: n.tab, nodeId: n.nodeId }))} className={cn("flex w-full items-start gap-3 rounded-lg border p-3 text-left hover:bg-accent/50", n.severity === "attention" && "border-status-attention/60 bg-status-attention/10")}>
                  {n.severity === "attention" ? <CircleAlert className="mt-0.5 size-4 shrink-0 text-status-attention" /> : <Info className="mt-0.5 size-4 shrink-0 text-status-starting" />}
                  <span>
                    <span className="block text-sm font-medium">{n.title}</span>
                    <span className="block text-xs text-muted-foreground">{n.detail}</span>
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}
