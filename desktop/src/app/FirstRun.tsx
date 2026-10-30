// First run (decisions 134, 141): create a Fleet, keep its Recovery Phrase,
// or get into an existing Fleet one of three ways. No sidebar yet.
import { useEffect, useRef, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { createClient } from "@connectrpc/connect";
import { create, toBinary } from "@bufbuild/protobuf";
import { motion } from "motion/react";
import { Boxes, KeyRound, Laptop, Hash, ArrowLeft, Dices, Network } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Checkbox } from "@/components/ui/checkbox";
import { cn } from "@/lib/utils";
import { RecoveryService } from "@/gen/maitre/v1/recovery_pb";
import { TrustedDeviceSchema } from "@/gen/maitre/v1/fleet_pb";
import { PutRecordRequestSchema } from "@/gen/maitre/v1/node_pb";
import { transportFor } from "@/transport";
import { api, type ConsoleState, type DeviceInfo, type Found } from "@/state";
import { adoptFleet, firstTrustingNode } from "@/fleet/join";
import { signedRecord } from "@/fleet/sign";
import { nodeAddr, typedNode, withTyped } from "@/fleet/address";

export type FirstRunScreen = "welcome" | "join" | "recover" | "code";

function Frame({ children, wide }: { children: React.ReactNode; wide?: boolean }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-background p-8 text-foreground">
      <motion.div initial={{ opacity: 0, y: 8 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.25 }} className={cn("w-full", wide ? "max-w-2xl" : "max-w-md")}>
        {children}
      </motion.div>
    </div>
  );
}

function Title({ children, sub }: { children: React.ReactNode; sub?: React.ReactNode }) {
  return (
    <div className="mb-6">
      <h1 className="text-2xl font-semibold tracking-tight">{children}</h1>
      {sub && <p className="mt-1 text-sm text-muted-foreground">{sub}</p>}
    </div>
  );
}

const FLEET_NAMES = ["Home", "The House", "Basecamp", "Our Place", "Headquarters", "The Burrow", "Homestead"];

