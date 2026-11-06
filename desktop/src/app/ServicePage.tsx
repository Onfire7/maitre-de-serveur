// A Page of a Service is its own web page, full-bleed, with no bar above
// it (decision 138). When the Service is not Online, or offers no Page, a
// status card takes its place (decision 144). The page itself is a native
// view of the view layer (ServiceViews, decision 294), which keeps it
// when another Page is shown; this component says which frame is the one
// shown, and takes the page's place with a card when the view could not
// be opened or its web process died.
import { useEffect, useRef, type ReactNode } from "react";
import { ExternalLink, Play, ScrollText, RotateCw } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { invoke } from "@tauri-apps/api/core";
import type { Target } from "@/transport";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import type { FleetService } from "@/fleet/useFleet";
import { isDemo } from "@/demo/flag";
import { ServiceIcon, StatusWord } from "./status";
import { catalogById } from "./status";
import type { PageView } from "@/fleet/pages";
import { dropFrame, frameCommand, hideFrame, showFrame, useFrame } from "./frames";
import { PageSurface } from "./PageSurface";
import { sampleElement, setSampler } from "./layout";

export function ServicePage({ service, page, target, onAction }: { service: FleetService; page: PageView | undefined; target: Target | undefined; onAction: (a: "start" | "restart" | "logs" | "open") => void }) {
  const s = service.status;
  const web = !!page?.url;
  const online = service.word === "Online" && web;
  // The Page's host name keys its proxy, its view and its frame (ADR 0010).
  const host = page?.host ?? service.spec.name;
  // Decision 71: the page is read through a loopback proxy that tunnels to
  // the Node over its mutual-TLS listener, so it needs neither mDNS nor the
  // Service being reachable on the LAN, and the window may embed it.
  const local = useQuery({
    queryKey: ["service-url", host, target?.addr],
    enabled: online && !!target && !isDemo(),
    staleTime: Infinity,
    retry: 1,
    queryFn: () => invoke<string>("service_url", { addr: target!.addr, fingerprint: target!.fingerprint, view: host, service: service.spec.name, page: page?.first ? "" : (page?.page.name ?? "") }),
  });
  const src = online && !isDemo() ? local.data : undefined;
  const name = service.spec.name;
  const frame = useFrame(host);
  useEffect(() => {
    if (!src) return;
    showFrame(host, src, name, page?.page.name ?? "");
    return () => hideFrame(host);
  }, [host, name, src]);
  // A page that is not Online is gone: its frame goes, the card takes its place.
  useEffect(() => {
    if (!online) dropFrame(host);
  }, [host, online]);
  const openInBrowser = (
    <Button size="sm" variant="outline" onClick={() => onAction("open")}>
      <ExternalLink /> Open in your browser
    </Button>
  );
  if (online) {
    if (isDemo()) return <DemoFrame service={service} page={page} />;
    // Outside the app (no Tauri) there is no proxy and no view.
    const failed = local.error ? String((local.error as Error).message ?? local.error) : frame?.failed;
    if (failed)
      return (
        <PageCard service={service} actions={openInBrowser}>
          <p>The page could not be opened here.</p>
          <p className="rounded-md bg-muted px-3 py-2 font-mono text-xs">{failed}</p>
        </PageCard>
      );
    if (frame?.crashed)
      return (
        <PageCard
          service={service}
          actions={
            <>
              <Button size="sm" onClick={() => frameCommand(host, "reload")}>
                <RotateCw /> Reload
              </Button>
              {openInBrowser}
            </>
          }
        >
          <p>The page stopped: its web process ended. The Console is unaffected; reload the page to start it again.</p>
        </PageCard>
      );
    return null;
  }
  const entry = catalogById.get(service.spec.catalogId);
  return (
    <PageCard service={service} actions={<>
            {service.word === "Stopped" && !service.nodeUnreachable && (
              <Button size="sm" onClick={() => onAction("start")}>
                <Play /> Start
              </Button>
            )}
            {service.word === "Attention" && (
              <Button size="sm" onClick={() => onAction("restart")}>
                <RotateCw /> Restart
              </Button>
            )}
            {!service.nodeUnreachable && (
              <Button size="sm" variant="outline" onClick={() => onAction("logs")}>
                <ScrollText /> Logs
              </Button>
            )}
            {web && service.word === "Online" && openInBrowser}
          </>}>
          {service.nodeUnreachable && service.node ? (
            <p>
              <b>{service.node.name}</b> is unreachable, so this Service cannot be reached either. Check that the machine is on and connected.
            </p>
          ) : (
            <>
              {s?.message && <p className="rounded-md bg-muted px-3 py-2 font-mono text-xs">{s.message}</p>}
              {service.word === "Attention" && s && s.restarts > 0 && (
                <p className="text-muted-foreground">
                  Restarted {s.restarts} {s.restarts === 1 ? "time" : "times"}.
                </p>
              )}
              {service.word === "Starting" && <p className="text-muted-foreground">Give it a moment. The page opens by itself once it answers.</p>}
              {service.word === "Online" && !web && <p className="text-muted-foreground">{entry?.summary ?? "This Service offers no page; it works in the background."}</p>}
              {service.word === "Stopped" && !service.nodeUnreachable && <p className="text-muted-foreground">Stopped. Its data stays where it is.</p>}
            </>
          )}
    </PageCard>
  );
}

