// Set up a machine booted from the Installer (decisions 133, 147): check the
// pairing code, name the Node and pick its disk, then watch the install.
// Reports progress up so the sidebar can show the new Node as Provisioning.
import { useEffect, useMemo, useState } from "react";
import { ConnectError, createClient } from "@connectrpc/connect";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { Dices, Check, X, HardDrive, Usb, FolderSearch } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { cn } from "@/lib/utils";
import { formatBytes } from "@/lib/format";
import { NodeService } from "@/gen/maitre/v1/node_pb";
import { InstallerService, Provisioning_Phase, type InstallerStatus, type Provisioning as InstallerProvisioning } from "@/gen/maitre/v1/installer_pb";
import { NodeSchema, TrustedDeviceSchema, type Node as FleetNode, type Record as Rec, type TrustedDevice } from "@/gen/maitre/v1/fleet_pb";
import { transportFor } from "@/transport";
import { api, type ConsoleState, type DeviceInfo, type Found, type KnownNode } from "@/state";
import type { FleetSnapshot } from "@/fleet/useFleet";
import { nextLetter, randomNodeName } from "@/names";
import { fingerprintOfPem, putSigned, signedForProvision, signedRecord } from "@/fleet/sign";
import { DiskBrowser, holds } from "./DiskBrowser";
import { restartInstaller } from "./NetbootPanel";
import { HoldButton } from "@/components/hold-button";

/** The Device-signed kinds a new Node of an existing Fleet starts with, copied from a Node of it. */
const PROVISION_KINDS = new Set(["trusted_device", "node", "fleet", "space", "update_policy", "tailscale", "public_settings", "grant"]);

/** A new Fleet's Trusted Devices: this device and the Recovery Phrase, both signed by this device. */
export async function firstDevices(device: DeviceInfo, recoveryPem: string): Promise<Rec[]> {
  const now = BigInt(Date.now());
  const recoveryId = await fingerprintOfPem(recoveryPem);
  const own = create(TrustedDeviceSchema, { deviceId: device.fingerprint, name: device.name, publicKeyPem: device.public_key_pem, recoveryOnly: false, addedUnixMs: now });
  const phrase = create(TrustedDeviceSchema, { deviceId: recoveryId, name: "Recovery Phrase", publicKeyPem: recoveryPem, recoveryOnly: true, addedUnixMs: now });
  return [await signedRecord({ kind: "trusted_device", id: own.deviceId, body: toBinary(TrustedDeviceSchema, own) }), await signedRecord({ kind: "trusted_device", id: phrase.deviceId, body: toBinary(TrustedDeviceSchema, phrase) })];
}

export interface Provisioning {
  name: string;
  percent: number;
  message: string;
}

/** An Install the Installer gave up on: why, and what the failing command printed
 * (decision 265; Installers before it send no output). */
export function InstallFailed({ prov }: { prov: InstallerProvisioning }) {
  return (
    <div className="space-y-1">
      <p className="text-sm text-destructive">Install failed: {prov.message}</p>
      {prov.output.length > 0 && (
        <details className="text-xs">
          <summary className="cursor-pointer text-muted-foreground">What the installer printed ({prov.output.length} lines)</summary>
          <pre className="mt-1 max-h-48 overflow-auto rounded-md border bg-muted/30 p-2 whitespace-pre-wrap break-all">{prov.output.join("\n")}</pre>
        </details>
      )}
    </div>
  );
}

