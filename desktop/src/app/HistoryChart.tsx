// A metric over time (decisions 209, 210), as the dataviz skill draws it: a
// 2px line per series in the chart colours (avg), the highest readings a
// faint band above a lone series, solid hairline grid, gaps where the Node
// was off, and a crosshair whose readout lists every series at that time.
// `compact` is the Overview card's sparkline: no title or axes.
import { useEffect, useMemo, useRef, useState } from "react";
import { cn } from "@/lib/utils";
import { bandPath, formatTime, linePath, niceMax, type Point, type RangeId } from "@/fleet/metrics";

export interface ChartSeries {
  label: string;
  points: Point[];
  color: "chart-1" | "chart-2";
}

const STROKE = { "chart-1": "stroke-chart-1", "chart-2": "stroke-chart-2" };
const BG = { "chart-1": "bg-chart-1", "chart-2": "bg-chart-2" };

export function HistoryChart({
  title,
  series,
  format,
  percent,
  from,
  to,
  range,
  compact,
  dim,
  empty: emptyText,
  className,
}: {
  title?: string;
  series: ChartSeries[];
  format: (v: number) => string;
  /** 0 to 100; otherwise from 0 to a round number above the highest reading. */
  percent?: boolean;
  from: number;
  to: number;
  range: RangeId;
  compact?: boolean;
  /** Loading the next range: the last one stays, faded (dataviz: refetch keeps the frame). */
  dim?: boolean;
  /** Shown over a chart with no readings. */
  empty?: string;
  className?: string;
}) {
  const ref = useRef<SVGSVGElement>(null);
  const [hover, setHover] = useState<number | null>(null);
  const W = 300;
  const H = compact ? 44 : 64;
  const span = to - from || 1;
  const shown = useMemo(() => series.map((s) => ({ ...s, points: s.points.filter((p) => p.t >= from && p.t <= to) })), [series, from, to]);
  const base = shown[0]?.points ?? [];
  useEffect(() => setHover(null), [base.length === 0, range]);
  // The scale takes in the highest readings where their band is drawn (one series).
  const top = percent ? 100 : niceMax(Math.max(0, ...shown.flatMap((s) => s.points.flatMap((p) => (shown.length === 1 ? [p.v, p.max] : [p.v])).filter(Number.isFinite))));
  const x = (t: number) => ((t - from) / span) * W;
  const y = (v: number) => H - 2 - (Math.min(top, Math.max(0, v)) / top) * (H - 4);
  const last = [...base].reverse().find((p) => Number.isFinite(p.v));
  const empty = !shown.some((s) => s.points.some((p) => Number.isFinite(p.v)));
  const at = hover !== null ? base[hover] : undefined;
  const grid = compact ? [25, 50, 75].map((g) => (g / 100) * top) : [0, top / 2, top];

  return (
    <figure className={cn(!compact && "min-w-0", className)}>
      {!compact && (
        <figcaption className="mb-3.5 flex h-4 items-center justify-between gap-2 text-xs">
          <span className="font-medium">{title}</span>
          {shown.length > 1 ? (
            // A legend for two series: a line key beside each name, in text ink.
            <span className="flex gap-3 text-muted-foreground">
              {shown.map((s) => (
                <span key={s.label} className="flex items-center gap-1">
                  <span className={cn("h-0.5 w-3 rounded-full", BG[s.color])} />
                  {s.label}
                </span>
              ))}
            </span>
          ) : (
            last && <span className="text-muted-foreground tabular-nums">{format(last.v)}</span>
          )}
        </figcaption>
      )}
      <div className={cn("relative transition-opacity", compact && "mt-1", dim && "opacity-60")}>
        <svg
          ref={ref}
          viewBox={`0 0 ${W} ${H}`}
          preserveAspectRatio="none"
          className={cn("w-full overflow-visible", compact ? "h-11" : "h-16")}
          role="img"
          aria-label={empty ? `${title ?? series[0]?.label}: no readings in this range` : `${title ?? series[0]?.label}: ${shown.map((s) => `${s.label} ${format([...s.points].reverse().find((p) => Number.isFinite(p.v))?.v ?? NaN)}`).join(", ")} at the latest`}
          onPointerMove={(e) => {
            if (base.length < 1 || !ref.current) return;
            const r = ref.current.getBoundingClientRect();
            const px = ((e.clientX - r.left) / r.width) * W;
            let best = 0;
            base.forEach((p, i) => Math.abs(x(p.t) - px) < Math.abs(x(base[best].t) - px) && (best = i));
            setHover(best);
          }}
          onPointerLeave={() => setHover(null)}
        >
          {grid.map((g) => (
            <line key={g} x1={0} x2={W} y1={y(g)} y2={y(g)} className={g === 0 ? "stroke-muted-foreground/40" : "stroke-border"} strokeWidth={1} vectorEffect="non-scaling-stroke" />
          ))}
          {shown.length === 1 && <path d={bandPath(base.map((p) => ({ x: x(p.t), lo: y(p.v), hi: y(p.max) })))} className="fill-chart-1 opacity-15" />}
          {shown.map((s) => (
            <path key={s.label} d={linePath(s.points.map((p) => ({ x: x(p.t), y: Number.isFinite(p.v) ? y(p.v) : NaN })))} fill="none" className={STROKE[s.color]} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" />
          ))}
          {at && <line x1={x(at.t)} x2={x(at.t)} y1={0} y2={H} className="stroke-muted-foreground" strokeWidth={1} vectorEffect="non-scaling-stroke" />}
        </svg>
        {!compact && (
          // The top of the scale, on its gridline (axis text in muted ink).
          <span className="pointer-events-none absolute -top-1 left-0 -translate-y-full text-[10px] leading-none text-muted-foreground tabular-nums">{format(top)}</span>
        )}
        {compact && last && hover === null && (
          // The latest reading marked, as HTML so it stays round in the stretched SVG.
          <span className="pointer-events-none absolute size-2 -translate-x-1/2 -translate-y-1/2 rounded-full border-2 border-card bg-chart-1" style={{ left: `${(x(last.t) / W) * 100}%`, top: `${(y(last.v) / H) * 100}%` }} />
        )}
        {at &&
          shown.map((s) => {
            const p = s.points.find((q) => q.t === at.t);
            return p && Number.isFinite(p.v) ? <span key={s.label} className={cn("pointer-events-none absolute size-2 -translate-x-1/2 -translate-y-1/2 rounded-full border-2 border-card", BG[s.color])} style={{ left: `${(x(p.t) / W) * 100}%`, top: `${(y(p.v) / H) * 100}%` }} /> : null;
          })}
        {at && (
          <div className={"pointer-events-none absolute bottom-full z-10 mb-1 -translate-x-1/2 whitespace-nowrap rounded-md border bg-popover px-2 py-1 text-xs shadow-sm"} style={{ left: `${Math.min(80, Math.max(20, (x(at.t) / W) * 100))}%` }}>
            <div className="text-muted-foreground">{formatTime(at.t, range, true)}</div>
            {shown.map((s) => {
              const p = s.points.find((q) => q.t === at.t);
              const ok = p && Number.isFinite(p.v);
              return (
                <div key={s.label} className="flex items-center gap-1.5">
                  {shown.length > 1 && <span className={cn("h-0.5 w-2.5 rounded-full", BG[s.color])} />}
                  {ok ? (
                    <>
                      <span className="font-semibold tabular-nums">{format(p.v)}</span>
                      {shown.length > 1 && <span className="text-muted-foreground">{s.label}</span>}
                      {Number.isFinite(p.max) && p.max > p.v && <span className="text-muted-foreground tabular-nums">· highest {format(p.max)}</span>}
                    </>
                  ) : (
                    <span className="text-muted-foreground">{shown.length > 1 ? `${s.label}: ` : ""}no reading</span>
                  )}
                </div>
              );
            })}
          </div>
        )}
        {empty && <div className="absolute inset-0 flex items-center justify-center text-xs text-muted-foreground">{emptyText ?? "No readings in this range"}</div>}
      </div>
      {!compact && (
        <div className="mt-0.5 flex justify-between text-[10px] text-muted-foreground tabular-nums">
          <span>{formatTime(from, range)}</span>
          <span>{formatTime(from + span / 2, range)}</span>
          <span>{formatTime(to, range)}</span>
        </div>
      )}
    </figure>
  );
}
