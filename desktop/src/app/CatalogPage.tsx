// The Catalog as a card grid (decision 139) or a list (217) with the
// install sheet (151). Step 3 fills the sheet's Options disclosure; here it
// asks name and Node.
import { useEffect, useMemo, useState } from "react";
import { Search, ChevronRight, Copy, Library, LayoutGrid, List, Star } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { create } from "@bufbuild/protobuf";
import { ServiceMountSchema, type ServiceSpec } from "@/gen/maitre/v1/service_pb";
import { FoldersEditor, defaultFolders, type Folders } from "@/files/FoldersEditor";
import { folderSource, ownSpacesOn, spacesOn } from "@/files/sources";
import { cn } from "@/lib/utils";
import bundledCatalog from "@/catalog/index.json";
import { GENERATE, catalogKey, formatStars, generatedSettings, sortEntries, specFor, type CatalogEntry, type CatalogIndex, type CatalogSort } from "@/services";
import { changeCount, changesSince, lastSeen, markSeen, useCatalogs, type LoadedCatalog } from "@/fleet/catalogs";
import { CatalogsSheet, ChangesDialog } from "./UserCatalogs";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { declined } from "@/fleet/grants";
import { registerEntries, ServiceIcon } from "./status";

const catalog = bundledCatalog as CatalogIndex;
const VIEW_KEY = "maitre.catalog.view";
const SORT_KEY = "maitre.catalog.sort";
type View = "grid" | "list";

