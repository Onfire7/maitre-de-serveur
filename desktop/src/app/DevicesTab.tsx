// Trusted Devices and devices asking to join (decisions 148, 150).
import { useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { createClient } from "@connectrpc/connect";
import { create, toBinary } from "@bufbuild/protobuf";
import { toast } from "sonner";
import { Laptop, KeyRound, Smartphone } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { TrustedDeviceSchema } from "@/gen/maitre/v1/fleet_pb";
import { transportFor } from "@/transport";
import type { FleetSnapshot } from "@/fleet/useFleet";
import type { DeviceInfo, Found } from "@/state";
import { useQueryClient } from "@tanstack/react-query";

export function DevicesTab({ snap, device, found, onPromote, scoped }: { snap: FleetSnapshot | undefined; device: DeviceInfo; found: Found[]; onPromote: () => void; scoped: boolean }) {
  const qc = useQueryClient();
  const asking = found.filter((f) => f.kind === "device" && f.key_pem);
  const [codes, setCodes] = useState<Record<string, string>>({});
  const [done, setDone] = useState<Record<string, boolean>>({});
  useEffect(() => {
    for (const d of asking) if (!codes[d.id]) invoke<string>("device_code", { keyPem: d.key_pem }).then((c) => setCodes((m) => ({ ...m, [d.id]: c }))).catch(() => {});
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [asking.map((d) => d.id).join(",")]);
  async function approve(d: Found) {
    if (!snap) return toast.error("no Node of the Fleet can be reached");
    const body = toBinary(TrustedDeviceSchema, create(TrustedDeviceSchema, { deviceId: d.id, name: d.name, publicKeyPem: d.key_pem, addedUnixMs: BigInt(Date.now()) }));
    try {
      await createClient(NodeService, transportFor({ addr: snap.anchor.addr, fingerprint: snap.anchor.nodeId })).putRecord({ record: { kind: "trusted_device", id: d.id, body } });
      setDone((m) => ({ ...m, [d.id]: true }));
      toast.success(`${d.name} can now manage the Fleet`);
      qc.invalidateQueries({ queryKey: ["fleet"] });
    } catch (e) {
      toast.error(String((e as Error).message ?? e));
    }
  }
  return (
    <div className="max-w-2xl space-y-8">
      {asking.length > 0 && (
        <section className="space-y-3">
          <h2 className="text-base font-semibold">Asking to join</h2>
          <ul className="space-y-2">
            {asking.map((d) => (
              <li key={d.id} className="flex items-center gap-4 rounded-xl border border-status-starting/50 bg-status-starting/10 p-4">
                <Smartphone className="size-6 shrink-0 text-muted-foreground" />
                <div className="min-w-0 flex-1">
                  <div className="font-medium">
                    {d.name || "A device"}
                    {d.machine && <span className="text-muted-foreground"> · {d.machine}</span>}
                  </div>
                  <div className="text-xs text-muted-foreground">Approve only if the code on its screen matches.</div>
                </div>
                <code className="rounded-md bg-background px-3 py-1.5 font-mono text-xl tracking-widest">{codes[d.id] ?? "…"}</code>
                {done[d.id] ? <Badge>approved</Badge> : <Button onClick={() => approve(d)}>Approve</Button>}
              </li>
            ))}
          </ul>
        </section>
      )}
      <section className="space-y-3">
        <div className="flex items-center justify-between">
          <h2 className="text-base font-semibold">Trusted Devices</h2>
          {scoped && (
            <Button size="sm" variant="outline" onClick={onPromote}>
              Enter a code from another Node
            </Button>
          )}
        </div>
        <ul className="divide-y rounded-xl border bg-card">
          {(snap?.devices ?? []).map((d) => (
            <li key={d.deviceId} className="flex items-center gap-3 px-4 py-2.5 text-sm">
              {d.recoveryOnly ? <KeyRound className="size-4 text-muted-foreground" /> : <Laptop className="size-4 text-muted-foreground" />}
              <span className="font-medium">{d.name}</span>
              {d.deviceId === device.fingerprint && <Badge variant="secondary">this device</Badge>}
              {d.recoveryOnly && <Badge variant="outline">recovery only</Badge>}
              {d.nodeScope && <Badge className="bg-status-attention text-black">one Node only</Badge>}
              <span className="ml-auto text-xs text-muted-foreground">added {new Date(Number(d.addedUnixMs)).toLocaleDateString()}</span>
            </li>
          ))}
          {!snap && <li className="px-4 py-3 text-sm text-muted-foreground">Reaching the Fleet…</li>}
        </ul>
        <p className="text-xs text-muted-foreground">A device joins by scanning the Fleet from Maître Desktop on it; it then shows up above with a code to compare. The Recovery Phrase can only add devices, nothing else.</p>
      </section>
    </div>
  );
}
