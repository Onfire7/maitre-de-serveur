// The Fleet's time zone (decision 282), on the Nodes tab: one zone every
// Node's clock follows and every Service with TZ=auto runs in, with which
// Nodes have taken it.
import { useId, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, Clock } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { consoleTimeZone, fleetTimeZone, knownTimeZones, looksLikeTimeZone, nodeZones, setFleetTimeZone } from "@/fleet/timezone";

export function FleetTimeZone({ snap }: { snap: FleetSnapshot | undefined }) {
  const qc = useQueryClient();
  const listId = useId();
  const current = fleetTimeZone(snap?.records);
  const [draft, setDraft] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const value = draft ?? current;
  const mine = consoleTimeZone();
  const zones = knownTimeZones();
  const states = current ? nodeZones(snap, current) : [];
  const off = states.filter((s) => s.state !== "on");

  async function save(zone: string) {
    if (!snap) return;
    const tz = zone.trim();
    if (tz && !looksLikeTimeZone(tz)) {
      toast.error(`"${tz}" is not a time zone name. Use Area/City, such as Europe/Paris.`);
      return;
    }
    if (tz && zones.length > 0 && !zones.includes(tz)) {
      toast.error(`This computer does not know the time zone "${tz}". Pick one from the list.`);
      return;
    }
    setBusy(true);
    try {
      await setFleetTimeZone(snap, tz);
      setDraft(null);
      await qc.invalidateQueries({ queryKey: ["fleet"] });
      toast.success(tz ? `The Fleet's time zone is ${tz}. Each Node takes it within a minute.` : "The Fleet's time zone is cleared; each Node keeps the one it has.");
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="space-y-3" aria-label="Time zone">
      <h2 className="text-base font-semibold">Time zone</h2>
      <p className="text-sm text-muted-foreground">Every Node sets its clock to the Fleet's time zone, and a Service whose time zone is left on auto runs in it. A Service given its own time zone keeps it.</p>
      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          void save(value);
        }}
      >
        <Clock className="size-4 text-muted-foreground" aria-hidden />
        <Input className="h-8 w-64" list={listId} value={value} placeholder="Not set: each Node keeps its own" aria-label="Fleet time zone" onChange={(e) => setDraft(e.target.value)} />
        <datalist id={listId}>
          {zones.map((z) => (
            <option key={z} value={z} />
          ))}
        </datalist>
        <Button type="submit" size="sm" disabled={busy || !snap || value === current}>
          Save
        </Button>
        {mine && value !== mine && (
          <Button type="button" size="sm" variant="ghost" disabled={busy || !snap} onClick={() => void save(mine)}>
            Use {mine}, this computer's
          </Button>
        )}
        {current && (
          <Button type="button" size="sm" variant="ghost" disabled={busy || !snap} onClick={() => void save("")}>
            Clear
          </Button>
        )}
      </form>
      {current && states.length > 0 && (
        <p className="flex items-center gap-1.5 text-sm text-muted-foreground">
          {off.length === 0 ? (
            <>
              <Check className="size-4 text-emerald-600" aria-hidden />
              {states.length === 1 ? "The Node is on " : `All ${states.length} Nodes are on `}
              {current}.
            </>
          ) : (
            <span>
              {off.map((s, i) => (
                <span key={s.nodeId}>
                  {i > 0 && " "}
                  {s.state === "other" && `${s.name} is still on ${s.zone}; it takes the Fleet's zone within a minute of reading the record, or needs a newer Maître Server.`}
                  {s.state === "unknown" && `${s.name} does not report its time zone; it needs a newer Maître Server.`}
                  {s.state === "unreachable" && `${s.name} is unreachable; it takes the zone when it is back.`}
                </span>
              ))}
            </span>
          )}
        </p>
      )}
    </section>
  );
}
