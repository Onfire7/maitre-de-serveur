// Hold to confirm, after interior.dev's piece (decision 129): press and keep
// pressing for a moment; the fill shows how long is left; release early and
// nothing happens. For actions that are disruptive but not destructive
// (restart a Node, roll back), where a typed name would be too much.
import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export function HoldButton({ onConfirm, holdMs = 1200, className, children, disabled, variant = "default", size = "default" }: { onConfirm: () => void; holdMs?: number; className?: string; children: React.ReactNode; disabled?: boolean; variant?: React.ComponentProps<typeof Button>["variant"]; size?: React.ComponentProps<typeof Button>["size"] }) {
  // `?hold=0.6` draws every hold button that far along (screenshot fixture).
  const [progress, setProgress] = useState(() => Number(new URLSearchParams(window.location.search).get("hold")) || 0);
  const start = useRef<number | null>(null);
  const raf = useRef<number | undefined>(undefined);
  const done = useRef(false);
  const stop = () => {
    start.current = null;
    if (raf.current) cancelAnimationFrame(raf.current);
    setProgress(0);
  };
  const tick = () => {
    if (start.current === null) return;
    const p = Math.min(1, (performance.now() - start.current) / holdMs);
    setProgress(p);
    if (p >= 1) {
      if (!done.current) {
        done.current = true;
        onConfirm();
      }
      stop();
      return;
    }
    raf.current = requestAnimationFrame(tick);
  };
  const begin = () => {
    if (disabled) return;
    done.current = false;
    start.current = performance.now();
    raf.current = requestAnimationFrame(tick);
  };
  // On unmount only stop the frames; setting state there is pointless.
  useEffect(
    () => () => {
      if (raf.current) cancelAnimationFrame(raf.current);
    },
    [],
  );
  return (
    <Button
      variant={variant}
      size={size}
      disabled={disabled}
      className={cn("relative select-none overflow-hidden", className)}
      onPointerDown={begin}
      onPointerUp={stop}
      onPointerLeave={stop}
      onPointerCancel={stop}
      onKeyDown={(e) => {
        if ((e.key === " " || e.key === "Enter") && start.current === null) begin();
      }}
      onKeyUp={stop}
      onClick={(e) => e.preventDefault()}
      aria-label={typeof children === "string" ? `${children} (hold)` : undefined}
    >
      {/* The fill must read on every variant: light on red and dark
          buttons, dark on the near-white primary of the dark theme. */}
      <span className={cn("pointer-events-none absolute inset-y-0 left-0", variant === "default" ? "bg-white/30 dark:bg-black/30" : variant === "destructive" ? "bg-white/40 dark:bg-white/30" : "bg-foreground/15")} style={{ width: `${progress * 100}%` }} />
      <span className="relative flex items-center gap-2">{children}</span>
    </Button>
  );
}
