// Where a Service's page sits (decision 293): the area slides right by the
// open flyout's width, its size untouched so the page never reflows, and
// under a modal the page is hidden behind a blurred stand-in made from a
// few of its colours. The view layer (ServiceViews, decision 294) measures
// this box to place each native view, and the demo's stand-in page draws
// inside it, so both follow the same rules.
import { useEffect, useRef, type ReactNode } from "react";
import { cn } from "@/lib/utils";
import { usePageLayout, type ColorGrid } from "./layout";

export function PageSurface({ children, className, ref }: { children: ReactNode; className?: string; ref?: React.Ref<HTMLDivElement> }) {
  const { offset, covered, colors } = usePageLayout();
  return (
    <div
      ref={ref}
      data-testid="page-surface"
      data-offset={offset}
      data-covered={covered ? "true" : undefined}
      className={cn("absolute inset-0 transition-transform duration-200 ease-out motion-reduce:transition-none", className)}
      style={{ transform: `translateX(${offset}px)` }}
    >
      <div className={cn("absolute inset-0", covered && "invisible")}>{children}</div>
      {covered && <PageBackdrop colors={colors} />}
    </div>
  );
}

/** The blurred stand-in: the colour grid painted on a canvas a few pixels
 * wide, scaled up with smoothing and blurred, so it reads as the page seen
 * through frosted glass without holding any of the page's content. */
export function PageBackdrop({ colors }: { colors: ColorGrid }) {
  const ref = useRef<HTMLCanvasElement>(null);
  const rows = colors.length;
  const cols = rows ? colors[0].length : 0;
  useEffect(() => {
    const c = ref.current;
    const ctx = c?.getContext?.("2d");
    if (!c || !ctx) return;
    for (let r = 0; r < rows; r++) for (let x = 0; x < cols; x++) {
      ctx.fillStyle = colors[r][x];
      ctx.fillRect(x, r, 1, 1);
    }
  }, [colors, rows, cols]);
  return (
    <div data-testid="page-backdrop" className="absolute inset-0 overflow-hidden bg-background">
      <canvas ref={ref} width={cols || 1} height={rows || 1} aria-hidden className="absolute -inset-[12%] h-[124%] w-[124%] blur-3xl" style={{ imageRendering: "auto" }} />
    </div>
  );
}
