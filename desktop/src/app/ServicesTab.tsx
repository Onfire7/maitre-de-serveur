// The Services tab (decision 190): every Service in the Fleet at full width,
// under the Node that runs it, with the flyout's actions on each row and
// Start, Stop, Restart and Update for several at once.
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { ArrowUpCircle, ChevronRight, ExternalLink, MoreHorizontal, Play, Plus, RotateCw, Search, Square } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";
import { formatUptime } from "@/lib/format";
import type { FleetService, FleetSnapshot } from "@/fleet/useFleet";
import { nodeWord } from "@/fleet/useFleet";
import { controlService, targetFor } from "@/fleet/actions";
import { declined } from "@/fleet/grants";
import type { ConsoleState } from "@/state";
import { flyoutItems, type FlyoutItem } from "./ServiceFlyout";
import { NodeMark, ServiceIcon, StatusWord } from "./status";
import { ServiceDetails } from "./ServiceDetails";
import { useRoute, type ServiceEdit } from "./routes";

const GROUP_KEY = "maitre.services.group";

const REACH: Record<string, string> = {
  private: "At home",
  remote: "Your devices, anywhere",
  public: "Public (Tailscale)",
  direct: "Public",
};

type Show = "all" | "attention" | "running" | "stopped";
type Bulk = "start" | "stop" | "restart" | "update";

