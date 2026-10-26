// The Catalog as a card grid (decision 139) with the install sheet (151).
// Step 3 fills the sheet's Options disclosure; here it asks name and Node.
import { useMemo, useState } from "react";
import { Search } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import bundledCatalog from "@/catalog/index.json";
import { specFor, type CatalogEntry, type CatalogIndex } from "@/services";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { ServiceIcon } from "./status";

const catalog = bundledCatalog as CatalogIndex;

export function CatalogPage({ snap, onInstall }: { snap: FleetSnapshot | undefined; onInstall: (entry: CatalogEntry, name: string, nodeId: string) => Promise<void> }) {
  const [q, setQ] = useState("");
  const [cat, setCat] = useState<string>("all");
  const [picked, setPicked] = useState<CatalogEntry | null>(null);
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

function InstallSheet({ entry, snap, onClose, onInstall }: { entry: CatalogEntry | null; snap: FleetSnapshot | undefined; onClose: () => void; onInstall: (entry: CatalogEntry, name: string, nodeId: string) => Promise<void> }) {
  const nodes = snap?.nodes ?? [];
  const [name, setName] = useState("");
  const [nodeId, setNodeId] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const taken = new Set(snap?.specs.map((s) => s.name));
  const effName = (name || entry?.id || "").trim();
  const effNode = nodeId || nodes[0]?.nodeId || "";
  const defaultSpace = snap?.spaces.find((s) => s.defaultForNew);
  return (
    <Sheet
      open={!!entry}
      onOpenChange={(o) => {
        if (!o) {
          onClose();
          setName("");
          setNodeId("");
          setError("");
        }
      }}
    >
      <SheetContent className="w-[420px] sm:max-w-[420px]">
        {entry && (
          <>
            <SheetHeader>
              <div className="flex items-center gap-3">
                <ServiceIcon catalogId={entry.id} word="Online" size={44} />
                <div>
                  <SheetTitle>Install {entry.name}</SheetTitle>
                  <SheetDescription>{entry.summary}</SheetDescription>
                </div>
              </div>
            </SheetHeader>
            <div className="space-y-5 px-4">
              <div className="space-y-1.5">
                <Label htmlFor="svc-name">Name</Label>
                <Input id="svc-name" value={name} onChange={(e) => setName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ""))} placeholder={entry.id} />
                <p className="text-xs text-muted-foreground">Reachable on your network as {effName || entry.id}.local</p>
                {taken.has(effName) && <p className="text-xs text-destructive">A Service named {effName} already exists.</p>}
              </div>
              {nodes.length > 1 && (
                <div className="space-y-1.5">
                  <Label>Node</Label>
                  <div className="grid gap-1.5">
                    {nodes.map((n) => (
                      <label key={n.nodeId} className={cn("flex cursor-pointer items-center gap-2 rounded-md border px-3 py-2 text-sm", effNode === n.nodeId && "border-primary")}>
                        <input type="radio" name="node" checked={effNode === n.nodeId} onChange={() => setNodeId(n.nodeId)} />
                        {n.name}
                      </label>
                    ))}
                  </div>
                </div>
              )}
              {entry.volumes.length > 0 && (
                <div className="space-y-1.5">
                  <Label>Storage</Label>
                  <p className="text-sm text-muted-foreground">
                    {entry.volumes.length === 1 ? "Its data goes" : `Its ${entry.volumes.length} volumes go`} in <b className="text-foreground">{defaultSpace?.name ?? "no Space yet"}</b>. The Storage item in its menu changes this later.
                  </p>
                </div>
              )}
              <details className="text-sm">
                <summary className="cursor-pointer text-muted-foreground">Options</summary>
                <p className="mt-2 text-xs text-muted-foreground">Exposure (private), automatic updates (on){entry.port_forward ? ", port forwarding (off)" : ""}{entry.settings?.length ? ` and ${entry.settings.length} settings` : ""} keep their defaults. They are editable from the Service's menu once installed.</p>
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
                    await onInstall(entry, effName, effNode);
                    onClose();
                    setName("");
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