/** The card that takes the page's place: the Service's icon, name and
 * state, what there is to say, and what can be done. */
function PageCard({ service, children, actions }: { service: FleetService; children: ReactNode; actions: ReactNode }) {
  return (
    <div className="flex h-full items-center justify-center p-8">
      <Card className="w-full max-w-md">
        <CardHeader className="flex-row items-center gap-3">
          <ServiceIcon catalogId={service.spec.catalogId} word={service.word} size={44} />
          <div className="min-w-0">
            <CardTitle className="truncate">{service.spec.name}</CardTitle>
            <CardDescription>
              <StatusWord word={service.word} />
              {service.node && <> · on {service.node.name}</>}
            </CardDescription>
          </div>
        </CardHeader>
        <CardContent className="space-y-3 text-sm">
          {children}
          <div className="flex flex-wrap gap-2 pt-1">{actions}</div>
        </CardContent>
      </Card>
    </div>
  );
}

/** Stands in for the Service's web page in the demo Fleet, which has no network. */
function DemoFrame({ service, page }: { service: FleetService; page: PageView | undefined }) {
  const entry = catalogById.get(service.spec.catalogId);
  // The stand-in is in the Console's own document, so a modal's backdrop
  // can read its colours straight off the DOM (decision 293).
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    setSampler(async () => (ref.current ? sampleElement(ref.current) : []));
    return () => setSampler(undefined);
  }, []);
  return (
    <PageSurface>
    <div ref={ref} data-demo-frame className="flex h-full flex-col bg-white text-neutral-800 dark:bg-neutral-900 dark:text-neutral-100">
      <div className="flex items-center gap-3 border-b px-5 py-3">
        <ServiceIcon catalogId={service.spec.catalogId} word="Online" size={32} />
        <div className="text-lg font-semibold">
          {entry?.name ?? service.spec.name}
          {page && !page.first && <span className="ml-2 text-sm font-normal text-muted-foreground">{page.label}</span>}
        </div>
        <div className="ml-auto font-mono text-xs text-muted-foreground">{page?.link}</div>
      </div>
      <div className="grid flex-1 grid-cols-[200px_1fr]">
        <div className="space-y-2 border-r p-4">
          {["Home", "Library", "Settings"].map((x) => (
            <div key={x} className="rounded-md bg-muted/60 px-3 py-2 text-sm">
              {x}
            </div>
          ))}
        </div>
        <div className="p-6">
          <div className="mb-4 text-sm text-muted-foreground">This is where {entry?.name ?? service.spec.name}'s own page shows. The demo Fleet has no network, so a stand-in is drawn instead.</div>
          <div className="grid grid-cols-3 gap-3">
            {Array.from({ length: 6 }).map((_, i) => (
              <div key={i} className="aspect-video rounded-lg bg-muted" />
            ))}
          </div>
        </div>
      </div>
    </div>
    </PageSurface>
  );
}
