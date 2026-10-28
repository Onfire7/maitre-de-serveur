// The time range every graph on a page shows (decision 210): one picker in
// the page header, on the title row at the right, shared while the Console
// runs. Any page with graphs puts it in the same spot.
import { cn } from "@/lib/utils";
import { RANGES, setRange, useRange } from "@/fleet/metrics";

export function RangePicker({ className }: { className?: string }) {
  const range = useRange();
  return (
    <div role="radiogroup" aria-label="Time range of the graphs" className={cn("inline-flex items-center rounded-lg bg-muted p-0.5 text-xs", className)}>
      {RANGES.map((r) => (
        <button
          key={r.id}
          type="button"
          role="radio"
          aria-checked={range === r.id}
          onClick={() => setRange(r.id)}
          className={cn("rounded-md px-2.5 py-1 whitespace-nowrap transition-colors", range === r.id ? "bg-background font-medium text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")}
        >
          {r.label}
        </button>
      ))}
    </div>
  );
}