export function Welcome({ device, onCreated, onGo }: { device: DeviceInfo; onCreated: (phrase: string) => void; onGo: (s: FirstRunScreen) => void }) {
  const [name, setName] = useState("Home");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return (
    <Frame>
      <div className="mb-8 flex items-center gap-3">
        <span className="inline-flex size-10 items-center justify-center rounded-xl bg-primary text-primary-foreground">
          <Boxes className="size-5" />
        </span>
        <div>
          <div className="text-lg font-semibold">Maître</div>
          <div className="text-xs text-muted-foreground">Run your own apps at home, from {device.name}.</div>
        </div>
      </div>
      <Title sub="A Fleet is the set of servers you manage from this app, usually everything in one home.">Create your Fleet</Title>
      <form
        className="space-y-4"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError("");
          try {
            onCreated(await api.createFleet(name.trim()));
          } catch (err) {
            setError(String((err as Error).message ?? err));
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="space-y-1.5">
          <Label htmlFor="fleet-name">Fleet name</Label>
          <div className="flex gap-2">
            <Input id="fleet-name" autoFocus value={name} onChange={(e) => setName(e.target.value)} maxLength={40} />
            <Button type="button" variant="outline" size="icon" title="Draw another name" onClick={() => setName(FLEET_NAMES[(FLEET_NAMES.indexOf(name) + 1) % FLEET_NAMES.length])}>
              <Dices />
            </Button>
          </div>
        </div>
        {error && <p className="text-sm text-destructive">{error}</p>}
        <Button type="submit" disabled={busy || !name.trim()} className="w-full">
          Create Fleet
        </Button>
      </form>
      <div className="mt-10 space-y-2">
        <p className="text-xs font-medium text-muted-foreground">Already have a Fleet?</p>
        {(
          [
            ["join", <Laptop key="j" />, "Join from another computer that manages it", "That computer approves this one."],
            ["recover", <KeyRound key="r" />, "Recover with your Recovery Phrase", "The twelve words from when the Fleet was created."],
            ["code", <Hash key="c" />, "Join with a code from a Node", "A Node rebooted into recovery shows one."],
          ] as [FirstRunScreen, React.ReactNode, string, string][]
        ).map(([s, icon, title, detail]) => (
          <button key={s} onClick={() => onGo(s)} className="flex w-full items-center gap-3 rounded-lg border p-3 text-left hover:bg-accent [&>svg]:size-4 [&>svg]:shrink-0 [&>svg]:text-muted-foreground">
            {icon}
            <span>
              <span className="block text-sm font-medium">{title}</span>
              <span className="block text-xs text-muted-foreground">{detail}</span>
            </span>
          </button>
        ))}
      </div>
    </Frame>
  );
}

export function RecoveryPhrase({ phrase, onDone }: { phrase: string; onDone: () => void }) {
  const [ack, setAck] = useState(false);
  const words = phrase.trim().split(/\s+/);
  return (
    <Frame wide>
      <Title sub="If you ever lose every computer that has Maître Desktop, these twelve words let a new one take over your Fleet. They are shown only once and stored nowhere.">Your Recovery Phrase</Title>
      <ol className="grid grid-cols-3 gap-2 rounded-xl border bg-card p-4 sm:grid-cols-4">
        {words.map((w, i) => (
          <li key={i} className="flex items-baseline gap-2 rounded-md bg-muted/60 px-3 py-2 font-mono text-sm">
            <span className="w-5 text-right text-xs text-muted-foreground">{i + 1}</span>
            {w}
          </li>
        ))}
      </ol>
      <label className="mt-6 flex items-center gap-2 text-sm">
        <Checkbox checked={ack} onCheckedChange={(v) => setAck(v === true)} /> I have written these words down and put them somewhere safe
      </label>
      <Button className="mt-4" disabled={!ack} onClick={onDone}>
        Continue
      </Button>
    </Frame>
  );
}

function Back({ onClick }: { onClick: () => void }) {
  return (
    <button onClick={onClick} className="mb-4 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
      <ArrowLeft className="size-4" /> Back
    </button>
  );
}

/** A Node typed in by address, for networks where discovery finds nothing (decision 219). */
function ByAddress({ typed, onAdd }: { typed: Found[]; onAdd: (f: Found) => void }) {
  const [open, setOpen] = useState(false);
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<{ addr: string; detail: string } | null>(null);
  const addr = nodeAddr(text);
  async function connect() {
    setBusy(true);
    setError(null);
    try {
      const p = await api.peekKey(addr);
      onAdd(typedNode(addr, p.fingerprint));
      setText("");
    } catch (e) {
      setError({ addr, detail: String((e as Error).message ?? e) });
    } finally {
      setBusy(false);
    }
  }
  if (!open && typed.length === 0)
    return (
      <button onClick={() => setOpen(true)} className="mt-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
        <Network className="size-4" /> Node not found? Connect by IP address
      </button>
    );
  return (
    <div className="mt-4 space-y-2 rounded-lg border p-3">
      <Label htmlFor="node-addr">Node's IP address</Label>
      <form
        className="flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (addr) connect();
        }}
      >
        <Input id="node-addr" autoFocus value={text} onChange={(e) => setText(e.target.value)} placeholder="192.168.1.20" className="font-mono" />
        <Button type="submit" variant="outline" disabled={busy || !addr}>
          {busy ? "Connecting…" : "Connect"}
        </Button>
      </form>
      <p className="text-xs text-muted-foreground">The Node's screen shows it under Network. Port {addr ? addr.replace(/^.*:/, "") : "7443"} unless you add another, as in 192.168.1.20:8443.</p>
      {typed.map((t) => (
        <p key={t.addresses[0]} className="text-xs">
          <span className="mr-1.5 inline-block size-2 rounded-full bg-status-online" />A Node answered at <span className="font-mono">{t.addresses[0]}</span>
        </p>
      ))}
      {error && (
        <p className="text-xs text-destructive" title={error.detail}>
          Nothing answered at <span className="font-mono">{error.addr}</span>. Check the address, and that this computer is on a network that reaches it.
        </p>
      )}
    </div>
  );
}

