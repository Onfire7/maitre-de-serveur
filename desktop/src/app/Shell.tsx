// The Console's shell once a Fleet exists: sidebar, main area, notices
// (build order step 2). Data comes through the anchor Node (useFleet);
// per-Service calls go straight to the owning Node.
import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AnimatePresence, motion } from "motion/react";
import { toast } from "sonner";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { createClient } from "@connectrpc/connect";
import { fromBinary } from "@bufbuild/protobuf";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { TrustedDeviceSchema } from "@/gen/maitre/v1/fleet_pb";
import { transportFor } from "@/transport";
import { api, type ConsoleState, type DeviceInfo, type Found, type KnownNode } from "@/state";
import { useFleet, useFleetServices, type FleetService } from "@/fleet/useFleet";
import { controlService, removeService, services as serviceClient, targetFor } from "@/fleet/actions";
import { useIntroduce } from "@/fleet/introduce";
import { specFor, type CatalogEntry } from "@/services";
import { AddNode } from "@/App";
import { CodeScreen } from "@/join";
import { Sidebar, sidebarNodes } from "./Sidebar";
import type { FlyoutItem } from "./ServiceFlyout";
import { ServicePage } from "./ServicePage";
import { CatalogPage } from "./CatalogPage";
import { FleetPage } from "./FleetPage";
import { PreferencesDialog } from "./Preferences";
import { LogsSheet } from "./LogsSheet";
import { notices as computeNotices } from "./notices";
import { useRoute } from "./routes";

const COLLAPSE_KEY = "maitre.sidebar";
const AUTO_COLLAPSE_PX = 900;

