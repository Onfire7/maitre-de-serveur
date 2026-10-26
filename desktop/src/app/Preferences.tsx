// App preferences (decisions 132, 140, 141): theme, the demo Fleet, and the
// developer shortcuts that used to sit on the home page.
import { useState } from "react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { cn } from "@/lib/utils";
import { isDemo } from "@/demo/flag";
import type { DeviceInfo, KnownNode } from "@/state";
import { useTheme, type ThemePref } from "./theme";

export function PreferencesDialog({ open, onClose, device, onAddDev }: { open: boolean; onClose: () => void; device: DeviceInfo; onAddDev: (n: KnownNode) => void }) {
  const { pref, setPref } = useTheme();
  const [devAddr, setDevAddr] = useState("127.0.0.1:7443");
  const [devFp, setDevFp] = useState("");
  const demo = isDemo();
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Preferences</DialogTitle>
          <DialogDescription>How this app looks and behaves on this computer.</DialogDescription>
        </DialogHeader>
        <div className="space-y-5">
          <div className="space-y-2">
            <Label>Theme</Label>
            <div className="flex gap-1">
              {(["system", "light", "dark"] as ThemePref[]).map((p) => (
                <button key={p} onClick={() => setPref(p)} className={cn("rounded-md border px-3 py-1 text-sm capitalize", pref === p ? "border-primary bg-primary text-primary-foreground" : "hover:bg-accent")}>
                  {p}
                </button>
              ))}
            </div>
          </div>
          <div className="flex items-center justify-between">
            <div>
              <Label>Demo Fleet</Label>
              <p className="text-xs text-muted-foreground">A scripted Fleet instead of real Nodes. Reloads the app.</p>
            </div>
            <Switch
              checked={demo}
              onCheckedChange={(on) => {
                localStorage.setItem("maitre.demo", on ? "1" : "0");
                location.reload();
              }}
            />
          </div>
          <details className="text-sm">
            <summary className="cursor-pointer text-muted-foreground">Developer</summary>
            <div className="mt-2 space-y-2 text-xs text-muted-foreground">
              <p>
                Device fingerprint <code className="break-all">{device.fingerprint}</code>
              </p>
              <p>
                Public key file <code>{device.public_key_path}</code>
              </p>
              <p>
                Add a <code>maitre dev</code> Node by hand (its trust dir must already contain this device's public key):
              </p>
              <div className="flex gap-1">
                <Input value={devAddr} onChange={(e) => setDevAddr(e.target.value)} className="h-8" />
                <Input value={devFp} onChange={(e) => setDevFp(e.target.value)} placeholder="node fingerprint" className="h-8 font-mono" />
                <Button size="sm" disabled={!devAddr || devFp.length !== 64} onClick={() => onAddDev({ node_id: devFp.trim().toLowerCase(), name: "dev node", addresses: [devAddr] })}>
                  Add
                </Button>
              </div>
            </div>
          </details>
        </div>
      </DialogContent>
    </Dialog>
  );
}
