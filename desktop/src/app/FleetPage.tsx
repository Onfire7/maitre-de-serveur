// The Fleet page (decisions 132, 148-150): one route, seven tabs. The tabs
// not yet redesigned embed the existing panels; step 4 migrates them.
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Badge } from "@/components/ui/badge";
import { CircleAlert, Info, Laptop, FolderOpen } from "lucide-react";
import { cn } from "@/lib/utils";
import type { FleetSnapshot } from "@/fleet/useFleet";
import type { ConsoleState, DeviceInfo, Found } from "@/state";
import { SpacesPanel } from "@/spaces";
import { AskingDevices } from "@/join";
import { UpdatePolicyPanel } from "@/policy";
import { TailscalePanel } from "@/tailscale";
import { FLEET_TABS, FLEET_TAB_LABEL, useRoute, type FleetTab } from "./routes";
import { NodesTab } from "./NodesTab";
import type { Notice } from "./notices";

export function FleetPage({
  tab,
  nodeId,
  snap,
  state,
  device,
  found,
  notices,
  onState,
  onPromote,
}: {
  tab: FleetTab;
  nodeId?: string;
  snap: FleetSnapshot | undefined;
  state: ConsoleState;
  device: DeviceInfo;
  found: Found[];
  notices: Notice[];
  onState: (s: ConsoleState) => Promise<void>;
  onPromote: () => void;
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
      <div className="min-h-0 flex-1 overflow-y-auto p-6 [&_section]:mb-6">
        {tab === "overview" && <Overview snap={snap} device={device} notices={notices} state={state} onPromote={onPromote} />}
        {tab === "nodes" && <NodesTab snap={snap} state={state} found={found} selected={nodeId} onState={onState} />}
        {tab === "storage" && (
          <div className="legacy">
            <SpacesPanel state={state} />
          </div>
        )}
        {tab === "files" && <FilesTab snap={snap} />}
        {tab === "devices" && (
          <div className="space-y-6">
            <section>
              <h2 className="mb-2 text-base font-semibold">Trusted Devices</h2>
              <ul className="divide-y rounded-lg border">
                {(snap?.devices ?? []).map((d) => (
                  <li key={d.deviceId} className="flex items-center gap-3 px-3 py-2 text-sm">
                    <Laptop className="size-4 text-muted-foreground" />
                    <span className="font-medium">{d.name}</span>
                    {d.deviceId === device.fingerprint && <Badge variant="secondary">this device</Badge>}
                    {d.recoveryOnly && <Badge variant="outline">recovery only</Badge>}
                    {d.nodeScope && <Badge variant="outline">one Node only</Badge>}
                    <span className="ml-auto text-xs text-muted-foreground">added {new Date(Number(d.addedUnixMs)).toLocaleDateString()}</span>
                  </li>
                ))}
              </ul>
            </section>
            <div className="legacy">
              <AskingDevices state={state} found={found} />
            </div>
          </div>
        )}
        {tab === "updates" && (
          <div className="legacy">
            <UpdatePolicyPanel state={state} />
          </div>
        )}
        {tab === "remote" && (
          <div className="legacy">
            <TailscalePanel state={state} />
          </div>
        )}
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

function FilesTab({ snap }: { snap: FleetSnapshot | undefined }) {
  return (
    <div className="max-w-2xl space-y-4">
      <p className="text-sm text-muted-foreground">Browse and move files between Storage Spaces and Drives. The file manager arrives with the Storage screens (build order step 5); the places it will open are already here.</p>
      <ul className="divide-y rounded-lg border">
        {(snap?.spaces ?? []).map((s) => (
          <li key={s.spaceId} className="flex items-center gap-3 px-3 py-2 text-sm">
            <FolderOpen className="size-4 text-muted-foreground" /> <span className="font-medium">{s.name}</span>
            <span className="text-xs text-muted-foreground">Space{s.spanNodes ? " · spans Nodes" : ""}</span>
          </li>
        ))}
        {(snap?.drives ?? []).map((d) => (
          <li key={d.driveId + d.nodeId} className="flex items-center gap-3 px-3 py-2 text-sm">
            <FolderOpen className="size-4 text-muted-foreground" /> <span className="font-medium">{d.name}</span>
            <span className="text-xs text-muted-foreground">Drive on {snap?.nodes.find((n) => n.nodeId === d.nodeId)?.name ?? "a Node"}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