export function AddNode({ found, device, state, snap, onCancel, onProvisioned, onProgress }: { found: Found; device: DeviceInfo; state: ConsoleState; snap: FleetSnapshot | undefined; onCancel: () => void; onProvisioned: (node: KnownNode) => void; onProgress: (p: Provisioning | null) => void }) {
  const [addr, setAddr] = useState(found.addresses[0]);
  const qemuGuest = found.addresses.some((a) => a.startsWith("10.0.2."));
  const [altAddr, setAltAddr] = useState(qemuGuest ? "127.0.0.1:17443" : "");
  const [attempt, setAttempt] = useState(0);
  const [fp, setFp] = useState("");
  const [pem, setPem] = useState("");
  const [code, setCode] = useState("");
  const [status, setStatus] = useState<InstallerStatus | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const fleetNodes = snap?.nodes ?? [];
  const letter = nextLetter(fleetNodes.map((n) => n.letter));
  const takenNames = [...state.nodes.map((n) => n.name), ...fleetNodes.map((n) => n.name), ...(snap?.drives ?? []).map((d) => d.name)];
  const [name, setName] = useState("");
  useEffect(() => {
    if (!name) setName(randomNodeName(letter, takenNames));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [letter]);
  const [disk, setDisk] = useState("");
  const [dataPart, setDataPart] = useState(false);
  const [started, setStarted] = useState(false);
  const [restore, setRestore] = useState("");
  const [error, setError] = useState("");
  // The status poll's own trouble, apart from an Install that was refused,
  // which the next good poll must not wipe.
  const [pollError, setPollError] = useState("");
  // The disk being looked inside (decision 262).
  const [looking, setLooking] = useState("");
  const installer = useMemo(() => (fp ? createClient(InstallerService, transportFor({ addr, fingerprint: fp })) : null), [addr, fp]);

  // Step 1: learn the Installer's key and compute the code we expect.
  useEffect(() => {
    let stop = false;
    const candidates = attempt === 0 ? found.addresses : [addr];
    (async () => {
      let lastErr = "";
      for (const a of candidates) {
        try {
          const p = await api.peekInstaller(a);
          if (stop) return;
          if (found.id && p.fingerprint !== found.id) throw new Error("the machine's key does not match what it advertised; try again");
          setAddr(a);
          setFp(p.fingerprint);
          setPem(p.public_key_pem);
          setCode(p.pairing_code);
          setError("");
          return;
        } catch (e) {
          lastErr = String((e as Error).message ?? e);
        }
      }
      if (!stop) setError(lastErr);
    })();
    return () => {
      stop = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [attempt, found.id]);

  // Step 2: poll status over the pinned connection; the Installer must show the same code.
  useEffect(() => {
    if (!fp) return;
    const client = createClient(InstallerService, transportFor({ addr, fingerprint: fp }));
    let stop = false;
    const tick = async () => {
      try {
        const r = await client.getInstallerStatus({});
        if (stop || !r.status) return;
        if (r.status.pairingCode !== code) throw new Error("pairing code mismatch between this device and the installer");
        setStatus(r.status);
        setPollError("");
        if (started && r.status.provisioning) onProgress({ name, percent: r.status.provisioning.percent, message: r.status.provisioning.message });
        if (started && r.status.provisioning?.phase === Provisioning_Phase.REBOOTING) {
          stop = true;
          const reported = r.status.addresses.map((a) => `${a.includes(":") ? `[${a}]` : a}:7443`);
          const b = restore ? r.status.backups.find((x) => x.nodeId === restore) : undefined;
          onProgress(null);
          onProvisioned({ node_id: b ? b.nodeId : fp, name: b ? b.nodeName : name, addresses: [addr, ...reported.filter((a) => a !== addr)] });
        }
        if (r.status.provisioning?.phase === Provisioning_Phase.FAILED) {
          setStarted(false);
          onProgress(null);
        }
      } catch (e) {
        if (!stop) setPollError(String((e as Error).message ?? e));
      }
    };
    tick();
    const t = setInterval(tick, 1500);
    return () => {
      stop = true;
      clearInterval(t);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fp, code, started, name, addr, restore]);
  useEffect(() => () => onProgress(null), [onProgress]);

  const disks = status?.disks.filter((d) => !d.isInstallerMedia) ?? [];
  const chosen = disks.find((d) => d.path === disk);
  const usbNeedsData = !!chosen?.removable && !dataPart && state.nodes.length === 0;
  const prov = status?.provisioning;

  async function provision() {
    if (!state.fleet || !chosen) return;
    setError("");
    const client = createClient(InstallerService, transportFor({ addr, fingerprint: fp }));
    try {
      let trustedDevices: TrustedDevice[] = [
        create(TrustedDeviceSchema, { name: device.name, publicKeyPem: device.public_key_pem, recoveryOnly: false }),
        create(TrustedDeviceSchema, { name: "Recovery Phrase", publicKeyPem: state.fleet.recovery_public_key_pem, recoveryOnly: true }),
      ];
      let nodes: FleetNode[] = [];
      // Decision 189: what the new Node stores as it is. Older Installers
      // read only trustedDevices and nodes, so those are still filled.
      let records: Rec[] = [];
      let sponsor: { addr: string; fingerprint: string } | null = null;
      for (const n of state.nodes) {
        for (const a of n.addresses) {
          try {
            const nc = createClient(NodeService, transportFor({ addr: a, fingerprint: n.node_id }));
            const all = (await nc.listRecords({})).records;
            const recs = all.filter((r) => !r.deleted);
            records = await signedForProvision(all.filter((r) => PROVISION_KINDS.has(r.kind)));
            trustedDevices = recs.filter((r) => r.kind === "trusted_device").map((r) => fromBinary(TrustedDeviceSchema, r.body));
            nodes = recs.filter((r) => r.kind === "node").map((r) => fromBinary(NodeSchema, r.body));
            sponsor = { addr: a, fingerprint: n.node_id };
            break;
          } catch {
            /* try the next address or Node */
          }
        }
        if (sponsor) break;
      }
      if (state.nodes.length > 0 && !sponsor) throw new Error("none of the Fleet's Nodes can be reached to add this one; bring one online first");
      if (!sponsor && !restore) records = await firstDevices(device, state.fleet.recovery_public_key_pem);
      if (!restore && nodes.some((n) => n.name === name.trim())) throw new Error(`the Fleet already has a Node named "${name.trim()}"`);
      if (restore) {
        await client.provision({ targetDisk: chosen.path, dataPartition: dataPart, restoreNodeId: restore, fleet: { fleetId: state.fleet.fleet_id }, nodeName: "restore" });
      } else {
        await client.provision({ fleet: { fleetId: state.fleet.fleet_id, name: state.fleet.name, createdUnixMs: BigInt(state.fleet.created_unix_ms) }, nodeName: name.trim(), targetDisk: chosen.path, dataPartition: dataPart, trustedDevices, nodes, records });
      }
      setStarted(true);
      onProgress({ name: restore ? (status?.backups.find((b) => b.nodeId === restore)?.nodeName ?? name) : name, percent: 0, message: "starting" });
      if (!restore && sponsor) {
        const body = toBinary(NodeSchema, create(NodeSchema, { nodeId: fp, name: name.trim(), publicKeyPem: pem, addresses: [addr], provisionedUnixMs: BigInt(Date.now()) }));
        try {
          await putSigned(sponsor, { kind: "node", id: fp, body });
        } catch (e) {
          // The install goes on without it; the Fleet would not know the Node.
          throw new Error(`The install is running, but the Fleet's Node at ${sponsor.addr} would not record the new Node: ${ConnectError.from(e).rawMessage}`);
        }
      }
    } catch (e) {
      setError(String((e as Error).message ?? e));
    }
  }

  const step = started ? 3 : confirmed ? 2 : 1;
  return (
    <div className="mx-auto max-w-2xl p-6">
      <div className="mb-6">
        <h1 className="text-xl font-semibold">Set up {found.machine || "this machine"}</h1>
        <p className="text-sm text-muted-foreground">{addr}</p>
      </div>
      <ol className="mb-6 flex gap-2 text-xs">
        {["Check the code", "Name and disk", "Install"].map((label, i) => (
          <li key={label} className={cn("flex items-center gap-1.5 rounded-full border px-2.5 py-1", step === i + 1 ? "border-primary bg-primary text-primary-foreground" : step > i + 1 ? "text-muted-foreground" : "text-muted-foreground opacity-60")}>
            {step > i + 1 ? <Check className="size-3" /> : <span className="font-semibold">{i + 1}</span>} {label}
          </li>
        ))}
      </ol>

      {step === 1 && (
        <section className="space-y-4">
          {code ? (
            <>
              <p className="text-sm">The machine's screen shows a six-digit code. Does it match this one?</p>
              <div className="rounded-xl border bg-card p-6 text-center font-mono text-4xl tracking-[0.3em]">{code}</div>
              <div className="flex gap-2">
                <Button disabled={!status} onClick={() => setConfirmed(true)}>
                  <Check /> Yes, it matches
                </Button>
                <Button variant="outline" onClick={onCancel}>
                  <X /> No, cancel
                </Button>
              </div>
              {!status && <p className="text-xs text-muted-foreground">Waiting for the machine to answer over the pinned connection…</p>}
            </>
          ) : error ? (
            <div className="space-y-3 rounded-xl border border-status-attention/60 bg-status-attention/10 p-4 text-sm">
              <p>Could not reach the machine at {found.addresses.join(", ")}.</p>
              <p className="text-xs text-muted-foreground">If it is reachable at a different address or port, enter it{qemuGuest ? " (this looks like the development VM from `make vm`, which the host reaches at 127.0.0.1:17443)" : " (for example a VM behind NAT)"}.</p>
              <div className="flex gap-2">
                <Input value={altAddr} onChange={(e) => setAltAddr(e.target.value)} placeholder="192.168.1.20:7443" className="max-w-xs" />
                <Button variant="outline" disabled={!altAddr.includes(":")} onClick={() => (setAddr(altAddr.trim()), setAttempt((n) => n + 1))}>
                  Retry
                </Button>
              </div>
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">Contacting the machine…</p>
          )}
        </section>
      )}

      {step === 2 && (
        <section className="space-y-5">
          {status && status.backups.length > 0 && (
            <div className="space-y-2 rounded-xl border border-status-attention/60 bg-status-attention/10 p-4 text-sm">
              <p className="font-medium">A Drive in this machine holds a backup of a Node.</p>
              <RadioGroup value={restore} onValueChange={setRestore} className="gap-1.5">
                {status.backups.map((b) => (
                  <label key={b.nodeId} className="flex items-center gap-2">
                    <RadioGroupItem value={b.nodeId} /> Restore <b>{b.nodeName}</b> of Fleet "{b.fleetName}" (saved {new Date(Number(b.savedUnixMs)).toLocaleString()}, on {b.device})
                  </label>
                ))}
                <label className="flex items-center gap-2">
                  <RadioGroupItem value="" /> Set up a new Node instead
                </label>
              </RadioGroup>
            </div>
          )}
          {!restore && (
            <div className="space-y-1.5">
              <Label htmlFor="node-name">Node name</Label>
              <div className="flex gap-2">
                <Input id="node-name" value={name} onChange={(e) => setName(e.target.value)} maxLength={40} className="max-w-xs" />
                <Button type="button" variant="outline" size="icon" title="Draw another name" onClick={() => setName(randomNodeName(letter, takenNames))}>
                  <Dices />
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">
                This will be Node {letter || "?"}; it and its Drives share the {letter || "?"} names.{found.machine ? ` The machine calls itself ${found.machine}.` : ""}
              </p>
            </div>
          )}
          <div className="space-y-2">
            <Label>Install Maître Server on</Label>
            <p className="text-xs text-destructive">Everything on the chosen disk will be erased.</p>
            {status?.image && (
              <p className="text-xs text-muted-foreground">
                It will install <code>{status.image}</code>
                {status.source && status.source.startsWith("oci:") ? " from the copy on its media" : status.source ? ` from ${status.source}` : " from the registry"}
                {status.insecureRegistry && <span className="text-status-attention"> (pulling from {status.insecureRegistry} without TLS: a developer Depot)</span>}. If that is not what you expect, stop here.
              </p>
            )}
            {disks.length === 0 && <p className="text-sm text-muted-foreground">No other disks found on this machine.</p>}
            <RadioGroup value={disk} onValueChange={(v) => (setDisk(v), setDataPart(!disks.find((d) => d.path === v)?.removable))} className="gap-1.5">
              {disks.map((d) => (
                <div key={d.path} className={cn("rounded-lg border", disk === d.path && "border-primary")}>
                  <label className="flex cursor-pointer items-center gap-3 p-3 text-sm">
                    <RadioGroupItem value={d.path} />
                    {d.removable ? <Usb className="size-4 text-muted-foreground" /> : <HardDrive className="size-4 text-muted-foreground" />}
                    <span className="min-w-0 flex-1">
                      <span className="block font-medium">{d.model || d.path}</span>
                      <span className="block text-xs text-muted-foreground">
                        {formatBytes(d.sizeBytes)} · {d.path}
                        {d.removable && " · USB, removable"}
                      </span>
                      {/* Installers before decision 262 list no partitions. */}
                      {d.partitions.length > 0 && <span className="block text-xs text-muted-foreground">Holds: {holds(d)}</span>}
                    </span>
                    {d.partitions.some((p) => p.browsable) && installer && (
                      <Button type="button" variant="ghost" size="sm" onClick={(e) => (e.preventDefault(), setLooking(looking === d.path ? "" : d.path))}>
                        <FolderSearch /> {looking === d.path ? "Close" : "Look inside"}
                      </Button>
                    )}
                  </label>
                  {looking === d.path && installer && (
                    <div className="px-3 pb-3">
                      <DiskBrowser client={installer} disk={d} />
                    </div>
                  )}
                </div>
              ))}
            </RadioGroup>
          </div>
          {chosen && (
            <label className="flex items-center justify-between gap-3 rounded-lg border p-3 text-sm">
              <span>
                Use the rest of this disk for data
                <span className="block text-xs text-muted-foreground">
                  {usbNeedsData ? "Required: a USB stick as the only disk needs this, or another Node with a Pool." : chosen.removable ? "Off by default on USB drives." : "Its remaining space becomes the Node's built-in Drive."}
                </span>
              </span>
              <Switch checked={dataPart} onCheckedChange={setDataPart} />
            </label>
          )}
          {(error || pollError) && <p className="text-sm text-destructive">{error || pollError}</p>}
          {prov?.phase === Provisioning_Phase.FAILED && <InstallFailed prov={prov} />}
          <div className="flex gap-2">
            <Button disabled={!chosen || (!restore && !name.trim()) || usbNeedsData} onClick={provision}>
              {restore ? "Restore" : "Install"}
            </Button>
            <Button variant="outline" onClick={onCancel}>
              Cancel
            </Button>
          </div>
        </section>
      )}

      {step === 3 && (
        <section className="space-y-3">
          <div className="h-2 overflow-hidden rounded bg-muted">
            <div className="h-full rounded bg-status-starting transition-[width] duration-500" style={{ width: `${prov?.percent ?? 0}%` }} />
          </div>
          <p className="text-sm">{prov?.message || "starting"}</p>
          <p className="text-xs text-muted-foreground">Do not power the machine off. It restarts on its own when done, and then shows up as a Node. Meanwhile the rest of the Fleet is yours to explore.</p>
          {(error || pollError) && <p className="text-sm text-destructive">{error || pollError}</p>}
          <div className="flex items-center gap-3">
            <HoldButton size="sm" variant="outline" onConfirm={() => restartInstaller(addr, found.machine || "The machine", true)}>
              Hold to restart the machine
            </HoldButton>
            <span className="text-xs text-muted-foreground">If it looks stuck. The install stops and the disk is left half written; set it up again after.</span>
          </div>
        </section>
      )}
    </div>
  );
}
