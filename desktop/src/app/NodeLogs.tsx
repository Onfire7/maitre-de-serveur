// A Node's logs in the Console (decision 187), as its own screen shows them:
// the Maître agent, the storage pool, Caddy, boot health checks, the kernel,
// the whole journal since boot, or one of its Services. The call goes
// straight to the Node (160); any part of the Console opens the sheet with
// useNodeLogs().
import { createContext, useContext, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { Copy, Pause, Play } from "lucide-react";
import { toast } from "sonner";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { targetFor } from "@/fleet/actions";
import type { KnownNode } from "@/state";
import { transportFor } from "@/transport";

const Ctx = createContext<(nodeId: string, source?: string) => void>(() => {});

export function useNodeLogs() {
  return useContext(Ctx);
}

export function NodeLogsProvider({ snap, known, children }: { snap: FleetSnapshot | undefined; known: KnownNode[]; children: React.ReactNode }) {
  // `?nodelogs=<letter>` opens a Node's logs on load (screenshot fixture).
  const [open, setOpen] = useState<{ nodeId: string; source: string } | null>(null);
  const fixture = useRef(new URLSearchParams(window.location.search).get("nodelogs"));
  useEffect(() => {
    const n = fixture.current && snap?.nodes.find((x) => x.letter === fixture.current);
    if (n) {
      fixture.current = null;
      setOpen({ nodeId: n.nodeId, source: "agent" });
    }
  }, [snap]);
  const show = useMemo(() => (nodeId: string, source = "agent") => setOpen({ nodeId, source }), []);
  const node = open ? snap?.nodes.find((n) => n.nodeId === open.nodeId) : undefined;
  return (
    <Ctx.Provider value={show}>
      {children}
      <NodeLogsSheet key={open?.nodeId ?? ""} nodeId={open?.nodeId ?? null} name={node?.name ?? "Node"} initialSource={open?.source ?? "agent"} target={open ? targetFor(snap, known, open.nodeId) : undefined} onClose={() => setOpen(null)} />
    </Ctx.Provider>
  );
}

function NodeLogsSheet({ nodeId, name, initialSource, target, onClose }: { nodeId: string | null; name: string; initialSource: string; target: ReturnType<typeof targetFor>; onClose: () => void }) {
  const [source, setSource] = useState(initialSource);
  const [filter, setFilter] = useState("");
  const [paused, setPaused] = useState(false);
  const [follow, setFollow] = useState(true);
  const box = useRef<HTMLPreElement>(null);
  const q = useQuery({
    queryKey: ["node-logs", target?.addr, source],
    enabled: !!nodeId && !!target,
    refetchInterval: paused ? false : 2000,
    retry: false,
    queryFn: async () => createClient(NodeService, transportFor(target!)).getNodeLogs({ source, lines: 500 }),
  });
  const old = q.error instanceof ConnectError && q.error.code === Code.Unimplemented;
  const lines = q.data?.lines ?? [];
  const f = filter.trim().toLowerCase();
  const shown = f ? lines.filter((l) => l.toLowerCase().includes(f)) : lines;
  // Keep the newest line in view unless the reader scrolled up.
  useEffect(() => {
    const el = box.current;
    if (el && follow) el.scrollTop = el.scrollHeight;
  }, [shown.length, q.dataUpdatedAt, follow]);
  const label = q.data?.sources.find((s) => s.id === source)?.label ?? "Maître agent";
  return (
    <Sheet open={!!nodeId} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="flex w-[760px] flex-col sm:max-w-[760px]">
        <SheetHeader>
          <SheetTitle>{name} logs</SheetTitle>
          <SheetDescription>
            {label}: the last 500 lines{paused ? ", paused" : ", refreshed every two seconds"}.
          </SheetDescription>
        </SheetHeader>
        <div className="flex flex-wrap items-center gap-2 px-4">
          <Select value={source} onValueChange={setSource}>
            <SelectTrigger size="sm" className="w-56" aria-label="Log source">
              <SelectValue>{label}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {(q.data?.sources ?? [{ id: "agent", label: "Maître agent" }]).map((s) => (
                <SelectItem key={s.id} value={s.id}>
                  {s.id.startsWith("service:") ? `Service: ${s.label}` : s.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="Filter" aria-label="Filter lines" className="h-8 w-48" />
          <span className="flex-1" />
          <Button size="sm" variant="ghost" onClick={() => setPaused((p) => !p)} title={paused ? "Resume" : "Pause"}>
            {paused ? <Play /> : <Pause />} {paused ? "Resume" : "Pause"}
          </Button>
          <Button size="sm" variant="ghost" onClick={() => navigator.clipboard?.writeText(shown.join("\n")).then(() => toast("Log copied"))} title="Copy what is shown">
            <Copy /> Copy
          </Button>
        </div>
        <pre
          ref={box}
          onScroll={(e) => {
            const el = e.currentTarget;
            setFollow(el.scrollHeight - el.scrollTop - el.clientHeight < 24);
          }}
          className="mx-4 mb-4 min-h-0 flex-1 overflow-auto rounded-md bg-muted p-3 font-mono text-[11px] leading-relaxed whitespace-pre-wrap break-all"
        >
          {old ? (
            <span className="text-muted-foreground">{name} runs an older Maître Server without Node logs. Update it from the Nodes tab; until then its Services' logs are in each Service's menu.</span>
          ) : q.error ? (
            <span className="text-destructive">{String((q.error as Error).message ?? q.error)}</span>
          ) : !q.data ? (
            <span className="text-muted-foreground">Reading…</span>
          ) : shown.length === 0 ? (
            <span className="text-muted-foreground">{f ? "No line matches the filter." : "Nothing logged yet."}</span>
          ) : (
            shown.map((l, i) => (
              <div key={i} className={cn(/\b(error|failed|fatal|panic)\b/i.test(l) && "text-destructive", /\bwarn(ing)?\b/i.test(l) && "text-status-attention")}>
                {l}
              </div>
            ))
          )}
        </pre>
      </SheetContent>
    </Sheet>
  );
}
