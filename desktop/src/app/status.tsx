// Status treatment (decisions 135 and 136): a brightly coloured border and a
// tinted background on the icon, greyscale when Stopped, nothing when fine.
import { Box, Server } from "lucide-react";
import { useState } from "react";
import { cn } from "@/lib/utils";
import type { FleetService } from "@/fleet/useFleet";
import bundledCatalog from "@/catalog/index.json";
import type { CatalogIndex } from "@/services";

const catalog = bundledCatalog as CatalogIndex;
export const catalogById = new Map(catalog.entries.map((e) => [e.id, e]));

export type ServiceWord = FleetService["word"];
export type NodeWord = "Healthy" | "Unreachable" | "Provisioning" | "Updating" | "Attention";

const ring: Record<ServiceWord | NodeWord, string> = {
  Online: "border-transparent",
  Healthy: "border-transparent",
  Starting: "border-status-starting bg-status-starting/15",
  Provisioning: "border-status-starting bg-status-starting/15",
  Updating: "border-status-starting bg-status-starting/15",
  Attention: "border-status-attention bg-status-attention/20",
  Stopped: "border-transparent grayscale opacity-60",
  Unreachable: "border-transparent grayscale opacity-60",
};

export function statusClass(word: ServiceWord | NodeWord) {
  return ring[word];
}

/** A Service's icon from its Catalog entry, a generic box otherwise, dressed by status. */
export function ServiceIcon({ catalogId, word, size = 28, className }: { catalogId: string; word: ServiceWord; size?: number; className?: string }) {
  const [broken, setBroken] = useState(false);
  const icon = catalogById.get(catalogId)?.icon;
  return (
    <span
      className={cn("inline-flex shrink-0 items-center justify-center rounded-lg border-2 bg-muted transition-[filter,opacity,background-color,border-color] duration-200", ring[word], className)}
      style={{ width: size, height: size }}
    >
      {icon && !broken ? <img src={icon} alt="" draggable={false} onError={() => setBroken(true)} className="rounded-md object-contain" style={{ width: size - 8, height: size - 8 }} /> : <Box className="text-muted-foreground" style={{ width: size - 12, height: size - 12 }} />}
    </span>
  );
}

/** A Node's mark under the Fleet entry. */
export function NodeMark({ word, size = 24, className }: { word: NodeWord; size?: number; className?: string }) {
  return (
    <span className={cn("inline-flex shrink-0 items-center justify-center rounded-md border-2 bg-muted transition-[filter,opacity] duration-200", ring[word], className)} style={{ width: size, height: size }}>
      <Server className="text-muted-foreground" style={{ width: size - 10, height: size - 10 }} />
    </span>
  );
}

export function StatusWord({ word, className }: { word: ServiceWord | NodeWord; className?: string }) {
  const color: Record<ServiceWord | NodeWord, string> = {
    Online: "text-status-online",
    Healthy: "text-status-online",
    Starting: "text-status-starting",
    Provisioning: "text-status-starting",
    Updating: "text-status-starting",
    Attention: "text-status-attention",
    Stopped: "text-muted-foreground",
    Unreachable: "text-muted-foreground",
  };
  return <span className={cn("font-medium", color[word], className)}>{word}</span>;
}
