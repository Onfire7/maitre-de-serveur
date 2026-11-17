import { useEffect, useState } from "react";
import { cn } from "@/lib/utils";

/**
 * A part of a TimedBar, its width the Node's typical time for it. A
 * section fills either by a fraction it is given (Downloading, by the
 * staging percentage, never past its width), or by the time since it began
 * (Restarting): such a timed section runs on into Overtime, and the bar
 * turns red once it passes its typical time plus a tenth.
 */
export type TimedSection = { label: string; typicalMs: number } & ({ progress: number } | { since: number | null });

/** The hint the bar shows once it is red. */
export const LATE_HINT = "Taking longer than usual. Its monitor shows the Maître banner when it is back, or an error if it is not.";

/**
 * A progress bar against how long a Node usually takes: the sections'
 * typical times end at 75% of the scale, marked with a line labelled
 * "usually about 2 min"; the last quarter is overtime. Labelled with the
 * time elapsed since `since`, and ticks itself each second. Without a figure
 * (the sections' typical times sum to 0) it is the pulsing bar, labelled
 * with the elapsed time, no marker. The sections, Overtime last, are
 * labelled under the bar.
 */
export function TimedBar({ since, sections, className }: { since: number; sections: TimedSection[]; className?: string }) {
  const now = useNow();
  const elapsed = Math.max(0, now - since);
  const typical = sections.reduce((s, x) => s + x.typicalMs, 0);
  const label = formatElapsed(elapsed);

  if (typical <= 0)
    return (
      <div className={cn("space-y-1", className)}>
        <div role="progressbar" aria-label="Progress" aria-valuetext={label} className="h-1.5 overflow-hidden rounded bg-muted">
          <div className="h-full w-1/3 animate-pulse rounded bg-status-starting" />
        </div>
        <p className="text-xs text-muted-foreground tabular-nums">{label}</p>
      </div>
    );

  const scale = typical / 0.75;
  let overtime = 0;
  let late = false;
  const fills = sections.map((s) => {
    let done: number;
    if ("progress" in s) done = Math.min(1, Math.max(0, s.progress)) * s.typicalMs;
    else {
      const ran = s.since === null ? 0 : Math.max(0, now - s.since);
      done = Math.min(ran, s.typicalMs);
      overtime += ran - done;
      if (ran > s.typicalMs * 1.1) late = true;
    }
    return { label: s.label, width: s.typicalMs / scale, fill: done / scale };
  });
  const over = Math.min(0.25, overtime / scale);
  const total = Math.min(1, fills.reduce((s, f) => s + f.fill, 0) + over);
  const color = late ? "bg-destructive" : "bg-status-starting";

  return (
    <div className={cn("space-y-1", className)}>
      <div className="relative pt-4">
        <div data-marker className="absolute top-0 -translate-x-1/2 whitespace-nowrap text-[10px] text-muted-foreground" style={{ left: "75%" }}>
          {usually(typical)}
        </div>
        <div
          role="progressbar"
          aria-label="Progress"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={Math.round(total * 100)}
          aria-valuetext={label}
          data-state={late ? "late" : "running"}
          className="relative flex h-1.5 overflow-hidden rounded bg-muted"
        >
          {fills.map((f, i) => (
            <div key={i} title={f.label} className="h-full" style={{ width: `${f.width * 100}%` }}>
              <div className={cn("h-full transition-[width]", color)} style={{ width: `${f.width ? (f.fill / f.width) * 100 : 0}%` }} />
            </div>
          ))}
          <div title="Overtime" className="h-full" style={{ width: "25%" }}>
            <div className={cn("h-full transition-[width]", color)} style={{ width: `${over * 400}%` }} />
          </div>
        </div>
        <div className="absolute bottom-0 h-2.5 w-px -translate-x-1/2 bg-foreground/60" style={{ left: "75%" }} />
      </div>
      <ul aria-label="Sections" className="flex text-[10px] text-muted-foreground">
        {[...fills, { label: "Overtime", width: 0.25 }].map((f, i) => (
          <li key={i} className="truncate" style={{ width: `${f.width * 100}%` }}>
            {f.label}
          </li>
        ))}
      </ul>
      <p className="text-xs text-muted-foreground tabular-nums">{label}</p>
      {late && <p className="text-xs text-destructive">{LATE_HINT}</p>}
    </div>
  );
}

/** "usually about 2 min", "usually under a minute". */
function usually(ms: number) {
  return ms < 60_000 ? "usually under a minute" : `usually about ${Math.round(ms / 60_000)} min`;
}

/** "42 s", "2 min 5 s". */
export function formatElapsed(ms: number) {
  const s = Math.floor(ms / 1000);
  return s < 60 ? `${s} s` : `${Math.floor(s / 60)} min ${s % 60} s`;
}

/** The time, again each second. */
function useNow() {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);
  return now;
}
