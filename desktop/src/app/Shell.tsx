// The Console's shell once a Fleet exists: sidebar, main area, notices
// (build order step 2). Data comes through the anchor Node (useFleet);
// per-Service calls go straight to the owning Node.
import { openUrl } from "@tauri-apps/plugin-opener";
import { useEffect, useRef, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AnimatePresence, motion } from "motion/react";
import { toast } from "sonner";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { GrantsSheet, PortForwardSheet, type SheetKind } from "./ServiceSheets";
import { GrantDialogHost } from "./GrantDialog";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { createClient } from "@connectrpc/connect";
import { fromBinary } from "@bufbuild/protobuf";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { TrustedDeviceSchema } from "@/gen/maitre/v1/fleet_pb";
import { transportFor } from "@/transport";
import { api, type ConsoleState, type DeviceInfo, type Found, type KnownNode } from "@/state";
import { gapLine, nodeDown, nodeWord, syncGaps, useFleet, useFleetServices, type FleetService } from "@/fleet/useFleet";
import { controlService, removeService, services as serviceClient, targetFor } from "@/fleet/actions";
import { assignOwners } from "@/fleet/owners";
import { declined } from "@/fleet/grants";
import { useIntroduce } from "@/fleet/introduce";
import type { CatalogEntry } from "@/services";
import type { ServiceSpec } from "@/gen/maitre/v1/service_pb";
import { AddNode, type Provisioning } from "./AddNode";
import { CodeScreen } from "./FirstRun";
import { Sidebar, sidebarNodes } from "./Sidebar";
import type { FlyoutItem } from "./ServiceFlyout";
import { ServicePage } from "./ServicePage";
import { MoveServiceDialog } from "./MoveServiceDialog";
import { CatalogPage } from "./CatalogPage";
import { FleetPage } from "./FleetPage";
import { recordMetrics } from "@/fleet/history";
import { PreferencesDialog } from "./Preferences";
import { notices as computeNotices } from "./notices";
import { useRoute } from "./routes";

const COLLAPSE_KEY = "maitre.sidebar";
const AUTO_COLLAPSE_PX = 900;