export function CatalogPage({ snap, deviceId, onInstall }: { snap: FleetSnapshot | undefined; deviceId: string; onInstall: (entry: CatalogEntry, spec: ServiceSpec, nodeId: string) => Promise<void> }) {
  const [q, setQ] = useState("");
  const [cat, setCat] = useState<string>("all");
  // Which Catalog to show: all, the project's ("maitre"), or a user's (plan C9).
  const [from, setFrom] = useState<string>("all");
  const [manage, setManage] = useState(false);
  // Cards or a list, kept on this device (217).
  const [view, setView] = useState<View>(() => (localStorage.getItem(VIEW_KEY) === "list" ? "list" : "grid"));
  const pickView = (v: View) => {
    localStorage.setItem(VIEW_KEY, v);
    setView(v);
  };
  // Most popular first by default (P5), kept on this device like the view.
  const [sort, setSort] = useState<CatalogSort>(() => (localStorage.getItem(SORT_KEY) === "name" ? "name" : "popular"));
  const pickSort = (v: CatalogSort) => {
    localStorage.setItem(SORT_KEY, v);
    setSort(v);
  };
  const [review, setReview] = useState<LoadedCatalog | null>(null);
  const user = useCatalogs(snap?.records, deviceId);
  const loaded = user.data ?? [];
  useEffect(() => {
    for (const l of loaded) {
      registerEntries(l.entries);
      // A Catalog seen for the first time is simply there; changes count from now.
      if (!l.error && !l.elsewhere && !lastSeen(l.catalog.id)) markSeen(l);
    }
  }, [user.data]);
  const all = useMemo(() => [...catalog.entries, ...loaded.flatMap((l) => l.entries)], [user.data]);
  // `?install=<catalog id>` opens the sheet on load (screenshot fixture).
  const [picked, setPicked] = useState<CatalogEntry | null>(() => catalog.entries.find((e) => e.id === new URLSearchParams(window.location.search).get("install")) ?? null);
  const categories = useMemo(() => ["all", ...Array.from(new Set(all.map((e) => e.category)))], [all]);
  const entries = sortEntries(
    all.filter((e) => (from === "all" || (e.catalog?.id ?? "maitre") === from) && (cat === "all" || e.category === cat) && (!q || `${e.name} ${e.summary} ${e.category}`.toLowerCase().includes(q.toLowerCase()))),
    sort,
  );
  const installed = new Set(snap?.specs.map((s) => s.catalogId));
  const changed = loaded.map((l) => ({ l, ch: changesSince(l) })).filter((x) => changeCount(x.ch) > 0);
  return (
    <div className="h-full overflow-y-auto">
    <div className="p-6">
      <div className="mb-5 flex flex-wrap items-center gap-3">
        <h1 className="text-xl font-semibold">Add a Service</h1>
        <span className="text-sm text-muted-foreground">
          {entries.length === all.length ? `${all.length} in the Catalog` : `${entries.length} of ${all.length}`}
        </span>
        <div className="ml-auto flex rounded-md border p-0.5 text-xs" role="group" aria-label="Sort">
          {(
            [
              ["popular", "Popular", "Most popular first: by the stars of each app's project"],
              ["name", "A–Z", "By name"],
            ] as const
          ).map(([v, label, title]) => (
            <button key={v} title={title} aria-pressed={sort === v} onClick={() => pickSort(v)} className={cn("rounded px-2 py-1", sort === v ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground")}>
              {label}
            </button>
          ))}
        </div>
        <div className="flex rounded-md border p-0.5" role="group" aria-label="View">
          {(
            [
              ["grid", LayoutGrid, "Cards"],
              ["list", List, "List"],
            ] as const
          ).map(([v, Icon, label]) => (
            <button key={v} title={label} aria-label={label} aria-pressed={view === v} onClick={() => pickView(v)} className={cn("rounded px-1.5 py-1", view === v ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground")}>
              <Icon className="size-4" />
            </button>
          ))}
        </div>
        <Button size="sm" variant="outline" onClick={() => setManage(true)}>
          <Library /> Catalogs{loaded.length ? ` (${loaded.length + 1})` : ""}
        </Button>
        <div className="relative w-64">
          <Search className="pointer-events-none absolute left-2.5 top-2.5 size-4 text-muted-foreground" />
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search the Catalog" className="pl-8" />
        </div>
      </div>
      {changed.map(({ l, ch }) => (
        <div key={l.catalog.id} className="mb-3 flex items-center gap-3 rounded-lg border border-primary/40 bg-primary/5 px-3 py-2 text-sm">
          <span className="min-w-0 flex-1">
            <b>{l.catalog.name}</b> changed since you last looked: {[ch.added.length && `${ch.added.length} new`, ch.changed.length && `${ch.changed.length} changed`, ch.removed.length && `${ch.removed.length} removed`].filter(Boolean).join(", ")}.
          </span>
          <Button size="sm" variant="outline" onClick={() => setReview(l)}>
            Review
          </Button>
        </div>
      ))}
      {loaded.length > 0 && (
        <div className="mb-2 flex flex-wrap gap-1.5" role="group" aria-label="Catalog">
          {[{ id: "all", name: "Every Catalog" }, { id: "maitre", name: "Maître Catalog" }, ...loaded.map((l) => l.catalog)].map((c) => (
            <button key={c.id} onClick={() => setFrom(c.id)} className={cn("rounded-full border px-3 py-1 text-xs", from === c.id ? "border-primary bg-primary text-primary-foreground" : "hover:bg-accent")}>
              {c.name}
            </button>
          ))}
        </div>
      )}
      <div className="mb-4 flex flex-wrap gap-1.5">
        {categories.map((c) => (
          <button key={c} onClick={() => setCat(c)} className={cn("rounded-full border px-3 py-1 text-xs capitalize", cat === c ? "border-primary bg-primary text-primary-foreground" : "hover:bg-accent")}>
            {c}
          </button>
        ))}
      </div>
      {entries.length === 0 && <p className="text-sm text-muted-foreground">Nothing in the Catalog matches.</p>}
      {view === "grid" ? (
      <div className="grid grid-cols-[repeat(auto-fill,minmax(240px,1fr))] gap-3">
        {entries.map((e) => (
          <button key={catalogKey(e)} onClick={() => setPicked(e)} className="flex flex-col items-start gap-2 rounded-xl border bg-card p-4 text-left transition-colors hover:border-foreground/30 hover:bg-accent/40">
            <div className="flex w-full items-center gap-3">
              <ServiceIcon catalogId={catalogKey(e)} word="Online" size={40} />
              <div className="min-w-0">
                <div className="truncate font-medium">{e.name}</div>
                <div className="text-xs text-muted-foreground">
                  v{e.version.replace(/^v/, "")}
                  {installed.has(catalogKey(e)) && " · installed"}
                  {e.unfinished?.length ? " · unfinished" : ""}
                  {e.stars ? <Stars n={e.stars} /> : null}
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
              {e.catalog && <Badge variant="outline">{e.catalog.name}</Badge>}
            </div>
          </button>
        ))}
      </div>
      ) : (
        <CatalogList entries={entries} installed={installed} onPick={setPicked} />
      )}
      <InstallSheet entry={picked} snap={snap} onClose={() => setPicked(null)} onInstall={onInstall} />
      <CatalogsSheet open={manage} onClose={() => setManage(false)} snap={snap} deviceId={deviceId} loaded={user.data} />
      <ChangesDialog loaded={review} changes={review ? changesSince(review) : null} onClose={() => setReview(null)} />
    </div>
    </div>
  );
}

/** A project's stars, rounded: the popularity the Catalog sorts by. */
function Stars({ n, bare }: { n: number; bare?: boolean }) {
  return (
    <span title={`${n.toLocaleString()} stars on the app's project`}>
      {bare ? "" : " · "}
      <Star className="inline size-3 -translate-y-px" aria-hidden /> {formatStars(n)}
    </span>
  );
}

/** The Catalog as rows (217): one line each, for scanning hundreds. */
function CatalogList({ entries, installed, onPick }: { entries: CatalogEntry[]; installed: Set<string | undefined>; onPick: (e: CatalogEntry) => void }) {
  if (entries.length === 0) return null;
  return (
    <div className="@container overflow-hidden rounded-xl border bg-card">
      <div className="grid grid-cols-[28px_1fr_6rem] @xl:grid-cols-[28px_minmax(9rem,13rem)_1fr_6rem] @4xl:grid-cols-[28px_minmax(10rem,14rem)_1fr_8rem_6rem_7rem] items-center gap-3 border-b bg-muted/40 px-3 py-1.5 text-xs font-medium text-muted-foreground">
        <span />
        <span>Name</span>
        <span className="hidden @xl:block">What it does</span>
        <span className="hidden @4xl:block">Category</span>
        <span>Version</span>
        <span className="hidden @4xl:block">Catalog</span>
      </div>
      <ul className="divide-y">
        {entries.map((e) => (
          <li key={catalogKey(e)} className="[content-visibility:auto] [contain-intrinsic-size:auto_41px]">
            <button onClick={() => onPick(e)} className="grid w-full grid-cols-[28px_1fr_6rem] @xl:grid-cols-[28px_minmax(9rem,13rem)_1fr_6rem] @4xl:grid-cols-[28px_minmax(10rem,14rem)_1fr_8rem_6rem_7rem] items-center gap-3 px-3 py-2 text-left text-sm hover:bg-accent/50">
              <ServiceIcon catalogId={catalogKey(e)} word="Online" size={24} />
              <span className="flex min-w-0 items-center gap-1.5">
                <span className="truncate font-medium">{e.name}</span>
                {e.stars ? (
                  <span className="shrink-0 text-xs text-muted-foreground">
                    <Stars n={e.stars} bare />
                  </span>
                ) : null}
                {installed.has(catalogKey(e)) && (
                  <Badge variant="secondary" className="shrink-0 px-1.5 text-[10px]">
                    installed
                  </Badge>
                )}
              </span>
              <span className="hidden truncate text-muted-foreground @xl:block" title={e.summary}>
                {e.summary}
                {e.unfinished?.length ? " · unfinished" : ""}
                {e.infrastructure ? " · infrastructure" : ""}
                {!e.web ? " · headless" : ""}
              </span>
              <span className="hidden truncate text-xs capitalize text-muted-foreground @4xl:block">{e.category}</span>
              <span className="truncate font-mono text-xs text-muted-foreground">v{e.version.replace(/^v/, "")}</span>
              <span className="hidden truncate text-xs text-muted-foreground @4xl:block">{e.catalog?.name ?? "Maître"}</span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

function InstallSheet({ entry, snap, onClose, onInstall }: { entry: CatalogEntry | null; snap: FleetSnapshot | undefined; onClose: () => void; onInstall: (entry: CatalogEntry, spec: ServiceSpec, nodeId: string) => Promise<void> }) {
  const nodes = snap?.nodes ?? [];
  const spaces = snap?.spaces ?? [];
  const defaultSpace = spaces.find((s) => s.defaultForNew);
  const [name, setName] = useState("");
  const [nodeId, setNodeId] = useState("");
  const [folders, setFolders] = useState<Folders | null>(null);
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
    setFolders(null);
    setExposure("private");
    setAutoUpdate(true);
    setForward(false);
    setSettings({});
    setError("");
  };
  // Passwords the entry has made up (plan C7): fresh for each entry opened.
  const generated = useMemo(() => generatedSettings(entry), [entry?.id]);
  const settingValue = (key: string, def: string) => settings[key] ?? generated[key] ?? def;
  // Only Spaces the chosen Node has (176); folders start from the Catalog's
  // hints (175) and follow a change of Node.
  const here = spacesOn(snap, effNode);
  // A Service's own folders only in Spaces with a Drive on its Node (208).
  const ownHere = ownSpacesOn(snap, effNode);
  const fallback = ownHere.find((s) => s.defaultForNew)?.spaceId ?? ownHere[0]?.spaceId ?? "";
  const eff = folders ?? (entry ? defaultFolders(entry.volumes, here, fallback) : { volumeSpaces: {}, volumeFolders: {}, mounts: [] });
  useEffect(() => setFolders(null), [entry?.id, effNode]);
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
              <div className="space-y-1.5">
                <Label>Storage</Label>
                {spaces.length === 0 ? (
                  <p className="text-sm text-destructive">No Storage Space exists yet; create one on the Storage page first.</p>
                ) : (
                  <FoldersEditor volumes={entry.volumes} spaces={here} ownSpaces={ownHere} value={eff} onChange={setFolders} source={(sp) => folderSource(snap, effNode, sp)} serviceName={effName} />
                )}
              </div>
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
                      {exposure === "remote" && <p className="text-xs text-muted-foreground">Needs the Fleet connected to Tailscale (Remote tab). Public access is set from the Service's Access after install.</p>}
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
                          {s.default === GENERATE && settings[s.key] === undefined && (
                            <p className="flex items-center gap-1 text-xs text-muted-foreground">
                              Made up for {entry.name}; it stays in the Service's settings.
                              <button type="button" className="rounded p-0.5 hover:text-foreground" aria-label={`Copy ${s.label}`} onClick={() => navigator.clipboard?.writeText(generated[s.key]).then(() => toast("Copied"), () => toast.error("Could not copy"))}>
                                <Copy className="size-3.5" />
                              </button>
                            </p>
                          )}
                        </>
                      )}
                    </div>
                  ))}
                  {entry.own_login && <p className="text-xs text-muted-foreground">{entry.name} has its own accounts; you set one up the first time you open it.</p>}
                </div>
              </details>
              {error && <p className="text-sm text-destructive">{error}</p>}
              {!!entry.unfinished?.length && (
                <div className="rounded-md border border-status-attention/60 bg-status-attention/5 p-3 text-sm">
                  <p className="font-medium">Not ready to install</p>
                  <p className="mb-1 text-xs text-muted-foreground">This entry was converted from a Compose file, and someone still has to decide:</p>
                  <ul className="list-disc space-y-0.5 pl-4 text-xs">
                    {entry.unfinished.map((u) => (
                      <li key={u}>{u}</li>
                    ))}
                  </ul>
                </div>
              )}
            </div>
            <SheetFooter>
              <Button
                disabled={busy || !!entry.unfinished?.length || !effName || taken.has(effName) || !effNode || !defaultSpace}
                onClick={async () => {
                  setBusy(true);
                  setError("");
                  try {
                    const spec = specFor(entry, effName, Object.fromEntries((entry.settings ?? []).map((s) => [s.key, settingValue(s.key, s.default ?? "")])));
                    spec.exposure = entry.web && !entry.infrastructure ? exposure : "private";
                    spec.autoUpdate = autoUpdate;
                    spec.forwardPorts = !!entry.port_forward && forward;
                    spec.volumeSpaces = Object.fromEntries(entry.volumes.map((v) => [v.name, eff.volumeSpaces[v.name] || fallback]));
                    spec.volumeFolders = eff.volumeFolders;
                    spec.mounts = eff.mounts.map((m) => create(ServiceMountSchema, m));
                    await onInstall(entry, spec, effNode);
                    onClose();
                    reset();
                  } catch (e) {
                    if (!declined(e)) setError(String((e as Error).message ?? e));
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
