// The Logs tab (decision 187, revised): any Node's journal at full width,
// as its own screen shows it: the Maître agent, the storage pool, Caddy,
// boot health checks, the kernel, everything since boot, or one of its
// Services. Opens at the newest line; search highlights and steps through
// matches. Calls go straight to the Node (160). Service log pages open here.
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { ArrowDown, ChevronDown, ChevronUp, Copy, Pause, Play, Search } from "lucide-react";
import { toast } from "sonner";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { cn } from "@/lib/utils";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { nodeDown, nodeWord } from "@/fleet/useFleet";
import { services as serviceClient, targetFor } from "@/fleet/actions";
import type { ConsoleState } from "@/state";
import { transportFor } from "@/transport";
import { useRoute } from "./routes";

const LINES = 1000;
const BUILT_IN = [
  { id: "agent", label: "Maître agent" },
  { id: "pool", label: "Storage pool" },
  { id: "caddy", label: "Web routes (Caddy)" },
  { id: "health", label: "Boot health checks" },
  { id: "kernel", label: "Kernel" },
  { id: "system", label: "Everything since boot" },
];

export function LogsTab({ snap, state, initial }: { snap: FleetSnapshot | undefined; state: ConsoleState; initial?: { nodeId: string; source: string } }) {
  const nodes = snap?.nodes ?? [];
  // `?nodelogs=<letter>` picks that Node on load (screenshot fixture).
  const fixture = useMemo(() => new URLSearchParams(window.location.search).get("nodelogs"), []);
  const [nodeId, setNodeId] = useState(initial?.nodeId ?? "");
  const [source, setSource] = useState(initial?.source ?? "agent");
  useEffect(() => {
    if (initial) {
      setNodeId(initial.nodeId);
      setSource(initial.source);
    }
  }, [initial?.nodeId, initial?.source]);
  const effNode = nodeId || nodes.find((n) => n.letter === fixture)?.nodeId || snap?.anchor.nodeId || nodes[0]?.nodeId || "";
  const node = nodes.find((n) => n.nodeId === effNode);
  const target = targetFor(snap, state.nodes, effNode);
  const down = nodeDown(nodeWord(snap?.status.get(effNode), Date.now()));
  const [paused, setPaused] = useState(false);
  const q = useQuery({
    queryKey: ["node-logs", target?.addr, source],
    enabled: !!target && !down,
    refetchInterval: paused ? false : 2000,
    retry: false,
    queryFn: async () => {
      try {
        return await createClient(NodeService, transportFor(target!)).getNodeLogs({ source, lines: LINES });
      } catch (e) {
        // A Node from before GetNodeLogs still has its Services' logs.
        if (e instanceof ConnectError && e.code === Code.Unimplemented && source.startsWith("service:")) {
          const lines = (await serviceClient(target!).getServiceLogs({ name: source.slice(8), lines: LINES })).lines;
          return { lines, sources: [], old: true };
        }
        throw e;
      }
    },
  });
  const old = (q.error instanceof ConnectError && q.error.code === Code.Unimplemented) || (q.data && "old" in q.data);
  // Sources: what the Node lists, else the built-in ones and its Services.
  const sources = q.data?.sources.length ? q.data.sources.map((s) => ({ id: s.id, label: s.label })) : [...BUILT_IN, ...(snap?.specs ?? []).filter((s) => s.nodeId === effNode).map((s) => ({ id: `service:${s.name}`, label: s.name }))];
  const lines = q.data?.lines ?? [];
  const label = sources.find((s) => s.id === source)?.label ?? source;

  // Search: matches are highlighted and stepped through; "Only matching
  // lines" hides the rest.
  // `?logsearch=<text>` searches on load (screenshot fixture).
  const [search, setSearch] = useState(() => new URLSearchParams(window.location.search).get("logsearch") ?? "");
  const [onlyMatching, setOnlyMatching] = useState(false);
  const [current, setCurrent] = useState(0);
  const needle = search.trim().toLowerCase();
  const shown = needle && onlyMatching ? lines.filter((l) => l.toLowerCase().includes(needle)) : lines;
  const matches = useMemo(() => (needle ? shown.flatMap((l, i) => (l.toLowerCase().includes(needle) ? [i] : [])) : []), [shown, needle]);
  // A search starts at the newest match, like the log starts at the
  // newest line, until the reader steps.
  const [stepped, setStepped] = useState(false);
  useEffect(() => setStepped(false), [needle, onlyMatching, effNode, source]);
  useEffect(() => {
    if (!stepped) setCurrent(Math.max(0, matches.length - 1));
  }, [matches.length, stepped]);
  const box = useRef<HTMLDivElement>(null);
  const [follow, setFollow] = useState(true);
  // Start at the bottom, and stay there while following.
  useLayoutEffect(() => {
    const el = box.current;
    if (el && follow && !needle) el.scrollTop = el.scrollHeight;
  }, [q.dataUpdatedAt, follow, needle, shown.length]);
  useEffect(() => {
    setFollow(true);
  }, [effNode, source]);
  function step(d: number) {
    if (!matches.length) return;
    const next = (current + d + matches.length) % matches.length;
    setCurrent(next);
    setStepped(true);
    setFollow(false);
    box.current?.querySelector(`[data-line="${matches[next]}"]`)?.scrollIntoView({ block: "center" });
  }
  useEffect(() => {
    if (needle && matches.length && !stepped) box.current?.querySelector(`[data-line="${matches[current]}"]`)?.scrollIntoView({ block: "center" });
  }, [needle, current, stepped, q.dataUpdatedAt]);
  const searchBox = useRef<HTMLInputElement>(null);
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "f") {
        e.preventDefault();
        searchBox.current?.focus();
        searchBox.current?.select();
      }
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, []);

  return (
    <div className="flex h-full min-h-0 flex-col gap-3">
      <div className="flex items-center gap-2">
        <Select value={effNode} onValueChange={(v) => (setNodeId(v), setSource("agent"))}>
          <SelectTrigger size="sm" className="w-36 shrink-0" aria-label="Node">
            <SelectValue placeholder="Node" />
          </SelectTrigger>
          <SelectContent>
            {nodes.map((n) => (
              <SelectItem key={n.nodeId} value={n.nodeId}>
                {n.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={source} onValueChange={setSource}>
          <SelectTrigger size="sm" className="w-52 shrink-0" aria-label="Log source">
            <SelectValue>{label}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {sources.map((s) => (
              <SelectItem key={s.id} value={s.id}>
                {s.id.startsWith("service:") ? `Service: ${s.label}` : s.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <div className="relative max-w-72 min-w-36 flex-1">
          <Search className="pointer-events-none absolute top-1/2 left-2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            ref={searchBox}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") (e.preventDefault(), step(e.shiftKey ? -1 : 1));
              if (e.key === "Escape") setSearch("");
            }}
            placeholder="Search"
            aria-label="Search the log"
            className="h-8 w-full pl-8"
          />
        </div>
        {needle && (
          <>
            <span className="shrink-0 text-xs text-muted-foreground tabular-nums" aria-live="polite">
              {matches.length ? `${current + 1} of ${matches.length}` : "no matches"}
            </span>
            <Button size="icon" variant="ghost" className="size-8" onClick={() => step(-1)} disabled={!matches.length} aria-label="Previous match">
              <ChevronUp />
            </Button>
            <Button size="icon" variant="ghost" className="size-8" onClick={() => step(1)} disabled={!matches.length} aria-label="Next match">
              <ChevronDown />
            </Button>
            <label className="flex shrink-0 items-center gap-1.5 text-xs whitespace-nowrap text-muted-foreground">
              <Switch checked={onlyMatching} onCheckedChange={setOnlyMatching} /> Only matching lines
            </label>
          </>
        )}
        <Button size="icon" variant="ghost" className="ml-auto size-8 shrink-0" onClick={() => setPaused((p) => !p)} title={paused ? "Resume" : "Pause"} aria-label={paused ? "Resume" : "Pause"}>
          {paused ? <Play /> : <Pause />}
        </Button>
        <Button size="icon" variant="ghost" className="size-8" onClick={() => navigator.clipboard?.writeText(shown.join("\n")).then(() => toast("Log copied"))} title="Copy what is shown" aria-label="Copy">
          <Copy />
        </Button>
      </div>
      <div className="relative min-h-0 flex-1">
        <div
          ref={box}
          role="log"
          aria-label={`${node?.name ?? "Node"}: ${label}`}
          onScroll={(e) => {
            const el = e.currentTarget;
            setFollow(el.scrollHeight - el.scrollTop - el.clientHeight < 24);
          }}
          className="h-full overflow-auto rounded-xl border bg-muted/40 p-3 font-mono text-xs leading-relaxed"
        >
          {down ? (
            <p className="text-muted-foreground">{node?.name ?? "This Node"} is unreachable. Its logs show when it is back.</p>
          ) : old && !q.data ? (
            <p className="text-muted-foreground">{node?.name} runs an older Maître Server without Node logs. Update it from the Nodes tab; its Services' logs show here meanwhile.</p>
          ) : q.error ? (
            <p className="text-destructive">{String((q.error as Error).message ?? q.error)}</p>
          ) : !q.data ? (
            <p className="text-muted-foreground">Reading…</p>
          ) : shown.length === 0 ? (
            <p className="text-muted-foreground">{needle ? "No line matches." : "Nothing logged yet."}</p>
          ) : (
            shown.map((l, i) => (
              <div key={i} data-line={i} className={cn("whitespace-pre-wrap break-all", /\b(error|failed|fatal|panic)\b/i.test(l) && "text-destructive", /\bwarn(ing)?\b/i.test(l) && "text-status-attention", needle && matches[current] === i && "rounded-sm bg-primary/10 ring-1 ring-primary/40")}>
                <Highlight text={l} needle={needle} />
              </div>
            ))
          )}
        </div>
        {!follow && !needle && (
          <Button size="sm" variant="secondary" className="absolute right-4 bottom-4 shadow" onClick={() => setFollow(true)}>
            <ArrowDown /> Newest
          </Button>
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        {node?.name}: {label}, the last {LINES} lines{paused ? ", paused" : ", refreshed every two seconds"}. Ctrl+F searches; Enter and Shift+Enter step through matches.
      </p>
    </div>
  );
}

function Highlight({ text, needle }: { text: string; needle: string }) {
  if (!needle) return <>{text}</>;
  const parts: React.ReactNode[] = [];
  const low = text.toLowerCase();
  let at = 0;
  for (let i = low.indexOf(needle); i >= 0; i = low.indexOf(needle, i + needle.length)) {
    parts.push(text.slice(at, i), <mark key={i} className="rounded-sm bg-status-attention/40 text-inherit">{text.slice(i, i + needle.length)}</mark>);
    at = i + needle.length;
  }
  parts.push(text.slice(at));
  return <>{parts}</>;
}

/** Opens a Node's logs on the Logs tab, from anywhere in the Console. */
export function useNodeLogs() {
  const { go } = useRoute();
  return (nodeId: string, source = "agent") => go({ name: "fleet", tab: "logs", logs: { nodeId, source } });
}