export function Shell({ device, state, save }: { device: DeviceInfo; state: ConsoleState; save: (s: ConsoleState) => Promise<void> }) {
  const { route, go } = useRoute();
  const fleet = useFleet(state.nodes);
  const snap = fleet.data;
  // A coarse clock for staleness words, so derived lists keep their identity between renders.
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 5000);
    return () => clearInterval(t);
  }, []);
  const services = useFleetServices(snap, now);
  useIntroduce(state.nodes, (nodes) => save({ ...state, nodes }));

  const discover = useQuery({ queryKey: ["discover"], queryFn: () => api.discover(), refetchInterval: 2000, retry: false });
  const found: Found[] = discover.data ?? [];

  // Decision 120: this device may be trusted by one Node only so far.
  const scope = useQuery({
    queryKey: ["scope", snap?.anchor.addr, device.fingerprint],
    enabled: !!snap,
    refetchInterval: 30_000,
    queryFn: async () => {
      const recs = (await createClient(NodeService, transportFor({ addr: snap!.anchor.addr, fingerprint: snap!.anchor.nodeId })).listRecords({ kind: "trusted_device" })).records.filter((r) => !r.deleted);
      const me = recs.map((r) => fromBinary(TrustedDeviceSchema, r.body)).find((d) => d.deviceId === device.fingerprint);
      if (!me?.nodeScope) return null;
      return { nodeName: snap!.nodes.find((n) => n.nodeId === me.nodeScope)?.name ?? "one Node", until: Number(me.addedUnixMs) + 24 * 3600_000 };
    },
  });
  const noticeList = useMemo(() => computeNotices(snap, found, scope.data ?? null, now), [snap, found, scope.data, now]);

  // Sidebar: collapsed by hand (persisted) or by a narrow window (not persisted).
  const [collapsedPref, setCollapsedPref] = useState(() => localStorage.getItem(COLLAPSE_KEY) === "1");
  const [narrow, setNarrow] = useState(() => window.innerWidth < AUTO_COLLAPSE_PX);
  useEffect(() => {
    const on = () => setNarrow(window.innerWidth < AUTO_COLLAPSE_PX);
    window.addEventListener("resize", on);
    return () => window.removeEventListener("resize", on);
  }, []);
  const collapsed = collapsedPref || narrow;
  const toggle = () => {
    const next = !collapsed;
    setCollapsedPref(next);
    localStorage.setItem(COLLAPSE_KEY, next ? "1" : "0");
  };
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "b") {
        e.preventDefault();
        toggle();
      }
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  });

  // Decision 134/147: present once a Node exists or one is being set up.
  const present = state.nodes.length > 0 || route.name === "add-node";

  const [prefs, setPrefs] = useState(false);
  const [logsOf, setLogsOf] = useState<string | null>(null);
  const [removing, setRemoving] = useState<FleetService | null>(null);
  const [deleteData, setDeleteData] = useState(false);

  const target = (s: FleetService) => targetFor(snap, state.nodes, s.spec.nodeId);

  async function act(s: FleetService, item: FlyoutItem) {
    const t = target(s);
    switch (item) {
      case "open":
        if (s.status?.url) window.open(s.status.url, "_blank");
        return;
      case "logs":
        setLogsOf(s.spec.name);
        return;
      case "remove":
        setRemoving(s);
        return;
      case "exposure":
      case "ports":
      case "storage":
        go({ name: "fleet", tab: "nodes", nodeId: s.spec.nodeId });
        toast(`${item === "exposure" ? "Exposure" : item === "ports" ? "Port forwarding" : "Storage"} for ${s.spec.name} is in the Node's details until step 3 of the overhaul.`);
        return;
      default:
        if (!t) return toast.error(`No address for ${s.node?.name ?? "the Node"}`);
        try {
          await controlService(t, s.spec.name, item);
          toast.success(`${s.spec.name}: ${item === "update" ? "updating" : item === "rollback" ? "rolling back" : item === "restart" ? "restarting" : item === "stop" ? "stopped" : "starting"}`);
          fleet.refresh();
        } catch (e) {
          toast.error(String((e as Error).message ?? e));
        }
    }
  }

  async function install(entry: CatalogEntry, name: string, nodeId: string) {
    const t = targetFor(snap, state.nodes, nodeId);
    if (!t) throw new Error("that Node has no address");
    const spec = specFor(entry, name, {});
    await serviceClient(t).installService({ spec });
    toast.success(`${name} is installing`);
    await fleet.refresh();
    go({ name: "service", service: name });
  }

  const current = route.name === "service" ? services.find((s) => s.spec.name === route.service) : undefined;
  const addFound = route.name === "add-node" ? (found.find((f) => f.kind === "installer" && (f.id === route.installerId || f.addresses[0] === route.addr)) ?? { kind: "installer" as const, id: route.installerId, name: "", machine: "", fleet_id: "", version: "", addresses: [route.addr], key_pem: "", recovery: false }) : undefined;

  return (
    <TooltipProvider delayDuration={400}>
      <div className="flex h-screen w-screen overflow-hidden bg-background text-foreground">
        <AnimatePresence initial={false}>
          {present && (
            <Sidebar
              key="sidebar"
              services={services}
              nodes={sidebarNodes(snap, now)}
              fleetName={state.fleet?.name ?? ""}
              badge={noticeList.length}
              collapsed={collapsed}
              onToggle={toggle}
              canAddService={!!snap?.spaces.some((s) => s.defaultForNew)}
              onFlyoutItem={act}
              onPreferences={() => setPrefs(true)}
              provisioning={route.name === "add-node" ? { name: "New Node" } : undefined}
            />
          )}
        </AnimatePresence>
        <main className="relative min-w-0 flex-1">
          <AnimatePresence mode="wait" initial={false}>
            <motion.div key={routeKey(route)} initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.12 }} className="absolute inset-0">
              {route.name === "service" &&
                (current ? (
                  <ServicePage service={current} onAction={(a) => act(current, a)} />
                ) : (
                  <div className="flex h-full items-center justify-center text-sm text-muted-foreground">{snap ? `There is no Service named ${route.service}.` : "Reaching the Fleet…"}</div>
                ))}
              {route.name === "catalog" && <CatalogPage snap={snap} onInstall={install} />}
              {route.name === "fleet" && <FleetPage tab={route.tab} nodeId={route.nodeId} snap={snap} state={state} device={device} found={found} notices={noticeList} onState={save} onPromote={() => go({ name: "code" })} />}
              {route.name === "add-node" && addFound && (
                <div className="legacy h-full overflow-y-auto p-6">
                  <AddNode
                    found={addFound}
                    device={device}
                    state={state}
                    onCancel={() => go({ name: "fleet", tab: "nodes" })}
                    onProvisioned={async (node: KnownNode) => {
                      await save({ ...state, nodes: [...state.nodes.filter((n) => n.node_id !== node.node_id), node] });
                      go({ name: "fleet", tab: "nodes", nodeId: node.node_id });
                    }}
                  />
                </div>
              )}
              {route.name === "code" && (
                <div className="legacy h-full overflow-y-auto p-6">
                  <CodeScreen
                    device={device}
                    promote
                    onJoined={async (s) => {
                      await save(s);
                      go({ name: "fleet", tab: "devices" });
                    }}
                    onCancel={() => go({ name: "fleet", tab: "devices" })}
                  />
                </div>
              )}
            </motion.div>
          </AnimatePresence>
          {fleet.error && state.nodes.length > 0 && <div className="absolute inset-x-0 top-0 z-10 bg-status-attention/90 px-4 py-1 text-center text-xs text-black">No Node answers. {String((fleet.error as Error).message ?? fleet.error)}</div>}
        </main>
      </div>
      <PreferencesDialog open={prefs} onClose={() => setPrefs(false)} device={device} onAddDev={(n) => save({ ...state, nodes: [...state.nodes.filter((x) => x.node_id !== n.node_id), n] })} />
      <LogsSheet name={logsOf} target={logsOf ? targetFor(snap, state.nodes, services.find((s) => s.spec.name === logsOf)?.spec.nodeId ?? "") : undefined} onClose={() => setLogsOf(null)} />
      <AlertDialog open={!!removing} onOpenChange={(o) => !o && (setRemoving(null), setDeleteData(false))}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove {removing?.spec.name}?</AlertDialogTitle>
            <AlertDialogDescription>It stops and its page goes away. Its data stays in its Storage Space unless you also delete it.</AlertDialogDescription>
          </AlertDialogHeader>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={deleteData} onChange={(e) => setDeleteData(e.target.checked)} /> Also delete its data
          </label>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={async () => {
                const s = removing!;
                const t = target(s);
                if (!t) return toast.error("that Node has no address");
                try {
                  await removeService(t, s.spec.name, deleteData);
                  toast(`${s.spec.name} was removed`);
                  if (route.name === "service" && route.service === s.spec.name) go({ name: "fleet", tab: "overview" });
                  fleet.refresh();
                } catch (e) {
                  toast.error(String((e as Error).message ?? e));
                }
              }}
            >
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <Toaster position="bottom-right" />
    </TooltipProvider>
  );
}

function routeKey(r: ReturnType<typeof useRoute>["route"]) {
  switch (r.name) {
    case "service":
      return `service:${r.service}`;
    case "fleet":
      return `fleet:${r.tab}`;
    default:
      return r.name;
  }
}
