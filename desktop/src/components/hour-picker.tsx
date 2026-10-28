// A time selector for whole hours (decision 197): the time as a button,
// opening a clock face of the day's 24 hours grouped by part of the day.
// Arrow keys on the button step the hour without opening it.
import { useState } from "react";
import { Clock } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";

export const hourLabel = (h: number) => `${String(h).padStart(2, "0")}:00`;

const PARTS: [string, number][] = [
  ["Night", 0],
  ["Morning", 6],
  ["Afternoon", 12],
  ["Evening", 18],
];

export function HourPicker({ value, onChange, label, highlight }: { value: number; onChange: (h: number) => void; label: string; highlight?: (h: number) => boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="w-24 justify-start font-mono tabular-nums"
          aria-label={`${label}: ${hourLabel(value)}`}
          onKeyDown={(e) => {
            const step = e.key === "ArrowUp" || e.key === "ArrowRight" ? 1 : e.key === "ArrowDown" || e.key === "ArrowLeft" ? -1 : 0;
            if (!step || open) return;
            e.preventDefault();
            onChange((value + step + 24) % 24);
          }}
        >
          <Clock className="text-muted-foreground" />
          {hourLabel(value)}
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-auto p-3" align="start">
        <div className="mb-2 text-xs font-medium text-muted-foreground">{label}</div>
        <div className="grid grid-cols-[auto_repeat(6,2.75rem)] items-center gap-1" role="listbox" aria-label={label}>
          {PARTS.map(([part, from]) => (
            <div key={part} className="contents">
              <span className="pr-2 text-xs text-muted-foreground">{part}</span>
              {Array.from({ length: 6 }, (_, i) => from + i).map((h) => (
                <button
                  key={h}
                  type="button"
                  role="option"
                  aria-selected={h === value}
                  onClick={() => {
                    onChange(h);
                    setOpen(false);
                  }}
                  className={cn("rounded-md py-1 font-mono text-xs tabular-nums hover:bg-accent hover:text-accent-foreground", highlight?.(h) && "bg-accent/60", h === value && "bg-primary text-primary-foreground hover:bg-primary hover:text-primary-foreground")}
                >
                  {hourLabel(h)}
                </button>
              ))}
            </div>
          ))}
        </div>
      </PopoverContent>
    </Popover>
  );
}
