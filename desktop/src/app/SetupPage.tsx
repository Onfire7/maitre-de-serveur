// A Service's Setup (ADR 0010): the Console page where its network, its
// Pages, its address and its container options are changed, with the unit
// files the Node would write shown read-only beside the form, each line
// marked as the bundle's, the agent's or the person's own.
//
// Nothing is applied until Apply: the page asks the Node what it would
// write (PreviewSetup) on every change and shows it as a diff against
// what is written now, with the Services that would restart. The user
// asked for this control without hand-editing the files.
import { useDeferredValue, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { create, clone } from "@bufbuild/protobuf";
import { toast } from "sonner";
import { ArrowLeft, Check, Copy, Plus, RotateCw, Trash2, TriangleAlert, Undo2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { cn } from "@/lib/utils";
import { SetupSchema, ServicePageSchema, UnitOverrideSchema, type RenderedUnit, type Setup } from "@/gen/maitre/v1/service_pb";
import type { FleetService } from "@/fleet/useFleet";
import { declined } from "@/fleet/grants";
import { lineDiff, diffCount, type DiffLine } from "@/fleet/diff";
import { NETWORK_HOST, NETWORK_OWN, NETWORK_SERVICE, NETWORK_TUNNEL, applySetup, dependantsOf, networkChoices, previewSetup, setupOf, stackOf, unitLines } from "@/fleet/setup";
import type { Target } from "@/transport";
import { catalogById, ServiceIcon, StatusWord } from "./status";
import { useRoute } from "./routes";

/** What the Setup fields need of a Service: at install there is no
 * Service yet, only the Catalog entry's shape. */
export interface SetupSubject {
  name: string;
  components: string[];
  tunnel: boolean;
  nodeName?: string;
  /** Empty at install: nothing leans on it yet. */
  dependants: string[];
}

export function subjectOf(service: FleetService, services: FleetService[]): SetupSubject {
  return { name: service.spec.name, components: service.spec.components.map((c) => c.name), tunnel: service.spec.tunnel, nodeName: service.node?.name, dependants: dependantsOf(service.spec.name, services) };
}

/** The four networks a Service can be on, in the page's own words. */
const NETWORKS: { kind: string; label: string; help: string }[] = [
  { kind: NETWORK_OWN, label: "Its own network", help: "It reaches other Services of the Fleet by name, and they reach it by its own." },
  { kind: NETWORK_TUNNEL, label: "Through a Tunnel", help: "All of its traffic goes through the Tunnel, which it shares a network with. It lives on the Tunnel's Node and starts, stops and moves with it." },
  { kind: NETWORK_SERVICE, label: "On another Service's network", help: "It joins that Service's own network as well as the shared one, so it reaches that Service's containers by their names." },
  { kind: NETWORK_HOST, label: "The Node's network", help: "It shares the Node's own addresses and ports. Its page is then open to the home network at the Node's address." },
];

export function SetupPage({ service, services, target, onDone }: { service: FleetService; services: FleetService[]; target: Target | undefined; onDone: () => void }) {
  const { go } = useRoute();
  const name = service.spec.name;
  const entry = catalogById.get(service.spec.catalogId);
  const [setup, setSetup] = useState<Setup>(() => setupOf(service.spec));
  const [applying, setApplying] = useState(false);
  // The spec changes under the page when the Fleet is read again; the
  // form is the person's, so only a Service of another name resets it.
  useEffect(() => setSetup(setupOf(service.spec)), [name]);
  const edit = (f: (s: Setup) => void) =>
    setSetup((cur) => {
      const next = clone(SetupSchema, cur);
      f(next);
      return next;
    });

  const choices = useMemo(() => networkChoices(service, services), [service, services]);
  // What is written now, to diff against.
  const current = useQuery({
    queryKey: ["setup-current", name, target?.addr],
    enabled: !!target,
    staleTime: 30_000,
    queryFn: () => previewSetup(target!, name),
  });
  // What this Setup would write. Deferred, so typing does not call the
  // Node on every keystroke.
  const asked = useDeferredValue(setup);
  const proposed = useQuery({
    queryKey: ["setup-preview", name, target?.addr, JSON.stringify(asked)],
    enabled: !!target,
    retry: false,
    queryFn: () => previewSetup(target!, name, asked),
  });
  const units = proposed.data?.units ?? current.data?.units ?? [];
  const problem = proposed.error ? String((proposed.error as Error).message ?? proposed.error) : undefined;
  const restarts = proposed.data?.restarts ?? [];
  const changed = JSON.stringify(setupOf(service.spec)) !== JSON.stringify(setup);
  const target_ = choices.find((c) => c.name === setup.networkTarget);
  const movesTo = setup.networkKind !== NETWORK_OWN && setup.networkKind !== NETWORK_HOST ? target_?.otherNode : undefined;

  const apply = async () => {
    if (!target) return toast.error(`No address for ${service.node?.name ?? "its Node"}`);
    setApplying(true);
    try {
      await applySetup(target, name, setup);
      toast.success(`${name}: applied${restarts.length > 1 ? `, restarting ${restarts.join(", ")}` : ", restarting"}`);
      onDone();
      go({ name: "page", service: name, page: "" });
    } catch (e) {
      if (!declined(e)) toast.error(String((e as Error).message ?? e));
    } finally {
      setApplying(false);
    }
  };

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="flex shrink-0 items-center gap-3 border-b px-5 py-3">
        <Button variant="ghost" size="icon" aria-label="Back to its page" onClick={() => go({ name: "page", service: name, page: "" })}>
          <ArrowLeft />
        </Button>
        <ServiceIcon catalogId={service.spec.catalogId} word={service.word} size={32} />
        <div className="min-w-0">
          <h1 className="truncate text-base font-semibold">{name} · Setup</h1>
          <p className="truncate text-xs text-muted-foreground">
            <StatusWord word={service.word} />
            {service.node && <> · on {service.node.name}</>}
            {entry && <> · {entry.name}</>}
          </p>
        </div>
        <div className="ml-auto flex items-center gap-2">
          {changed && (
            <Button variant="ghost" size="sm" onClick={() => setSetup(setupOf(service.spec))}>
              <Undo2 /> Discard
            </Button>
          )}
          <Button size="sm" disabled={!changed || applying || !!problem} onClick={apply}>
            {applying ? <RotateCw className="animate-spin" /> : <Check />} Apply and restart
          </Button>
        </div>
      </header>
      <div className="grid min-h-0 flex-1 gap-5 overflow-y-auto p-5 lg:grid-cols-2">
        <div className="space-y-5">
          {problem && (
            <div className="flex items-start gap-2 rounded-lg border border-status-attention/60 bg-status-attention/10 px-3 py-2 text-sm">
              <TriangleAlert className="mt-0.5 size-4 shrink-0 text-status-attention" />
              <span>{problem}</span>
            </div>
          )}
          <SetupFields setup={setup} edit={edit} subject={subjectOf(service, services)} choices={choices} movesTo={movesTo} stack={stackOf(name, services)} />
        </div>
        <div className="min-w-0 space-y-3">
          <UnitsPanel units={units} before={current.data?.units ?? []} loading={proposed.isFetching || current.isLoading} />
          {restarts.length > 0 && changed && (
            <p className="text-xs text-muted-foreground">
              Applying restarts {restarts.length === 1 ? restarts[0] : <>{restarts.slice(0, -1).join(", ")} and {restarts[restarts.length - 1]}</>}.
            </p>
          )}
          {(proposed.data?.protected ?? []).length > 0 && (
            <p className="text-xs text-muted-foreground">
              A Trusted Device is asked to allow {(proposed.data?.protected ?? []).map((p) => p.option).join(", ")} when this is applied.
            </p>
          )}
        </div>
      </div>
    </div>
  );
}

/** The whole form: the Setup page shows it beside the rendered units, and
 * the install sheet shows it under Advanced. */
export function SetupFields({ setup, edit, subject, choices, movesTo, stack }: { setup: Setup; edit: (f: (s: Setup) => void) => void; subject: SetupSubject; choices: ReturnType<typeof networkChoices>; movesTo?: string; stack: string[] }) {
  return (
    <>
      <NetworkCard setup={setup} edit={edit} choices={choices} subject={subject} movesTo={movesTo} stack={stack} />
      <ReachCard setup={setup} edit={edit} />
      <PagesCard setup={setup} edit={edit} subject={subject} />
      <OptionsCard setup={setup} edit={edit} subject={subject} />
    </>
  );
}

function NetworkCard({ setup, edit, choices, subject, movesTo, stack }: { setup: Setup; edit: (f: (s: Setup) => void) => void; choices: ReturnType<typeof networkChoices>; subject: SetupSubject; movesTo?: string; stack: string[] }) {
  const kinds = NETWORKS.filter((n) => {
    if (n.kind === NETWORK_TUNNEL) return subject.tunnel ? false : choices.some((c) => c.kind === NETWORK_TUNNEL);
    if (n.kind === NETWORK_SERVICE) return choices.some((c) => c.kind === NETWORK_SERVICE);
    if (n.kind === NETWORK_HOST) return subject.components.length === 0;
    return true;
  });
  const forKind = choices.filter((c) => c.kind === setup.networkKind);
  return (
    <Card>
      <CardHeader>
        <CardTitle>Network</CardTitle>
        <CardDescription>Which network this Service is on. Services of the Fleet reach each other by name, {subject.name}.local, whichever Node they run on.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {subject.tunnel && <p className="rounded-md bg-muted px-3 py-2 text-xs">This is a Tunnel: other Services join it in their own Setup, and it stays on its own network.</p>}
        <div className="space-y-2">
          {kinds.map((n) => (
            <label key={n.kind} className={cn("flex cursor-pointer gap-2.5 rounded-lg border p-3 text-sm", setup.networkKind === n.kind && "border-primary bg-accent/40")}>
              <input
                type="radio"
                name="network"
                className="mt-0.5"
                checked={setup.networkKind === n.kind}
                onChange={() =>
                  edit((s) => {
                    s.networkKind = n.kind;
                    s.networkTarget = n.kind === NETWORK_TUNNEL || n.kind === NETWORK_SERVICE ? (choices.find((c) => c.kind === n.kind)?.name ?? "") : "";
                  })
                }
              />
              <span className="min-w-0">
                <span className="font-medium">{n.label}</span>
                <span className="block text-xs text-muted-foreground">{n.help}</span>
              </span>
            </label>
          ))}
        </div>
        {(setup.networkKind === NETWORK_TUNNEL || setup.networkKind === NETWORK_SERVICE) && (
          <div className="space-y-1.5">
            <Label htmlFor="network-target">{setup.networkKind === NETWORK_TUNNEL ? "Tunnel" : "Service"}</Label>
            <Select value={setup.networkTarget} onValueChange={(v) => edit((s) => (s.networkTarget = v))}>
              <SelectTrigger id="network-target">
                <SelectValue placeholder="Choose one" />
              </SelectTrigger>
              <SelectContent>
                {forKind.map((c) => (
                  <SelectItem key={c.name} value={c.name}>
                    {c.name}
                    {c.otherNode ? ` · on ${c.otherNode}` : ""}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}
        {movesTo && (
          <div className="space-y-1.5 rounded-md border border-status-attention/60 bg-status-attention/10 px-3 py-2 text-xs">
            <p>
              <b>{setup.networkTarget}</b> runs on {movesTo}, and a Service joins only a Service of its own Node. You can:
            </p>
            <ul className="list-disc space-y-0.5 pl-4">
              <li>move {stack.length > 1 ? `${subject.name} and the rest of its set (${stack.join(", ")})` : subject.name} to {movesTo}, from Move to another Node;</li>
              <li>install a {setup.networkTarget} of its own on {subject.nodeName ?? "this Node"} and join that one;</li>
              <li>leave it as it is.</li>
            </ul>
          </div>
        )}
        {subject.dependants.length > 0 && (
          <p className="text-xs text-muted-foreground">
            {subject.dependants.join(", ")} {subject.dependants.length === 1 ? "is on" : "are on"} this Service's network. It cannot be removed until {subject.dependants.length === 1 ? "it leaves" : "they leave"}.
          </p>
        )}
        {subject.components.length > 0 && (
          <label className="flex items-center gap-2 text-sm">
            <Switch checked={setup.networkInternal} onCheckedChange={(v) => edit((s) => (s.networkInternal = v))} />
            Keep its own network off the internet
          </label>
        )}
      </CardContent>
    </Card>
  );
}

function ReachCard({ setup, edit }: { setup: Setup; edit: (f: (s: Setup) => void) => void }) {
  const [port, setPort] = useState("");
  const add = () => {
    const p = port.trim();
    const full = /^\d{1,5}$/.test(p) ? `${p}/tcp` : p;
    if (!/^\d{1,5}\/(tcp|udp)$/.test(full)) return toast.error("Write a port as 5432, or 5432/udp");
    if (!setup.ports.includes(full)) edit((s) => s.ports.push(full));
    setPort("");
  };
  return (
    <Card>
      <CardHeader>
        <CardTitle>Reach from other Nodes</CardTitle>
        <CardDescription>Whether Services on the Fleet's other Nodes may use this one. Its pages' ports always come along; add any other port apps need, such as a database's.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <label className="flex items-center gap-2 text-sm">
          <Switch checked={setup.fleetReach} onCheckedChange={(v) => edit((s) => (s.fleetReach = v))} />
          Reachable by other Services of the Fleet
        </label>
        <p className="text-xs text-muted-foreground">The Nodes carry the traffic between themselves; nothing is opened to the home network.</p>
        <div className="flex flex-wrap gap-1.5">
          {setup.ports.map((p) => (
            <Badge key={p} variant="outline" className="gap-1">
              {p}
              <button aria-label={`Remove port ${p}`} onClick={() => edit((s) => (s.ports = s.ports.filter((x) => x !== p)))}>
                <Trash2 className="size-3" />
              </button>
            </Badge>
          ))}
        </div>
        <div className="flex gap-2">
          <Input aria-label="Port other Services may use" placeholder="5432" value={port} onChange={(e) => setPort(e.target.value)} onKeyDown={(e) => e.key === "Enter" && (e.preventDefault(), add())} className="w-32" />
          <Button variant="outline" size="sm" onClick={add}>
            <Plus /> Add a port
          </Button>
        </div>
        <AddressRow setup={setup} edit={edit} />
      </CardContent>
    </Card>
  );
}

function AddressRow({ setup, edit }: { setup: Setup; edit: (f: (s: Setup) => void) => void }) {
  return (
    <div className="space-y-1.5 border-t pt-3">
      <Label htmlFor="address">Its address on the Fleet's network</Label>
      <div className="flex gap-2">
        <Input id="address" value={setup.address} onChange={(e) => edit((s) => (s.address = e.target.value.trim()))} className="font-mono" />
        <Button
          variant="outline"
          size="icon"
          aria-label="Copy the address"
          onClick={() => navigator.clipboard?.writeText(setup.address).then(() => toast(`Copied ${setup.address}`), () => toast.error("Could not copy the address"))}
        >
          <Copy />
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">The same on whichever Node it runs; apps that take an address rather than a name can have this one.</p>
    </div>
  );
}

function PagesCard({ setup, edit, subject }: { setup: Setup; edit: (f: (s: Setup) => void) => void; subject: SetupSubject }) {
  const components = subject.components;
  return (
    <Card>
      <CardHeader>
        <CardTitle>Pages</CardTitle>
        <CardDescription>The web pages this Service offers. The first is its own, at {subject.name}.local; the others at {subject.name}-&lt;page&gt;.local. A Service with no page works in the background and is not in the sidebar.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {setup.pages.length === 0 && <p className="text-sm text-muted-foreground">No pages.</p>}
        {setup.pages.map((p, i) => (
          <div key={i} className="space-y-2 rounded-lg border p-3">
            <div className="flex items-center gap-2">
              <Input aria-label={`Page ${i + 1} name`} value={p.name} onChange={(e) => edit((s) => (s.pages[i].name = e.target.value.trim()))} placeholder="web" className="w-32" />
              <Input aria-label={`Page ${i + 1} label`} value={p.label} onChange={(e) => edit((s) => (s.pages[i].label = e.target.value))} placeholder="What the sidebar calls it" />
              <Button variant="ghost" size="icon" aria-label={`Remove page ${p.name || i + 1}`} onClick={() => edit((s) => s.pages.splice(i, 1))}>
                <Trash2 />
              </Button>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Input aria-label={`Page ${i + 1} port`} type="number" value={p.port || ""} onChange={(e) => edit((s) => (s.pages[i].port = Number(e.target.value) || 0))} placeholder="Port" className="w-24" />
              <Input aria-label={`Page ${i + 1} path`} value={p.path} onChange={(e) => edit((s) => (s.pages[i].path = e.target.value))} placeholder="/" className="w-28" />
              <Input aria-label={`Page ${i + 1} health path`} value={p.healthHttp} onChange={(e) => edit((s) => (s.pages[i].healthHttp = e.target.value))} placeholder="Health path" className="w-32" />
              {components.length > 0 && (
                <Select value={p.component || components[0]} onValueChange={(v) => edit((s) => (s.pages[i].component = v))}>
                  <SelectTrigger aria-label={`Page ${i + 1} container`} className="w-36">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {components.map((c) => (
                      <SelectItem key={c} value={c}>
                        {c}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
              <Select value={p.exposure || "follow"} onValueChange={(v) => edit((s) => (s.pages[i].exposure = v === "follow" ? "" : v))}>
                <SelectTrigger aria-label={`Page ${i + 1} access`} className="w-40">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="follow">Access: as the Service</SelectItem>
                  <SelectItem value="private">At home only</SelectItem>
                  <SelectItem value="remote">At home and away</SelectItem>
                  <SelectItem value="public">On the internet</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
        ))}
        <Button
          variant="outline"
          size="sm"
          onClick={() =>
            edit((s) =>
              s.pages.push(
                create(ServicePageSchema, { name: s.pages.length === 0 ? "web" : `page${s.pages.length + 1}`, component: subject.components[0] ?? "", port: 0, path: "/" }),
              ),
            )
          }
        >
          <Plus /> Add a page
        </Button>
      </CardContent>
    </Card>
  );
}

function OptionsCard({ setup, edit, subject }: { setup: Setup; edit: (f: (s: Setup) => void) => void; subject: SetupSubject }) {
  const components = subject.components;
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [component, setComponent] = useState(components[0] ?? "");
  const [remove, setRemove] = useState(false);
  const add = () => {
    const k = key.trim();
    if (!/^[A-Za-z][A-Za-z0-9]{0,40}$/.test(k)) return toast.error("A container option is a Quadlet key such as AddCapability");
    edit((s) => s.overrides.push(create(UnitOverrideSchema, { component, key: k, value: value.trim(), remove })));
    setKey("");
    setValue("");
  };
  return (
    <Card>
      <CardHeader>
        <CardTitle>Container options</CardTitle>
        <CardDescription>Any option of the container definition, set or removed. These are kept with the Service, not in the Catalog bundle, so an update to the app keeps them. An option that gives the container power over its Node still needs a Trusted Device to allow it.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {setup.overrides.length === 0 && <p className="text-sm text-muted-foreground">None. The container runs as its Catalog entry says.</p>}
        <ul className="space-y-1.5">
          {setup.overrides.map((o, i) => (
            <li key={i} className="flex items-center gap-2 rounded-md border px-2 py-1.5 font-mono text-xs">
              {o.component && <Badge variant="outline">{o.component}</Badge>}
              <span className={cn("min-w-0 flex-1 truncate", o.remove && "line-through")}>
                {o.key}
                {o.value ? `=${o.value}` : ""}
              </span>
              <Button variant="ghost" size="icon" aria-label={`Remove the change to ${o.key}`} onClick={() => edit((s) => s.overrides.splice(i, 1))}>
                <Trash2 />
              </Button>
            </li>
          ))}
        </ul>
        <div className="flex flex-wrap items-center gap-2">
          {components.length > 0 && (
            <Select value={component} onValueChange={setComponent}>
              <SelectTrigger aria-label="Container" className="w-32">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {components.map((c) => (
                  <SelectItem key={c} value={c}>
                    {c}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
          <Input aria-label="Option" placeholder="AddCapability" value={key} onChange={(e) => setKey(e.target.value)} className="w-40 font-mono" />
          <Input aria-label="Value" placeholder="NET_ADMIN" value={value} onChange={(e) => setValue(e.target.value)} onKeyDown={(e) => e.key === "Enter" && (e.preventDefault(), add())} className="w-40 font-mono" />
          <label className="flex items-center gap-1.5 text-xs">
            <Switch checked={remove} onCheckedChange={setRemove} aria-label="Remove this option instead of setting it" /> Remove it
          </label>
          <Button variant="outline" size="sm" onClick={add}>
            <Plus /> Add
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

/** The unit files the Node would write, one tab each, read-only: every
 * line marked where it came from, and what this Setup changes. */
function UnitsPanel({ units, before, loading }: { units: RenderedUnit[]; before: RenderedUnit[]; loading: boolean }) {
  const [tab, setTab] = useState<string>(units[0]?.name ?? "");
  const names = units.map((u) => u.name);
  const shown = names.includes(tab) ? tab : (names[0] ?? "");
  const was = (name: string) => before.find((u) => u.name === name)?.body ?? "";
  return (
    <Card className="min-w-0">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          What the Node writes
          {loading && <RotateCw className="size-3.5 animate-spin text-muted-foreground" />}
        </CardTitle>
        <CardDescription>Read-only. Change it through the settings beside, not by hand.</CardDescription>
      </CardHeader>
      <CardContent className="min-w-0">
        {units.length === 0 ? (
          <p className="text-sm text-muted-foreground">Nothing to show yet.</p>
        ) : (
          <Tabs value={shown} onValueChange={setTab}>
            <TabsList className="flex-wrap">
              {units.map((u) => {
                const { added, removed } = diffCount(lineDiff(was(u.name), u.body));
                return (
                  <TabsTrigger key={u.name} value={u.name} className="font-mono text-xs">
                    {u.name}
                    {added + removed > 0 && <span className="ml-1 text-primary">●</span>}
                  </TabsTrigger>
                );
              })}
            </TabsList>
            {units.map((u) => (
              <TabsContent key={u.name} value={u.name} className="min-w-0">
                <UnitBody unit={u} before={was(u.name)} />
              </TabsContent>
            ))}
          </Tabs>
        )}
        <Legend />
      </CardContent>
    </Card>
  );
}

function UnitBody({ unit, before }: { unit: RenderedUnit; before: string }) {
  const sources = unitLines(unit).map((l) => l.source);
  const lines: DiffLine[] = lineDiff(before, unit.body);
  return (
    <pre className="max-h-[60vh] overflow-auto rounded-md bg-muted/50 p-3 font-mono text-xs leading-5">
      {lines.map((l, i) => {
        const source = l.index >= 0 ? sources[l.index] : "";
        return (
          <div
            key={i}
            data-source={source}
            data-mark={l.mark}
            className={cn(
              "whitespace-pre-wrap px-1",
              l.mark === "added" && "bg-status-online/15",
              l.mark === "removed" && "bg-destructive/15 line-through opacity-70",
              l.mark === "same" && source === "override" && "bg-primary/10",
              l.mark === "same" && source === "agent" && "text-muted-foreground",
            )}
          >
            {l.text || " "}
          </div>
        );
      })}
    </pre>
  );
}

function Legend() {
  return (
    <div className="flex flex-wrap gap-3 pt-2 text-[11px] text-muted-foreground">
      <span>
        <span className="mr-1 inline-block size-2 rounded-sm bg-primary/30" />
        yours
      </span>
      <span>
        <span className="mr-1 inline-block size-2 rounded-sm bg-muted-foreground/40" />
        added by Maître
      </span>
      <span>
        <span className="mr-1 inline-block size-2 rounded-sm bg-status-online/40" />
        this change adds
      </span>
      <span>
        <span className="mr-1 inline-block size-2 rounded-sm bg-destructive/40" />
        this change removes
      </span>
    </div>
  );
}
