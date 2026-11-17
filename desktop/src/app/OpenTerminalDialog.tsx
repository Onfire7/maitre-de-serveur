// Where a new Terminal runs and as whom (ADR 0011, #7): a Service of
// several containers picks one, the first by default; Run as root runs the
// shell as uid 0 instead of the image's default user. Which shell runs is
// the Node's to find (the Catalog's `shell:`, the one set in Service
// Configuration, /bin/bash, /bin/sh).
import { useState } from "react";
import { SquareTerminal } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import type { TerminalChoice } from "@/terminal/session";

/** `containers` are the Service's components; none for a Service of one container. */
export function OpenTerminalDialog({ service, containers, onOpen, onClose }: { service: string; containers: string[]; onOpen: (c: Required<TerminalChoice>) => void; onClose: () => void }) {
  const [component, setComponent] = useState(containers[0] ?? "");
  const [root, setRoot] = useState(false);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Open a Terminal in {service}</DialogTitle>
          <DialogDescription>A shell inside {containers.length > 1 ? "one of its containers" : "its container"}.</DialogDescription>
        </DialogHeader>
        <div className="space-y-4 text-sm">
          {containers.length > 1 && (
            <div>
              <div className="mb-2 text-muted-foreground">Container</div>
              <RadioGroup value={component} onValueChange={setComponent} aria-label="Container" className="gap-2">
                {containers.map((c) => (
                  <Label key={c} className="flex items-center gap-2 font-normal">
                    <RadioGroupItem value={c} aria-label={c} />
                    <span className="font-mono">{c}</span>
                  </Label>
                ))}
              </RadioGroup>
            </div>
          )}
          <Label className="flex items-start gap-2 font-normal">
            <Checkbox checked={root} onCheckedChange={(v) => setRoot(v === true)} aria-label="Run as root" className="mt-0.5" />
            <span>
              Run as root
              <span className="block text-xs text-muted-foreground">Otherwise it runs as the user the image names.</span>
            </span>
          </Label>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={() => onOpen({ component, root })}>
            <SquareTerminal /> Open
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