function useTyped(): [Found[], (f: Found) => void] {
  const [typed, setTyped] = useState<Found[]>([]);
  return [typed, (f) => setTyped((t) => [...t.filter((x) => x.addresses[0] !== f.addresses[0]), f])];
}

export function JoinScreen({ device, onJoined, onCancel }: { device: DeviceInfo; onJoined: (s: ConsoleState) => void; onCancel: () => void }) {
  const [code, setCode] = useState("");
  const [error, setError] = useState("");
  const [typed, addTyped] = useTyped();
  const typedRef = useRef(typed);
  typedRef.current = typed;
  useEffect(() => {
    let fullname = "";
    let stop = false;
    invoke<string>("device_code", {}).then(setCode).catch(() => {});
    invoke<string>("announce_join")
      .then((n) => (fullname = n))
      .catch((e) => setError(String(e)));
    const tick = async () => {
      try {
        const found = withTyped(await api.discover(), typedRef.current);
        const t = await firstTrustingNode(found);
        if (t && !stop) {
          stop = true;
          onJoined(await adoptFleet(t, found));
        }
      } catch (e) {
        if (!stop) setError(String(e));
      }
    };
    const timer = setInterval(tick, 3000);
    return () => {
      stop = true;
      clearInterval(timer);
      if (fullname) invoke("withdraw_join", { fullname }).catch(() => {});
    };
  }, [onJoined]);
  return (
    <Frame>
      <Back onClick={onCancel} />
      <Title sub={`On a computer that already manages the Fleet, open Maître Desktop: this computer appears under Devices as ${device.name}. Approve it there if it shows this code.`}>Join a Fleet</Title>
      <div className="rounded-xl border bg-card p-6 text-center">
        <div className="font-mono text-4xl tracking-[0.3em]">{code || "…"}</div>
        <div className="mt-3 flex items-center justify-center gap-2 text-sm text-muted-foreground">
          <span className="size-2 animate-pulse rounded-full bg-status-starting" /> Waiting for approval
        </div>
      </div>
      {error && <p className="mt-3 text-sm text-destructive">{error}</p>}
      <ByAddress typed={typed} onAdd={addTyped} />
    </Frame>
  );
}