export function ServicesTab({ snap, state, services, onAct, now }: { snap: FleetSnapshot | undefined; state: ConsoleState; services: FleetService[]; onAct: (s: FleetService, item: FlyoutItem) => void; now: number }) {
  const { go, route } = useRoute();
  const [query, setQuery] = useState("");
  const [show, setShow] = useState<Show>("all");
  const [byNode, setByNode] = useState(() => localStorage.getItem(GROUP_KEY) !== "list");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  // One Service open at a time, with all its settings (192).
  const [open, setOpen] = useState<string | null>(() => new URLSearchParams(window.location.search).get("expand"));
  // Access… and Storage… from a menu land here, the row open at that
  // section in edit mode (194); `?edit=access` does the same on load.
  const [edit, setEdit] = useState<ServiceEdit | undefined>(() => (["access", "storage"].includes(new URLSearchParams(window.location.search).get("edit") ?? "") ? (new URLSearchParams(window.location.search).get("edit") as ServiceEdit) : undefined));
  const asked = route.name === "fleet" ? route.open : undefined;
  useEffect(() => {
    if (!asked) return;
    // Clear the filters so the row is there to open.
    setQuery("");
    setShow("all");
    setOpen(asked.service);
    setEdit(asked.edit);
  }, [asked]);

  const spaceName = (id: string) => snap?.spaces.find((sp) => sp.spaceId === id)?.name ?? id;
  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    return services
      .filter((s) => !q || s.spec.name.toLowerCase().includes(q) || s.spec.catalogId.toLowerCase().includes(q) || (s.node?.name.toLowerCase().includes(q) ?? false))
      .filter((s) => show === "all" || (show === "attention" ? s.word === "Attention" || s.nodeUnreachable : show === "running" ? s.word === "Online" || s.word === "Starting" : s.word === "Stopped"))
      .sort((a, b) => a.spec.name.localeCompare(b.spec.name));
  }, [services, query, show]);

  // Every Node gets a group, even with nothing on it, so the layout of the
  // Fleet reads at a glance; a filter hides the empty ones.
  const groups = useMemo(() => {
    const nodes = [...(snap?.nodes ?? [])].sort((a, b) => a.name.localeCompare(b.name));
    const g = nodes.map((n) => ({ nodeId: n.nodeId, name: n.name, word: nodeWord(snap?.status.get(n.nodeId), now), services: shown.filter((s) => s.spec.nodeId === n.nodeId) }));
    const orphans = shown.filter((s) => !nodes.some((n) => n.nodeId === s.spec.nodeId));
    if (orphans.length) g.push({ nodeId: "", name: "Node unknown", word: "Unreachable" as const, services: orphans });
    return query || show !== "all" ? g.filter((x) => x.services.length) : g;
  }, [snap, shown, now, query, show]);

  const pickedServices = services.filter((s) => picked.has(s.spec.name));
  const toggle = (name: string, on: boolean) =>
    setPicked((p) => {
      const n = new Set(p);
      on ? n.add(name) : n.delete(name);
      return n;
    });
  const allPicked = shown.length > 0 && shown.every((s) => picked.has(s.spec.name));

  async function bulk(action: Bulk) {
    const list = pickedServices.filter((s) => !s.nodeUnreachable);
    setBusy(true);
    const failed: string[] = [];
    for (const s of list) {
      const t = targetFor(snap, state.nodes, s.spec.nodeId);
      try {
        if (!t) throw new Error("no address");
        await controlService(t, s.spec.name, action);
      } catch (e) {
        if (!declined(e)) failed.push(`${s.spec.name}: ${String((e as Error).message ?? e)}`);
      }
    }
    setBusy(false);
    const verb = { start: "Starting", stop: "Stopped", restart: "Restarting", update: "Updating" }[action];
    const ok = list.length - failed.length;
    if (ok) toast.success(`${verb} ${ok} ${ok === 1 ? "Service" : "Services"}`);
    if (failed.length) toast.error(failed.join("\n"));
    const skipped = pickedServices.length - list.length;
    if (skipped) toast(`${skipped} skipped: ${skipped === 1 ? "its Node is" : "their Nodes are"} unreachable`);
    setPicked(new Set());
  }

  if (snap && services.length === 0)
    return (
      <div className="flex flex-col items-start gap-3">
        <p className="text-sm text-muted-foreground">No Services yet.</p>
        <Button onClick={() => go({ name: "catalog" })}>
          <Plus /> Add a Service
        </Button>
      </div>
    );

  const row = (s: FleetService) => (
    <ServiceRow key={s.spec.name} service={s} picked={picked.has(s.spec.name)} onPick={(on) => toggle(s.spec.name, on)} onAct={(it) => onAct(s, it)} onOpenPage={() => go({ name: "service", service: s.spec.name })} snap={snap} open={open === s.spec.name} edit={open === s.spec.name ? edit : undefined} onToggle={() => (setEdit(undefined), setOpen((o) => (o === s.spec.name ? null : s.spec.name)))} showNode={!byNode} spaces={[...new Set(Object.values(s.spec.volumeSpaces))].map(spaceName)} now={now} />
  );

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative w-64">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Find a Service or Node" className="h-8 pl-8" aria-label="Find a Service or Node" />
        </div>
        <Segmented
          value={show}
          onChange={setShow}
          options={[
            ["all", "All"],
            ["attention", "Needs attention"],
            ["running", "Running"],
            ["stopped", "Stopped"],
          ]}
        />
        <Segmented
          value={byNode ? "node" : "list"}
          onChange={(v) => {
            setByNode(v === "node");
            localStorage.setItem(GROUP_KEY, v);
          }}
          options={[
            ["node", "By Node"],
            ["list", "One list"],
          ]}
        />
        <Button size="sm" variant="outline" className="ml-auto" onClick={() => go({ name: "catalog" })}>
          <Plus /> Add a Service
        </Button>
      </div>

      <div className={cn("flex h-9 items-center gap-2 rounded-lg border px-3 text-sm", picked.size === 0 && "text-muted-foreground")}>
        <Checkbox
          aria-label="Select all shown"
          checked={allPicked ? true : picked.size > 0 ? "indeterminate" : false}
          onCheckedChange={(v) => setPicked(v === true ? new Set(shown.map((s) => s.spec.name)) : new Set())}
        />
        {picked.size === 0 ? (
          <span>Select Services to start, stop, restart or update several at once.</span>
        ) : (
          <>
            <span className="mr-2 font-medium">{picked.size} selected</span>
            <Button size="sm" variant="ghost" className="h-7" disabled={busy} onClick={() => bulk("start")}>
              <Play /> Start
            </Button>
            <Button size="sm" variant="ghost" className="h-7" disabled={busy} onClick={() => bulk("stop")}>
              <Square /> Stop
            </Button>
            <Button size="sm" variant="ghost" className="h-7" disabled={busy} onClick={() => bulk("restart")}>
              <RotateCw /> Restart
            </Button>
            <Button size="sm" variant="ghost" className="h-7" disabled={busy} onClick={() => bulk("update")}>
              <ArrowUpCircle /> Update
            </Button>
            <Button size="sm" variant="ghost" className="ml-auto h-7" onClick={() => setPicked(new Set())}>
              Clear
            </Button>
          </>
        )}
      </div>

      {byNode ? (
        groups.map((g) => (
          <section key={g.nodeId || "unknown"} aria-label={g.name}>
            <header className="mb-1.5 flex items-center gap-2">
              <NodeMark word={g.word} size={22} />
              <button className="text-sm font-semibold hover:underline" disabled={!g.nodeId} onClick={() => go({ name: "fleet", tab: "nodes", nodeId: g.nodeId })}>
                {g.name}
              </button>
              {g.word !== "Healthy" && <StatusWord word={g.word} className="text-xs" />}
              <span className="text-xs text-muted-foreground">
                {g.services.length} {g.services.length === 1 ? "Service" : "Services"}
              </span>
            </header>
            {g.services.length ? <ul className="divide-y rounded-lg border">{g.services.map(row)}</ul> : <p className="rounded-lg border border-dashed px-3 py-2 text-xs text-muted-foreground">Nothing runs here.</p>}
          </section>
        ))
      ) : shown.length ? (
        <ul className="divide-y rounded-lg border">{shown.map(row)}</ul>
      ) : (
        <p className="text-sm text-muted-foreground">No Service matches.</p>
      )}
    </div>
  );
}