export function Shell({ device, state, save }: { device: DeviceInfo; state: ConsoleState; save: (s: ConsoleState) => Promise<void> }) {
  const { route, go } = useRoute();
  const fleet = useFleet(state.nodes);
  const snap = fleet.data;
  // The Overview's trend lines (209) follow every sample, whichever tab is open.
  useEffect(() => recordMetrics(snap), [snap]);
  // Decision 169: the anchor holds older copies than another Node. Say so
  // and offer to read through that Node; never switch on our own.
  const behind = snap ? syncGaps(snap).find((g) => g.nodeId === snap.anchor.nodeId && !nodeDown(nodeWord(snap.status.get(g.aheadNodeId), Date.now()))) : undefined;
  const nameOf = (id: string) => snap?.nodes.find((n) => n.nodeId === id)?.name ?? id.slice(0, 8);
  // A coarse clock for staleness words, so derived lists keep their identity between renders.
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 5000);
    return () => clearInterval(t);
  }, []);
  const services = useFleetServices(snap, now);
  useIntroduce(state.nodes, (nodes) => save({ ...state, nodes }));
  // Decision 182: Spaces made before owners existed get one, once.
  const owning = useRef(new Set<string>());
  useEffect(() => {
    const missing = (snap?.spaces ?? []).filter((sp) => !sp.ownerId && !owning.current.has(sp.spaceId));
    if (!snap || missing.length === 0) return;
    missing.forEach((sp) => owning.current.add(sp.spaceId));
    assignOwners({ addr: snap.anchor.addr, fingerprint: snap.anchor.nodeId }, snap.spaces, snap.records).catch(() => missing.forEach((sp) => owning.current.delete(sp.spaceId)));
  }, [snap?.spaces]);

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
  const [provisioning, setProvisioning] = useState<Provisioning | null>(null);
  const present = state.nodes.length > 0 || provisioning !== null;

  const [prefs, setPrefs] = useState(false);
  const [removing, setRemoving] = useState<FleetService | null>(null);
  const [moving, setMoving] = useState<FleetService | null>(null);
  const [deleteData, setDeleteData] = useState(false);
  const [typed, setTyped] = useState("");
  // `?sheet=ports:minecraft` opens a sheet on load (screenshot fixture).
  const [sheet, setSheet] = useState<{ kind: SheetKind; service: string } | null>(() => {
    const v = new URLSearchParams(window.location.search).get("sheet")?.split(":");
    return v && ["ports", "grants"].includes(v[0]) && v[1] ? { kind: v[0] as SheetKind, service: v[1] } : null;
  });
  const sheetService = sheet ? services.find((s) => s.spec.name === sheet.service) : undefined;

  const target = (s: FleetService) => targetFor(snap, state.nodes, s.spec.nodeId);

  async function act(s: FleetService, item: FlyoutItem) {
    const t = target(s);
    switch (item) {
      case "open":
        // window.open does nothing in the Tauri window; the opener plugin
        // hands the address to the system browser. A plain browser (the
        // demo) has no plugin.
        if (s.status?.url) openUrl(s.status.url).catch(() => window.open(s.status!.url, "_blank"));
        return;
      case "logs":
        // Service logs live on the Logs tab, at full width (187).
        go({ name: "fleet", tab: "logs", logs: { nodeId: s.spec.nodeId, source: `service:${s.spec.name}` } });
        return;
      case "remove":
        setRemoving(s);
        return;
      case "move":
        setMoving(s);
        return;
      case "access":
      case "storage":
        // Changed in place, in the Service's open row (194).
        go({ name: "fleet", tab: "services", open: { service: s.spec.name, edit: item } });
        return;
      case "ports":
      case "grants":
        setSheet({ kind: item, service: s.spec.name });
        return;
      default:
        if (!t) return toast.error(`No address for ${s.node?.name ?? "the Node"}`);
        try {
          await controlService(t, s.spec.name, item);
          toast.success(`${s.spec.name}: ${item === "update" ? "updating" : item === "rollback" ? "rolling back" : item === "restart" ? "restarting" : item === "stop" ? "stopped" : "starting"}`);
          fleet.refresh();
        } catch (e) {
          if (!declined(e)) toast.error(String((e as Error).message ?? e));
        }
    }
  }

  async function install(_entry: CatalogEntry, spec: ServiceSpec, nodeId: string) {
    const t = targetFor(snap, state.nodes, nodeId);
    if (!t) throw new Error("that Node has no address");
    await serviceClient(t).installService({ spec });
    toast.success(`${spec.name} is installing`);
    await fleet.refresh();
    go({ name: "service", service: spec.name });
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
              snap={snap}
              nodes={sidebarNodes(snap, now)}
              fleetName={state.fleet?.name ?? ""}
              badge={noticeList.length}
              collapsed={collapsed}
              onToggle={toggle}
              canAddService={!!snap?.spaces.some((s) => s.defaultForNew)}
              onFlyoutItem={act}
              onPreferences={() => setPrefs(true)}
              provisioning={provisioning ?? undefined}
            />
          )}
        </AnimatePresence>
        <main className="flex min-w-0 flex-1 flex-col">
          {fleet.error && state.nodes.length > 0 && <div className="shrink-0 bg-status-attention/90 px-4 py-1 text-center text-xs text-black">No Node answers. {String((fleet.error as Error).message ?? fleet.error)}</div>}
          {behind && (
            <div className="flex shrink-0 items-center justify-center gap-3 bg-status-attention/90 px-4 py-1 text-xs text-black">
              <span>
                You are reading the Fleet through {nameOf(behind.nodeId)}, which is behind {nameOf(behind.aheadNodeId)} on {gapLine(behind)}. What you see may be out of date.
              </span>
              <button className="rounded border border-black/40 px-2 py-0.5 font-medium hover:bg-black/10" onClick={() => fleet.readThrough(behind.aheadNodeId)}>
                Read through {nameOf(behind.aheadNodeId)}
              </button>
            </div>
          )}
          <div className="relative min-h-0 flex-1">
          <AnimatePresence mode="wait" initial={false}>
            <motion.div key={routeKey(route)} initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.12 }} className="absolute inset-0">
              {route.name === "service" &&
                (current ? (
                  <ServicePage service={current} target={target(current)} onAction={(a) => act(current, a)} />
                ) : (
                  <div className="flex h-full items-center justify-center text-sm text-muted-foreground">{snap ? `There is no Service named ${route.service}.` : "Reaching the Fleet…"}</div>
                ))}
              {route.name === "catalog" && <CatalogPage snap={snap} onInstall={install} />}
              {route.name === "fleet" && <FleetPage tab={route.tab} nodeId={route.nodeId} files={route.files} logs={route.logs} snap={snap} state={state} device={device} found={found} notices={noticeList} onState={save} onPromote={() => go({ name: "code" })} scoped={!!scope.data} services={services} onAct={act} now={now} />}
              {route.name === "add-node" && addFound && (
                <div className="h-full overflow-y-auto">
                  <AddNode
                    found={addFound}
                    device={device}
                    state={state}
                    snap={snap}
                    onCancel={() => go({ name: "fleet", tab: "nodes" })}
                    onProgress={setProvisioning}
                    onProvisioned={async (node: KnownNode) => {
                      await save({ ...state, nodes: [...state.nodes.filter((n) => n.node_id !== node.node_id), node] });
                      go({ name: "fleet", tab: "nodes", nodeId: node.node_id });
                    }}
                  />
                </div>
              )}
              {route.name === "code" && (
                <div className="h-full overflow-y-auto">
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
          </div>
        </main>
      </div>
      <PreferencesDialog open={prefs} onClose={() => setPrefs(false)} device={device} onAddDev={(n) => save({ ...state, nodes: [...state.nodes.filter((x) => x.node_id !== n.node_id), n] })} />
      {sheetService && <PortForwardSheet service={sheetService} target={target(sheetService)} open={sheet?.kind === "ports"} onClose={() => setSheet(null)} />}
      {sheetService && <GrantsSheet service={sheetService} target={target(sheetService)} snap={snap} open={sheet?.kind === "grants"} onClose={() => setSheet(null)} />}
      <GrantDialogHost snap={snap} />
      <MoveServiceDialog service={moving} snap={snap} state={state} onClose={() => setMoving(null)} onDone={() => fleet.refresh()} />
      <AlertDialog open={!!removing} onOpenChange={(o) => !o && (setRemoving(null), setDeleteData(false), setTyped(""))}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove {removing?.spec.name}?</AlertDialogTitle>
            <AlertDialogDescription>It stops and its page goes away. Its data stays in its Storage Space unless you also delete it.</AlertDialogDescription>
          </AlertDialogHeader>
          <label className="flex items-center gap-2 text-sm">
            <Checkbox checked={deleteData} onCheckedChange={(v) => setDeleteData(v === true)} /> Also delete its data
          </label>
          {deleteData && removing && (
            <div className="space-y-2 rounded-lg border border-destructive/60 bg-destructive/5 p-3 text-sm">
              <p>
                <b>This cannot be undone.</b> Gone for good: {removing.spec.volumes.length ? removing.spec.volumes.map((v) => `${v.name} in ${snap?.spaces.find((sp) => sp.spaceId === removing.spec.volumeSpaces[v.name])?.name ?? removing.spec.volumeSpaces[v.name] ?? "its Space"}`).join(", ") : "nothing, it keeps no data"}.
              </p>
              <Label htmlFor="rm-typed">
                Type <code className="rounded bg-muted px-1">{removing.spec.name}</code> to confirm
              </Label>
              <Input id="rm-typed" value={typed} onChange={(e) => setTyped(e.target.value)} />
            </div>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              className="dark:bg-destructive"
              disabled={deleteData && typed.trim() !== removing?.spec.name}
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
              {deleteData ? "Remove and delete its data" : "Remove, keep its data"}
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