export function RecoverScreen({ device, onJoined, onCancel }: { device: DeviceInfo; onJoined: (s: ConsoleState) => void; onCancel: () => void }) {
  const [phrase, setPhrase] = useState("");
  const [found, setNodes] = useState<Found[]>([]);
  const [typed, addTyped] = useTyped();
  const nodes = withTyped(found, typed);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    const tick = () => api.discover().then((f) => setNodes(f.filter((x) => x.kind === "node"))).catch(() => {});
    tick();
    const t = setInterval(tick, 2000);
    return () => clearInterval(t);
  }, []);
  const words = phrase.trim().toLowerCase().split(/\s+/).filter(Boolean);
  async function recover() {
    setBusy(true);
    setError("");
    try {
      const found = withTyped(await api.discover(), typed);
      let last = "";
      for (const n of nodes)
        for (const addr of n.addresses) {
          const body = toBinary(TrustedDeviceSchema, create(TrustedDeviceSchema, { deviceId: device.fingerprint, name: device.name, publicKeyPem: device.public_key_pem, addedUnixMs: BigInt(Date.now()) }));
          // Decision 189: signed by the phrase's key, which is what the Node trusts here.
          const record = await signedRecord({ kind: "trusted_device", id: device.fingerprint, body }, { phrase: words.join(" ") });
          const req = toBinary(PutRecordRequestSchema, create(PutRecordRequestSchema, { record }));
          try {
            await invoke<string>("recover_with_phrase", { phrase: words.join(" "), addr, fingerprint: n.id, body: Array.from(req) });
            onJoined(await adoptFleet({ addr, fingerprint: n.id }, found));
            return;
          } catch (e) {
            last = String(e);
          }
        }
      throw new Error(last || "no Node on this network answered");
    } catch (e) {
      setError(String((e as Error).message ?? e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Frame>
      <Back onClick={onCancel} />
      <Title sub="The twelve words you wrote down when the Fleet was created. They let this computer add itself; nothing else.">Recover with your phrase</Title>
      <textarea value={phrase} onChange={(e) => setPhrase(e.target.value)} rows={3} spellCheck={false} className="w-full rounded-md border border-input bg-transparent px-3 py-2 font-mono text-sm shadow-xs outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 dark:bg-input/30" placeholder="twelve words, in order" />
      <p className="mt-1 text-xs text-muted-foreground">
        {words.length} of 12 words · {nodes.length === 0 ? "looking for a Node on this network…" : `Nodes found: ${nodes.map((n) => n.name).join(", ")}`}
      </p>
      {error && <p className="mt-2 text-sm text-destructive">{error}</p>}
      <Button className="mt-4" disabled={busy || nodes.length === 0 || words.length !== 12} onClick={recover}>
        {busy ? "Recovering…" : "Recover"}
      </Button>
      <div>
        <ByAddress typed={typed} onAdd={addTyped} />
      </div>
    </Frame>
  );
}

export function CodeScreen({ device, onJoined, onCancel, promote = false }: { device: DeviceInfo; onJoined: (s: ConsoleState) => void; onCancel: () => void; promote?: boolean }) {
  const [code, setCode] = useState("");
  const [found, setNodes] = useState<Found[]>([]);
  const [typed, addTyped] = useTyped();
  const nodes = withTyped(found, typed);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    const tick = () => api.discover().then((f) => setNodes(f.filter((x) => x.kind === "node"))).catch(() => {});
    tick();
    const t = setInterval(tick, 2000);
    return () => clearInterval(t);
  }, []);
  const open = nodes.filter((n) => n.recovery);
  const digits = code.replace(/\s+/g, "");
  async function authorize(n: Found) {
    setBusy(true);
    setError("");
    try {
      const found = withTyped(await api.discover(), typed);
      let last = "";
      for (const a of n.addresses) {
        const host = a.replace(/:\d+$/, "");
        try {
          const rc = createClient(RecoveryService, transportFor({ addr: `${host}:7444`, fingerprint: n.id }));
          await rc.authorize({ code: digits, name: device.name, publicKeyPem: device.public_key_pem });
          onJoined(await adoptFleet({ addr: a, fingerprint: n.id }, found));
          return;
        } catch (e) {
          last = String(e);
        }
      }
      throw new Error(last);
    } catch (e) {
      setError(String((e as Error).message ?? e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Frame>
      <Back onClick={onCancel} />
      <Title sub={promote ? "Use a different Node than the one that trusted this device: a code from each of two Nodes is what makes the trust Fleet-wide." : "In a Fleet of two or more Nodes, one code makes this device trusted by that Node alone; a second code from another Node within a day makes it trusted everywhere."}>{promote ? "Be trusted by the whole Fleet" : "Join with a code from a Node"}</Title>
      <p className="mb-4 text-sm text-muted-foreground">At the Node's keyboard, press r on the Status page and choose "reboot into recovery". It shows an eight-digit code and restarts; enter the code here within 15 minutes.</p>
      <Input value={code} onChange={(e) => setCode(e.target.value.replace(/[^\d\s]/g, ""))} placeholder="1234 5678" className="h-12 font-mono text-2xl tracking-[0.2em]" />
      <div className="mt-4 space-y-2">
        {open.length === 0 ? (
          <p className="text-sm text-muted-foreground">No Node is accepting a code right now{nodes.length > 0 ? ` (found: ${nodes.map((n) => n.name).join(", ")})` : ""}.</p>
        ) : (
          open.map((n) => (
            <div key={n.id} className="flex items-center justify-between rounded-lg border p-3 text-sm">
              <span>
                {found.some((f) => f.id === n.id) ? (
                  <>
                    <b>{n.name}</b> is accepting a code
                  </>
                ) : (
                  <>
                    The Node at <b className="font-mono">{n.name}</b>
                  </>
                )}
              </span>
              <Button size="sm" disabled={busy || digits.length !== 8} onClick={() => authorize(n)}>
                Join through {n.name}
              </Button>
            </div>
          ))
        )}
      </div>
      {error && <p className="mt-3 text-sm text-destructive">{error}</p>}
      <ByAddress typed={typed} onAdd={addTyped} />
    </Frame>
  );
}
