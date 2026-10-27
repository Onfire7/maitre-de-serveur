// A Service's page is its own web page, full-bleed, with no bar above it
// (decision 138). When it is not Online, or has no web page, a status card
// takes its place (decision 144).
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

export function ServicePage({ service, target, onAction }: { service: FleetService; target: Target | undefined; onAction: (a: "start" | "restart" | "logs" | "open") => void }) {
  const s = service.status;
  const web = !!s?.url;
  const online = service.word === "Online" && web;
  // Decision 71: the page is read through a loopback proxy that tunnels to
  // the Node over its mutual-TLS listener, so it needs neither mDNS nor the
  // Service being reachable on the LAN, and the window may embed it.
  const local = useQuery({
    queryKey: ["service-url", service.spec.name, target?.addr],
    enabled: online && !!target && !isDemo(),
    staleTime: Infinity,
    retry: 1,
    queryFn: () => invoke<string>("service_url", { addr: target!.addr, fingerprint: target!.fingerprint, service: service.spec.name }),
  });
  if (online) {
    if (isDemo()) return <DemoFrame service={service} />;
    // Outside the app (no Tauri) the proxy is not there: use the address.
    const src = local.data ?? (local.error ? s!.url : undefined);
    if (!src) return null;
    return <iframe key={src} title={service.spec.name} src={src} className="h-full w-full border-0 bg-white" sandbox="allow-same-origin allow-scripts allow-forms allow-popups allow-downloads" />;
  }
  const entry = catalogById.get(service.spec.catalogId);
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
              {service.word === "Online" && !web && <p className="text-muted-foreground">{entry?.summary ?? "This Service has no web page; it works in the background."}</p>}
              {service.word === "Stopped" && !service.nodeUnreachable && <p className="text-muted-foreground">Stopped. Its data stays where it is.</p>}
            </>
          )}
          <div className="flex flex-wrap gap-2 pt-1">
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
            {web && service.word === "Online" && (
              <Button size="sm" variant="outline" onClick={() => onAction("open")}>
                <ExternalLink /> Open in your browser
              </Button>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}

/** Stands in for the Service's web page in the demo Fleet, which has no network. */
function DemoFrame({ service }: { service: FleetService }) {
  const entry = catalogById.get(service.spec.catalogId);
  return (
    <div className="flex h-full flex-col bg-white text-neutral-800 dark:bg-neutral-900 dark:text-neutral-100">
      <div className="flex items-center gap-3 border-b px-5 py-3">
        <ServiceIcon catalogId={service.spec.catalogId} word="Online" size={32} />
        <div className="text-lg font-semibold">{entry?.name ?? service.spec.name}</div>
        <div className="ml-auto font-mono text-xs text-muted-foreground">{service.status?.url}</div>
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
  );
}