function ServiceRow({ service: s, snap, open, edit, onToggle, picked, onPick, onAct, onOpenPage, showNode, spaces, now }: { service: FleetService; snap: FleetSnapshot | undefined; open: boolean; edit?: ServiceEdit; onToggle: () => void; picked: boolean; onPick: (on: boolean) => void; onAct: (it: FlyoutItem) => void; onOpenPage: () => void; showNode: boolean; spaces: string[]; now: number }) {
  const st = s.status;
  const running = st?.state === 3 || st?.state === 2;
  const items = flyoutItems(s);
  const up = running && st?.startedUnixMs ? Math.max(0, Math.floor((now - Number(st.startedUnixMs)) / 1000)) : 0;
  return (
    <li>
      <div className={cn("flex items-center gap-3 px-3 py-2", picked && "bg-accent/40", s.nodeUnreachable && "opacity-60")}>
        <button onClick={onToggle} aria-expanded={open} aria-label={`${open ? "Hide" : "Show"} all of ${s.spec.name}'s settings`} title={open ? "Hide settings" : "Show all settings"} className="-mr-1 rounded p-0.5 text-muted-foreground hover:bg-accent hover:text-foreground">
          <ChevronRight className={cn("size-4 transition-transform", open && "rotate-90")} />
        </button>
        <Checkbox aria-label={`Select ${s.spec.name}`} checked={picked} onCheckedChange={(v) => onPick(v === true)} />
        <ServiceIcon catalogId={s.spec.catalogId} word={s.word} />
        <div className="w-44 min-w-0 shrink-0">
          <button onClick={onOpenPage} className="block max-w-full truncate text-left text-sm font-medium hover:underline">
            {s.spec.name}
          </button>
          <div className="truncate text-xs">
            {s.nodeUnreachable ? <span className="text-status-attention">Node unreachable</span> : <StatusWord word={s.word} />}
            {showNode && <span className="text-muted-foreground"> · {s.node?.name ?? "Node unknown"}</span>}
          </div>
        </div>
        <div className="min-w-0 flex-1 cursor-pointer text-xs text-muted-foreground" onClick={onToggle}>
          {!s.nodeUnreachable && st?.message && s.word !== "Online" ? (
            <div className="truncate text-foreground/80" title={st.message}>
              {st.message}
            </div>
          ) : st?.url ? (
            <div className="truncate" title={st.url}>
              {st.url}
              {st.remoteUrl && ` · ${st.remoteUrl}`}
            </div>
          ) : (
            <div>No web page</div>
          )}
          <div className="truncate">
            {REACH[s.spec.exposure || "private"] ?? s.spec.exposure}
            {spaces.length > 0 && ` · data in ${spaces.join(", ")}`}
          </div>
        </div>
        <div className="hidden w-28 shrink-0 text-right text-xs text-muted-foreground tabular-nums lg:block">
          {up > 0 && <div>up {formatUptime(up)}</div>}
          {!!st?.restarts && <div className={cn(st.restarts > 3 && "text-status-attention")}>{st.restarts} restarts</div>}
        </div>
        <div className="flex shrink-0 items-center">
          <Button size="icon" variant="ghost" className="size-8" title="Open in your browser" aria-label={`Open ${s.spec.name}`} disabled={!st?.url || s.word !== "Online"} onClick={() => onAct("open")}>
            <ExternalLink />
          </Button>
          {running ? (
            <Button size="icon" variant="ghost" className="size-8" title="Stop" aria-label={`Stop ${s.spec.name}`} disabled={s.nodeUnreachable} onClick={() => onAct("stop")}>
              <Square />
            </Button>
          ) : (
            <Button size="icon" variant="ghost" className="size-8" title="Start" aria-label={`Start ${s.spec.name}`} disabled={s.nodeUnreachable} onClick={() => onAct("start")}>
              <Play />
            </Button>
          )}
          <Button size="icon" variant="ghost" className="size-8" title="Restart" aria-label={`Restart ${s.spec.name}`} disabled={s.nodeUnreachable} onClick={() => onAct("restart")}>
            <RotateCw />
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button size="icon" variant="ghost" className="size-8" title="More" aria-label={`More for ${s.spec.name}`}>
                <MoreHorizontal />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-56">
              {items.map((it) => (
                <div key={it.id}>
                  {it.danger && <DropdownMenuSeparator />}
                  <DropdownMenuItem disabled={it.disabled} onSelect={() => onAct(it.id)} className={cn(it.danger && "text-destructive focus:text-destructive [&_svg]:text-destructive")}>
                    {it.icon}
                    {it.label}
                  </DropdownMenuItem>
                </div>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>
      {open && <ServiceDetails service={s} snap={snap} onAct={onAct} now={now} edit={edit} />}
    </li>
  );
}

function Segmented<T extends string>({ value, onChange, options }: { value: T; onChange: (v: T) => void; options: [T, string][] }) {
  return (
    <div role="radiogroup" className="inline-flex h-8 items-center rounded-md border p-0.5 text-xs">
      {options.map(([v, label]) => (
        <button key={v} role="radio" aria-checked={value === v} onClick={() => onChange(v)} className={cn("h-full rounded px-2.5", value === v ? "bg-accent font-medium text-foreground" : "text-muted-foreground hover:text-foreground")}>
          {label}
        </button>
      ))}
    </div>
  );
}
