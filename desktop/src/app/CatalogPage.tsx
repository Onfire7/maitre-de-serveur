// The Catalog as a card grid (decision 139) with the install sheet (151).
// Step 3 fills the sheet's Options disclosure; here it asks name and Node.
import { useMemo, useState } from "react";
import { Search, ChevronRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import type { ServiceSpec } from "@/gen/maitre/v1/service_pb";
import { cn } from "@/lib/utils";
import bundledCatalog from "@/catalog/index.json";
import { specFor, type CatalogEntry, type CatalogIndex } from "@/services";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { ServiceIcon } from "./status";

const catalog = bundledCatalog as CatalogIndex;

export function CatalogPage({ snap, onInstall }: { snap: FleetSnapshot | undefined; onInstall: (entry: CatalogEntry, spec: ServiceSpec, nodeId: string) => Promise<void> }) {
  const [q, setQ] = useState("");
  const [cat, setCat] = useState<string>("all");
  // `?install=<catalog id>` opens the sheet on load (screenshot fixture).
  const [picked, setPicked] = useState<CatalogEntry | null>(() => catalog.entries.find((e) => e.id === new URLSearchParams(window.location.search).get("install")) ?? null);
  const categories = useMemo(() => ["all", ...Array.from(new Set(catalog.entries.map((e) => e.category)))], []);
  const entries = catalog.entries.filter((e) => (cat === "all" || e.category === cat) && (!q || `${e.name} ${e.summary}`.toLowerCase().includes(q.toLowerCase())));
  const installed = new Set(snap?.specs.map((s) => s.catalogId));
  return (
    <div className="mx-auto max-w-5xl p-6">
      <div className="mb-5 flex flex-wrap items-center gap-3">
        <h1 className="text-xl font-semibold">Add a Service</h1>
        <div className="relative ml-auto w-64">
          <Search className="pointer-events-none absolute left-2.5 top-2.5 size-4 text-muted-foreground" />
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search the Catalog" className="pl-8" />
        </div>
      </div>
      <div className="mb-4 flex flex-wrap gap-1.5">
        {categories.map((c) => (
          <button key={c} onClick={() => setCat(c)} className={cn("rounded-full border px-3 py-1 text-xs capitalize", cat === c ? "border-primary bg-primary text-primary-foreground" : "hover:bg-accent")}>
            {c}
          </button>
        ))}
      </div>
      <div className="grid grid-cols-[repeat(auto-fill,minmax(240px,1fr))] gap-3">
        {entries.map((e) => (
          <button key={e.id} onClick={() => setPicked(e)} className="flex flex-col items-start gap-2 rounded-xl border bg-card p-4 text-left transition-colors hover:border-foreground/30 hover:bg-accent/40">
            <div className="flex w-full items-center gap-3">
              <ServiceIcon catalogId={e.id} word="Online" size={40} />
              <div className="min-w-0">
                <div className="truncate font-medium">{e.name}</div>
                <div className="text-xs text-muted-foreground">
                  v{e.version}
                  {installed.has(e.id) && " · installed"}
                </div>
              </div>
            </div>
            <p className="line-clamp-2 text-sm text-muted-foreground">{e.summary}</p>
            <div className="mt-auto flex gap-1">
              <Badge variant="secondary" className="capitalize">
                {e.category}
              </Badge>
              {e.infrastructure && <Badge variant="outline">infrastructure</Badge>}
              {!e.web && <Badge variant="outline">headless</Badge>}
            </div>
          </button>
        ))}
        {entries.length === 0 && <p className="text-sm text-muted-foreground">Nothing in the Catalog matches.</p>}
      </div>
      <InstallSheet entry={picked} snap={snap} onClose={() => setPicked(null)} onInstall={onInstall} />
    </div>
  );
}

function InstallSheet({ entry, snap, onClose, onInstall }: { entry: CatalogEntry | null; snap: FleetSnapshot | undefined; onClose: () => void; onInstall: (entry: CatalogEntry, spec: ServiceSpec, nodeId: string) => Promise<void> }) {
  const nodes = snap?.nodes ?? [];
  const spaces = snap?.spaces ?? [];
  const defaultSpace = spaces.find((s) => s.defaultForNew);
  const [name, setName] = useState("");
  const [nodeId, setNodeId] = useState("");
  const [volumeSpaces, setVolumeSpaces] = useState<Record<string, string>>({});
  const [exposure, setExposure] = useState("private");
  const [autoUpdate, setAutoUpdate] = useState(true);
  const [forward, setForward] = useState(false);
  const [settings, setSettings] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const taken = new Set(snap?.specs.map((s) => s.name));
  const effName = (name || entry?.id || "").trim();
  const effNode = nodeId || nodes[0]?.nodeId || "";
  const reset = () => {
    setName("");
    setNodeId("");
    setVolumeSpaces({});
    setExposure("private");
    setAutoUpdate(true);
    setForward(false);
    setSettings({});
    setError("");
  };
  const settingValue = (key: string, def: string) => settings[key] ?? def;
  return (
    <Sheet
      open={!!entry}
      onOpenChange={(o) => {
        if (!o) {
          onClose();
          reset();
        }
      }}
    >
      <SheetContent className="flex w-[440px] flex-col sm:max-w-[440px]">
        {entry && (
          <>
            <SheetHeader className="pr-10">
              <div className="flex items-center gap-3">
                <ServiceIcon catalogId={entry.id} word="Online" size={44} />
                <div>
                  <SheetTitle>Install {entry.name}</SheetTitle>
                  <SheetDescription>{entry.summary}</SheetDescription>
                </div>
              </div>
            </SheetHeader>
            <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-4 pb-4">
              {entry.infrastructure && <p className="rounded-md border border-status-attention/60 bg-status-attention/10 p-2 text-xs">Other devices on your network will depend on this. The Node keeps a fixed address and starts it before anything else. It needs ports {entry.host_ports?.join(", ")} on the Node.</p>}
              <div className="space-y-1.5">
                <Label htmlFor="svc-name">Name</Label>
                <Input id="svc-name" value={name} onChange={(e) => setName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ""))} placeholder={entry.id} />
                <p className="text-xs text-muted-foreground">Reachable on your network as {effName || entry.id}.local</p>
                {taken.has(effName) && <p className="text-xs text-destructive">A Service named {effName} already exists.</p>}
              </div>
              {nodes.length > 1 && (
                <div className="space-y-1.5">
                  <Label>Node</Label>
                  <RadioGroup value={effNode} onValueChange={setNodeId} className="gap-1.5">
                    {nodes.map((n) => (
                      <label key={n.nodeId} className={cn("flex cursor-pointer items-center gap-2 rounded-md border px-3 py-2 text-sm", effNode === n.nodeId && "border-primary")}>
                        <RadioGroupItem value={n.nodeId} /> {n.name}
                      </label>
                    ))}
                  </RadioGroup>
                </div>
              )}
              {entry.volumes.length > 0 && (
                <div className="space-y-1.5">
                  <Label>Storage</Label>
                  {spaces.length === 0 ? (
                    <p className="text-sm text-destructive">No Storage Space exists yet; create one on the Storage page first.</p>
                  ) : spaces.length === 1 ? (
                    <p className="text-sm text-muted-foreground">
                      {entry.volumes.length === 1 ? "Its data goes" : `Its ${entry.volumes.length} volumes go`} in <b className="text-foreground">{spaces[0].name}</b>.
                    </p>
                  ) : (
                    <div className="space-y-1.5">
                      {entry.volumes.map((v) => (
                        <div key={v.name} className="flex items-center gap-2 text-sm">
                          <span className="w-28 shrink-0 truncate text-muted-foreground">
                            {v.name}
                            {v.size ? ` (${v.size})` : ""}
                          </span>
                          <Select value={volumeSpaces[v.name] ?? defaultSpace?.spaceId ?? ""} onValueChange={(val) => setVolumeSpaces({ ...volumeSpaces, [v.name]: val })}>
                            <SelectTrigger size="sm" className="flex-1">
                              <SelectValue placeholder="Space" />
                            </SelectTrigger>
                            <SelectContent>
                              {spaces.map((s) => (
                                <SelectItem key={s.spaceId} value={s.spaceId}>
                                  {s.name}
                                  {s.defaultForNew ? " (default)" : ""}
                                </SelectItem>
                              ))}
                            </SelectContent>
                          </Select>
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              )}
              <details className="group rounded-lg border" open={!!entry.settings?.length}>
                <summary className="flex cursor-pointer items-center gap-1 px-3 py-2 text-sm font-medium">
                  <ChevronRight className="size-4 transition-transform group-open:rotate-90" /> Options
                </summary>
                <div className="space-y-4 border-t px-3 py-3">
                  {entry.web && !entry.infrastructure && (
                    <div className="space-y-1.5">
                      <Label>Reachable</Label>
                      <Select value={exposure} onValueChange={setExposure}>
                        <SelectTrigger size="sm">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="private">at home only</SelectItem>
                          <SelectItem value="remote">from anywhere, for you (Tailscale)</SelectItem>
                        </SelectContent>
                      </Select>
                      {exposure === "remote" && <p className="text-xs text-muted-foreground">Needs the Fleet connected to Tailscale (Remote tab). Public exposure is set from the Service's menu after install.</p>}
                    </div>
                  )}
                  <label className="flex items-center justify-between gap-3 text-sm">
                    <span>
                      Keep it up to date by itself
                      <span className="block text-xs text-muted-foreground">Daily; the previous version stays one click away.</span>
                    </span>
                    <Switch checked={autoUpdate} onCheckedChange={setAutoUpdate} />
                  </label>
                  {entry.port_forward && (
                    <label className="flex items-center justify-between gap-3 text-sm">
                      <span>
                        Reachable from the internet
                        <span className="block text-xs text-muted-foreground">The Node asks your router to forward {entry.host_ports?.join(", ")}. Anyone on the internet can then reach it.</span>
                      </span>
                      <Switch checked={forward} onCheckedChange={setForward} />
                    </label>
                  )}
                  {(entry.settings ?? []).map((s) => (
                    <div key={s.key} className="space-y-1.5">
                      {s.type === "bool" ? (
                        <label className="flex items-center justify-between gap-3 text-sm">
                          <span>{s.label}</span>
                          <Switch checked={settingValue(s.key, s.default ?? "") === "true"} onCheckedChange={(v) => setSettings({ ...settings, [s.key]: String(v) })} />
                        </label>
                      ) : (
                        <>
                          <Label htmlFor={`set-${s.key}`}>{s.label}</Label>
                          <Input id={`set-${s.key}`} type={s.type === "password" ? "password" : s.type === "number" ? "number" : "text"} value={settingValue(s.key, s.default ?? "")} onChange={(e) => setSettings({ ...settings, [s.key]: e.target.value })} placeholder={s.type === "timezone" ? "auto = the Node's time zone" : ""} />
                        </>
                      )}
                    </div>
                  ))}
                  {entry.own_login && <p className="text-xs text-muted-foreground">{entry.name} has its own accounts; you set one up the first time you open it.</p>}
                </div>
              </details>
              {error && <p className="text-sm text-destructive">{error}</p>}
            </div>
            <SheetFooter>
              <Button
                disabled={busy || !effName || taken.has(effName) || !effNode || !defaultSpace}
                onClick={async () => {
                  setBusy(true);
                  setError("");
                  try {
                    const spec = specFor(entry, effName, Object.fromEntries((entry.settings ?? []).map((s) => [s.key, settingValue(s.key, s.default ?? "")])));
                    spec.exposure = entry.web && !entry.infrastructure ? exposure : "private";
                    spec.autoUpdate = autoUpdate;
                    spec.forwardPorts = !!entry.port_forward && forward;
                    spec.volumeSpaces = Object.fromEntries(entry.volumes.map((v) => [v.name, volumeSpaces[v.name] ?? defaultSpace?.spaceId ?? ""]));
                    await onInstall(entry, spec, effNode);
                    onClose();
                    reset();
                  } catch (e) {
                    setError(String((e as Error).message ?? e));
                  } finally {
                    setBusy(false);
                  }
                }}
              >
                {busy ? "Installing…" : "Install"}
              </Button>
              {!defaultSpace && <p className="text-xs text-destructive">Create a Storage Space and mark it as the default first.</p>}
            </SheetFooter>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}

export { specFor };
