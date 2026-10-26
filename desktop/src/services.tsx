import { useEffect, useMemo, useRef, useState } from "react";
import { createClient } from "@connectrpc/connect";
import { invoke } from "@tauri-apps/api/core";
import { ServiceService, ServiceStatus_State, ControlServiceRequest_Action, ServiceSpecSchema, PublicSettingsSchema, type ServiceStatus } from "./gen/maitre/v1/service_pb";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { transportFor, type Target } from "./transport";
import { NodeService } from "./gen/maitre/v1/node_pb";
// Decision 78: the Catalog lives with the Console. This index is generated
// from the catalog/ submodule by `make gen`; the Node knows nothing of it.
import bundledCatalog from "./catalog/index.json";

// Mirrors catalog/FORMAT.md (the agent hands us index.json verbatim).
interface CatalogEntry {
  id: string;
  name: string;
  summary: string;
  description: string;
  icon: string;
  category: string;
  image: string;
  version: string;
  exposure: string;
  own_login: boolean;
  infrastructure: boolean;
  host_ports?: string[];
  port_forward?: boolean;
  web?: { port: number; path: string };
  volumes: { name: string; path: string; size?: string }[];
  settings?: { key: string; label: string; type: string; default?: string }[];
  health?: { http?: string };
  quadlet: string;
}
interface CatalogIndex {
  format: number;
  entries: CatalogEntry[];
}

export function ServicesPanel({
  target,
  tiers = [],
  remoteReady = false,
  publicIp = "",
  portMapping = "",
}: {
  target: Target;
  tiers?: { tierId: string; name: string }[];
  remoteReady?: boolean;
  publicIp?: string;
  portMapping?: string;
}) {
  const client = useMemo(() => createClient(ServiceService, transportFor(target)), [target.addr, target.fingerprint]);
  const [services, setServices] = useState<ServiceStatus[]>([]);
  const catalog = bundledCatalog as CatalogIndex;
  const catalogById = useMemo(() => new Map(catalog.entries.map((e) => [e.id, e])), [catalog]);
  const [error, setError] = useState("");
  const [installing, setInstalling] = useState<CatalogEntry | null>(null);
  const [open, setOpen] = useState<{ name: string; url: string } | null>(null);
  const [logsFor, setLogsFor] = useState<{ name: string; lines: string[] } | null>(null);
  const [removing, setRemoving] = useState<ServiceStatus | null>(null);
  const [exposing, setExposing] = useState<ServiceStatus | null>(null);
  const [forwarding, setForwarding] = useState<ServiceStatus | null>(null);
  // Milestone 3: a Service record whose version moved while this Console
  // did not write it was changed from another device (or Node); say so.
  const [notice, setNotice] = useState("");
  const seen = useRef<Map<string, bigint>>(new Map());
  const lastWrite = useRef(0);
  const nodeClient = useMemo(() => createClient(NodeService, transportFor(target)), [target.addr, target.fingerprint]);

  const refresh = async () => {
    try {
      const r = await client.listServices({});
      setServices(r.services);
      setError("");
      const recs = (await nodeClient.listRecords({ kind: "service" })).records;
      const changed: string[] = [];
      for (const rec of recs) {
        const prev = seen.current.get(rec.id);
        if (prev !== undefined && rec.version > prev && Date.now() - lastWrite.current > 5000) changed.push(rec.deleted ? `${rec.id} (removed)` : rec.id);
        seen.current.set(rec.id, rec.version);
      }
      if (changed.length > 0) {
        setNotice(`Changed from another device: ${changed.join(", ")}`);
        setTimeout(() => setNotice(""), 15000);
      }
    } catch (e) {
      setError(String(e));
    }
  };
  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 3000);
    return () => clearInterval(t);
  }, [client]);

  const installed = new Set(services.map((s) => s.spec?.catalogId));

  async function control(name: string, action: ControlServiceRequest_Action) {
    try {
      lastWrite.current = Date.now();
      await client.controlService({ name, action });
      await refresh();
    } catch (e) {
      setError(String(e));
    }
  }

  async function openService(name: string) {
    try {
      const url = await invoke<string>("service_url", { addr: target.addr, fingerprint: target.fingerprint, service: name });
      setOpen({ name, url });
    } catch (e) {
      setError(String(e));
    }
  }

  if (open) {
    return (
      <section style={{ display: "flex", flexDirection: "column", height: "70vh" }}>
        <div style={{ display: "flex", gap: 8, alignItems: "center", marginBottom: 8 }}>
          <button onClick={() => setOpen(null)}>← Services</button>
          <b>{open.name}</b>
          <span style={{ color: "#555", fontSize: 13 }}>opened privately through this Node</span>
        </div>
        <iframe title={open.name} src={open.url} style={{ flex: 1, border: "1px solid #ddd", borderRadius: 6, background: "#fff" }} />
      </section>
    );
  }

  return (
    <>
      {error && <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>{error}</pre>}
      {notice && <p style={{ color: "#a60", fontSize: 13 }}>{notice}</p>}

      <section>
        <h3>Services on this Node</h3>
        {services.length === 0 && <p style={{ color: "#555" }}>Nothing installed yet. Pick something from the Catalog below.</p>}
        <ul style={{ listStyle: "none", padding: 0 }}>
          {services.map((s) => (
            <li key={s.spec?.name} style={row}>
              <div>
                <b>{s.spec?.name}</b> <StateBadge s={s} />
                <div style={{ color: "#555", fontSize: 13 }}>
                  {s.url ? `Open here, or from a browser on your network at ${s.url}` : "no web page"} {s.restarts > 0 && `· restarted ${s.restarts}×`}{" "}
                  {s.message}
                  {s.spec?.autoUpdate && " · updates itself"}
                </div>
                {s.spec && s.spec.exposure !== "private" && (
                  <div style={{ fontSize: 13 }}>
                    {s.spec.exposure === "public" || s.spec.exposure === "direct" ? <span style={{ color: "#b00020" }}>Public</span> : <span style={{ color: "#2a7" }}>Remote</span>}
                    {s.spec.exposure === "direct" && <span style={{ color: "#555" }}> at {s.spec.publicHost}</span>}:{" "}
                    {s.remoteUrl ? (
                      <a href={s.remoteUrl} target="_blank" rel="noreferrer">
                        {s.remoteUrl}
                      </a>
                    ) : (
                      <span style={{ color: "#555" }}>{s.remoteState || "starting"}</span>
                    )}
                  </div>
                )}
                {s.forwardState && (
                  <div style={{ fontSize: 13, color: "#555" }}>
                    {s.spec?.forwardPorts && <span style={{ color: "#b00020" }}>Internet: </span>}
                    {s.forwardState}
                  </div>
                )}
              </div>
              <div style={{ display: "flex", gap: 4 }}>
                {s.url && s.state === ServiceStatus_State.RUNNING && <button onClick={() => openService(s.spec!.name)}>Open</button>}
                {s.state === ServiceStatus_State.RUNNING || s.state === ServiceStatus_State.STARTING ? (
                  <button onClick={() => control(s.spec!.name, ControlServiceRequest_Action.STOP)}>Stop</button>
                ) : (
                  <button onClick={() => control(s.spec!.name, ControlServiceRequest_Action.START)}>Start</button>
                )}
                <button
                  onClick={async () => {
                    const r = await client.getServiceLogs({ name: s.spec!.name, lines: 200 });
                    setLogsFor({ name: s.spec!.name, lines: r.lines });
                  }}
                >
                  Logs
                </button>
                <button onClick={() => control(s.spec!.name, ControlServiceRequest_Action.UPDATE)} title="Pull the newest image and restart on it; the previous one is kept">
                  Update
                </button>
                {s.spec?.previousImage && (
                  <button
                    onClick={() => {
                      if (confirm(`Go back to the previous image of ${s.spec?.name}?`)) control(s.spec!.name, ControlServiceRequest_Action.ROLLBACK);
                    }}
                  >
                    Roll back
                  </button>
                )}
                {s.spec?.webPort !== 0 && <button onClick={() => setExposing(s)}>Exposure…</button>}
                {(s.spec?.hostPorts.length ?? 0) > 0 && !s.spec?.infrastructure && catalogById.get(s.spec?.catalogId ?? "")?.port_forward && (
                  <button onClick={() => setForwarding(s)}>{s.spec?.forwardPorts ? "Internet: on…" : "Internet…"}</button>
                )}
                <button onClick={() => setRemoving(s)}>Remove</button>
              </div>
            </li>
          ))}
        </ul>
        {logsFor && (
          <div>
            <div style={{ display: "flex", justifyContent: "space-between" }}>
              <b>Logs: {logsFor.name}</b> <button onClick={() => setLogsFor(null)}>Close</button>
            </div>
            <pre style={{ maxHeight: 240, overflow: "auto", background: "#111", color: "#ddd", padding: 8, fontSize: 12 }}>{logsFor.lines.join("\n") || "(empty)"}</pre>
          </div>
        )}
      </section>

      <section>
        <h3>Catalog</h3>
        <ul style={{ listStyle: "none", padding: 0 }}>
          {catalog.entries.map((e) => (
            <li key={e.id} style={row}>
              <div style={{ display: "flex", gap: 12, alignItems: "center" }}>
                {e.icon && <img src={e.icon} alt="" width={32} height={32} style={{ objectFit: "contain" }} />}
                <div>
                  <b>{e.name}</b> <span style={{ color: "#555", fontSize: 13 }}>{e.version}</span>
                  {e.infrastructure && <span style={{ marginLeft: 6, fontSize: 12, color: "#a60" }}>network infrastructure</span>}
                  <div style={{ color: "#555", fontSize: 13 }}>{e.summary}</div>
                </div>
              </div>
              <button disabled={installed.has(e.id)} onClick={() => setInstalling(e)}>
                {installed.has(e.id) ? "Installed" : "Install"}
              </button>
            </li>
          ))}
        </ul>
      </section>

      {exposing && (
        <ExposureDialog
          s={exposing}
          remoteReady={remoteReady}
          publicIp={publicIp}
          portMapping={portMapping}
          target={target}
          nodeAddr={target.addr.replace(/:\d+$/, "")}
          onCancel={() => setExposing(null)}
          onSet={async (exposure, publicHost) => {
            setExposing(null);
            try {
              lastWrite.current = Date.now();
              await client.setExposure({ name: exposing.spec!.name, exposure, publicHost });
              await refresh();
            } catch (e) {
              setError(String(e));
            }
          }}
        />
      )}
      {forwarding && (
        <PortForwardDialog
          s={forwarding}
          portMapping={portMapping}
          nodeAddr={target.addr.replace(/:\d+$/, "")}
          onCancel={() => setForwarding(null)}
          onSet={async (enabled) => {
            setForwarding(null);
            try {
              lastWrite.current = Date.now();
              await client.setPortForwarding({ name: forwarding.spec!.name, enabled });
              await refresh();
            } catch (e) {
              setError(String(e));
            }
          }}
        />
      )}
      {removing && (
        <RemoveDialog
          s={removing}
          onCancel={() => setRemoving(null)}
          onRemove={async (deleteData) => {
            setRemoving(null);
            try {
              lastWrite.current = Date.now();
              await client.controlService({ name: removing.spec!.name, action: ControlServiceRequest_Action.REMOVE, deleteData });
              await refresh();
            } catch (e) {
              setError(String(e));
            }
          }}
        />
      )}
      {installing && (
        <InstallDialog
          entry={installing}
          tiers={tiers}
          remoteReady={remoteReady}
          onCancel={() => setInstalling(null)}
          onInstall={async (name, settings, volumeTiers, exposure, autoUpdate) => {
            try {
              lastWrite.current = Date.now();
              const spec = specFor(installing, name, settings);
              spec.volumeTiers = volumeTiers;
              spec.exposure = exposure;
              spec.autoUpdate = autoUpdate;
              await client.installService({ spec });
              setInstalling(null);
              await refresh();
            } catch (e) {
              setError(String(e));
            }
          }}
        />
      )}
    </>
  );
}

/** Mirrors catalog.SpecFor on the Go side: everything the Node needs to run the Service. */
function specFor(e: CatalogEntry, name: string, settings: Record<string, string>) {
  return create(ServiceSpecSchema, {
    name: name || e.id,
    catalogId: e.id,
    image: e.image,
    settings: { ...Object.fromEntries((e.settings ?? []).map((s) => [s.key, s.default ?? ""])), ...settings },
    exposure: e.exposure,
    quadlet: e.quadlet,
    infrastructure: e.infrastructure,
    hostPorts: e.host_ports ?? [],
    webPort: e.web?.port ?? 0,
    webPath: e.web?.path ?? "",
    healthHttp: e.health?.http ?? "",
    volumes: e.volumes.map((v) => ({ name: v.name, path: v.path, size: v.size ?? "" })),
  });
}

function InstallDialog({
  entry,
  tiers,
  remoteReady,
  onCancel,
  onInstall,
}: {
  entry: CatalogEntry;
  tiers: { tierId: string; name: string }[];
  remoteReady: boolean;
  onCancel: () => void;
  onInstall: (name: string, settings: Record<string, string>, volumeTiers: Record<string, string>, exposure: string, autoUpdate: boolean) => void;
}) {
  const [name, setName] = useState(entry.id);
  // Decision 110: at home only by default, so a Service takes no Tailscale
  // hostname until the user asks for it.
  const [exposure, setExposure] = useState("private");
  const [autoUpdate, setAutoUpdate] = useState(true);
  const [volumeTiers, setVolumeTiers] = useState<Record<string, string>>(Object.fromEntries(entry.volumes.map((v) => [v.name, "default"])));
  const [settings, setSettings] = useState<Record<string, string>>(
    Object.fromEntries((entry.settings ?? []).map((s) => [s.key, s.default ?? ""])),
  );
  return (
    <div style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,.35)", display: "flex", alignItems: "center", justifyContent: "center" }}>
      <div style={{ background: "#fff", padding: 24, borderRadius: 8, width: 480, maxHeight: "80vh", overflow: "auto" }}>
        <h3 style={{ marginTop: 0 }}>Install {entry.name}</h3>
        <p style={{ color: "#555", whiteSpace: "pre-wrap" }}>{entry.description}</p>
        {entry.infrastructure && (
          <p style={{ color: "#a60", fontSize: 13 }}>
            Other devices on your network will depend on this. The Node keeps a fixed address and starts it before anything else.
            It needs ports {entry.host_ports?.join(", ")} on the Node.
          </p>
        )}
        <label>
          Name{" "}
          <input value={name} onChange={(e) => setName(e.target.value.toLowerCase())} style={{ width: 220 }} />
        </label>
        <div style={{ color: "#555", fontSize: 13 }}>Reachable as {name || "…"}.local from devices on your network.</div>
        {(entry.settings ?? []).map((s) => (
          <label key={s.key} style={{ display: "block", marginTop: 8 }}>
            {s.label}{" "}
            {s.type === "bool" ? (
              <input type="checkbox" checked={settings[s.key] === "true"} onChange={(e) => setSettings({ ...settings, [s.key]: String(e.target.checked) })} />
            ) : (
              <input
                type={s.type === "password" ? "password" : s.type === "number" ? "number" : "text"}
                value={settings[s.key]}
                onChange={(e) => setSettings({ ...settings, [s.key]: e.target.value })}
                placeholder={s.type === "timezone" ? "auto = the Node's time zone" : ""}
                style={{ width: 220 }}
              />
            )}
          </label>
        ))}
        {tiers.length > 1 &&
          entry.volumes.map((v) => (
            <label key={v.name} style={{ display: "block", marginTop: 8 }}>
              Store {v.name}
              {v.size ? ` (${v.size})` : ""} on{" "}
              <select value={volumeTiers[v.name]} onChange={(e) => setVolumeTiers({ ...volumeTiers, [v.name]: e.target.value })}>
                {tiers.map((t) => (
                  <option key={t.tierId} value={t.tierId}>
                    {t.name}
                  </option>
                ))}
              </select>
            </label>
          ))}
        {entry.web && !entry.infrastructure && (
          <label style={{ display: "block", marginTop: 8 }}>
            Reachable{" "}
            <select value={exposure} onChange={(e) => setExposure(e.target.value)}>
              <option value="private">at home only</option>
              <option value="remote" disabled={!remoteReady}>
                from anywhere, for you (Tailscale)
              </option>
            </select>
            {!remoteReady && <span style={{ color: "#555", fontSize: 13 }}> · connect the Fleet to Tailscale to reach it away from home</span>}
          </label>
        )}
        <label style={{ display: "block", marginTop: 8 }}>
          <input type="checkbox" checked={autoUpdate} onChange={(e) => setAutoUpdate(e.target.checked)} /> Keep {entry.name} up to date by itself (daily; the previous version stays one click away)
        </label>
        {entry.own_login && <p style={{ color: "#555", fontSize: 13 }}>{entry.name} has its own accounts; you will set one up the first time you open it.</p>}
        <div style={{ marginTop: 16, display: "flex", gap: 8 }}>
          <button disabled={!/^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$/.test(name)} onClick={() => onInstall(name, settings, volumeTiers, exposure, autoUpdate)}>
            Install
          </button>
          <button onClick={onCancel}>Cancel</button>
        </div>
      </div>
    </div>
  );
}

/** Decisions 111 and 112: Public needs the plain-language warning acknowledged; Public at your own address needs a host name and DNS. */
function ExposureDialog({
  s,
  remoteReady,
  publicIp,
  portMapping,
  target,
  nodeAddr,
  onCancel,
  onSet,
}: {
  s: ServiceStatus;
  remoteReady: boolean;
  publicIp: string;
  portMapping: string;
  target: Target;
  nodeAddr: string;
  onCancel: () => void;
  onSet: (exposure: string, publicHost: string) => void;
}) {
  const [choice, setChoice] = useState(s.spec?.exposure || "private");
  const [host, setHost] = useState(s.spec?.publicHost || "");
  const [ack, setAck] = useState(false);
  // Decision 114: one optional email for the whole Fleet, given to Let's
  // Encrypt for expiry and policy notices. Lives in a Fleet-wide record.
  const [email, setEmail] = useState("");
  const [emailSaved, setEmailSaved] = useState("");
  const [emailError, setEmailError] = useState("");
  useEffect(() => {
    (async () => {
      try {
        const recs = (await createClient(NodeService, transportFor(target)).listRecords({ kind: "public_settings" })).records.filter((r) => !r.deleted);
        if (recs[0]) {
          const e = fromBinary(PublicSettingsSchema, recs[0].body).acmeEmail;
          setEmail(e);
          setEmailSaved(e);
        }
      } catch {
        /* no settings yet */
      }
    })();
  }, [target.addr, target.fingerprint]);
  const emailOk = email.trim() === "" || /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim());
  async function saveEmail() {
    try {
      await createClient(NodeService, transportFor(target)).putRecord({
        record: { kind: "public_settings", id: "fleet", body: toBinary(PublicSettingsSchema, create(PublicSettingsSchema, { acmeEmail: email.trim() })) },
      });
      setEmailSaved(email.trim());
      setEmailError("");
    } catch (e) {
      setEmailError(String(e));
    }
  }
  const name = s.spec?.name ?? "";
  const canPublic = !s.spec?.infrastructure;
  const isPublic = choice === "public" || choice === "direct";
  const h = host.trim().toLowerCase();
  const hostOk = /^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/.test(h) && !/\.(local|localhost|lan|internal|home\.arpa)$/.test(h);
  return (
    <div style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,.35)", display: "flex", alignItems: "center", justifyContent: "center" }}>
      <div style={{ background: "#fff", padding: 24, borderRadius: 8, width: 560, maxHeight: "85vh", overflow: "auto" }}>
        <h3 style={{ marginTop: 0 }}>Where can {name} be reached?</h3>
        <label style={{ display: "block" }}>
          <input type="radio" checked={choice === "private"} onChange={() => setChoice("private")} /> <b>At home only.</b> Devices on your network, and this app.
        </label>
        <label style={{ display: "block", marginTop: 6 }}>
          <input type="radio" checked={choice === "remote"} onChange={() => setChoice("remote")} disabled={!remoteReady} /> <b>From anywhere, for you.</b> Your devices on Tailscale, wherever they are. Nobody else.
          {!remoteReady && <span style={{ color: "#555", fontSize: 13 }}> (connect the Fleet to Tailscale first)</span>}
        </label>
        <label style={{ display: "block", marginTop: 6 }}>
          <input type="radio" checked={choice === "public"} onChange={() => setChoice("public")} disabled={!remoteReady || !canPublic} /> <b>Public, at a Tailscale address.</b> Anyone on the internet with the link; nothing to set up.
          {!canPublic && <span style={{ color: "#555", fontSize: 13 }}> (network infrastructure stays at home)</span>}
          {canPublic && !remoteReady && <span style={{ color: "#555", fontSize: 13 }}> (connect the Fleet to Tailscale first)</span>}
        </label>
        <label style={{ display: "block", marginTop: 6 }}>
          <input type="radio" checked={choice === "direct"} onChange={() => setChoice("direct")} disabled={!canPublic} /> <b>Public, at your own address.</b> A name you own, such as photos.example.com, with a certificate the Node gets by itself. No Tailscale needed.
        </label>
        {choice === "direct" && (
          <div style={{ border: "1px solid #ddd", borderRadius: 6, padding: 8, marginTop: 8, fontSize: 13 }}>
            <label>
              Host name <input value={host} onChange={(e) => setHost(e.target.value.toLowerCase())} placeholder="photos.example.com" style={{ width: 260 }} />
            </label>
            <ol style={{ margin: "8px 0 0 0", paddingLeft: 20 }}>
              <li>
                At your domain's DNS, make an <b>A record</b> for <code>{hostOk ? h : "the host name"}</code> pointing at <code>{publicIp || "this Node's public address (the Node is still looking it up)"}</code>.
              </li>
              <li>
                The Node asks your router to send ports <b>80</b> and <b>443</b> to it (UPnP){portMapping.startsWith("router:") ? <> — found <b>{portMapping.slice(8)}</b></> : portMapping ? <> — {portMapping}</> : ""}.
                If the router will not, forward those two ports to this Node at <code>{nodeAddr}</code> yourself.
                {s.forwardState && (
                  <div style={{ color: "#555" }}>Now: {s.forwardState}</div>
                )}
              </li>
              <li>The Node then gets a certificate from Let's Encrypt on its own and keeps it renewed. The Service's line shows each step as it is reached.</li>
            </ol>
            <p style={{ color: "#555", margin: "8px 0 0 0" }}>If your home address changes, the record has to follow it; the Node says when the two no longer match.</p>
            <div style={{ marginTop: 8 }}>
              <label>
                Email for certificate notices (optional, shared by all your addresses){" "}
                <input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@example.com" style={{ width: 220 }} />
              </label>{" "}
              <button disabled={!emailOk || email.trim() === emailSaved} onClick={saveEmail}>
                Save
              </button>
              {!emailOk && <span style={{ color: "#b00020", marginLeft: 6 }}>That is not an email address.</span>}
              {emailError && <span style={{ color: "#b00020", marginLeft: 6 }}>{emailError}</span>}
              <div style={{ color: "#555" }}>Let's Encrypt writes there when a certificate is about to expire without renewing, and about policy changes. Nothing else.</div>
            </div>
          </div>
        )}
        {isPublic && (
          <div style={{ border: "1px solid #b00020", borderRadius: 6, padding: 8, marginTop: 8, fontSize: 13 }}>
            <p style={{ marginTop: 0 }}>
              <b>Public means public.</b> Anyone who finds the address can open {name}'s page. If it has no login of its own, or a weak one, strangers can use it, read what is in it, or change it. Bugs in {name} become reachable by
              everyone, not only by you.
            </p>
            <label>
              <input type="checkbox" checked={ack} onChange={(e) => setAck(e.target.checked)} /> I understand; {name} has a login I trust.
            </label>
          </div>
        )}
        <div style={{ marginTop: 16, display: "flex", gap: 8 }}>
          <button disabled={(isPublic && !ack) || (choice === "direct" && !hostOk)} onClick={() => onSet(choice, choice === "direct" ? h : "")}>
            Apply
          </button>
          <button onClick={onCancel}>Cancel</button>
        </div>
      </div>
    </div>
  );
}

/** Decision 115: have the router forward a Service's own ports (a game server) from the internet. */
function PortForwardDialog({
  s,
  portMapping,
  nodeAddr,
  onCancel,
  onSet,
}: {
  s: ServiceStatus;
  portMapping: string;
  nodeAddr: string;
  onCancel: () => void;
  onSet: (enabled: boolean) => void;
}) {
  const [on, setOn] = useState(!!s.spec?.forwardPorts);
  const [ack, setAck] = useState(!!s.spec?.forwardPorts);
  const name = s.spec?.name ?? "";
  const ports = (s.spec?.hostPorts ?? []).join(", ");
  return (
    <div style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,.35)", display: "flex", alignItems: "center", justifyContent: "center" }}>
      <div style={{ background: "#fff", padding: 24, borderRadius: 8, width: 520 }}>
        <h3 style={{ marginTop: 0 }}>Can people on the internet reach {name}?</h3>
        <p style={{ fontSize: 13 }}>
          {name} listens on <code>{ports}</code>. On your network it is reachable at <code>{nodeAddr}</code> already. Turning this on asks your router to send those ports from the internet to this Node (UPnP)
          {portMapping.startsWith("router:") ? <> — found <b>{portMapping.slice(8)}</b></> : portMapping ? <> — {portMapping}</> : ""}.
        </p>
        <label style={{ display: "block" }}>
          <input type="checkbox" checked={on} onChange={(e) => setOn(e.target.checked)} /> <b>Reachable from the internet</b> at your public address.
        </label>
        {on && (
          <div style={{ border: "1px solid #b00020", borderRadius: 6, padding: 8, marginTop: 8, fontSize: 13 }}>
            <p style={{ marginTop: 0 }}>
              <b>Open to everyone.</b> Anyone who finds your address can connect to {name}. Use its own allow-list or password if it has one, keep it updated, and turn this off when you stop playing.
            </p>
            <label>
              <input type="checkbox" checked={ack} onChange={(e) => setAck(e.target.checked)} /> I understand.
            </label>
          </div>
        )}
        {s.forwardState && <p style={{ color: "#555", fontSize: 13 }}>Now: {s.forwardState}</p>}
        <div style={{ marginTop: 16, display: "flex", gap: 8 }}>
          <button disabled={on && !ack} onClick={() => onSet(on)}>
            Apply
          </button>
          <button onClick={onCancel}>Cancel</button>
        </div>
      </div>
    </div>
  );
}

/** The guard (Milestone 4): deleting data needs the Service's name typed back. */
function RemoveDialog({ s, onCancel, onRemove }: { s: ServiceStatus; onCancel: () => void; onRemove: (deleteData: boolean) => void }) {
  const [deleteData, setDeleteData] = useState(false);
  const [typed, setTyped] = useState("");
  const name = s.spec?.name ?? "";
  const volumes = s.spec?.volumes ?? [];
  return (
    <div style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,.35)", display: "flex", alignItems: "center", justifyContent: "center" }}>
      <div style={{ background: "#fff", padding: 24, borderRadius: 8, width: 480 }}>
        <h3 style={{ marginTop: 0 }}>Remove {name}</h3>
        <p>The Service stops and its container goes away. Its data stays on the Pool unless you choose otherwise.</p>
        <label>
          <input type="checkbox" checked={deleteData} onChange={(e) => setDeleteData(e.target.checked)} /> Also delete its data
        </label>
        {deleteData && (
          <div style={{ color: "#b00020", fontSize: 13, marginTop: 8 }}>
            <b>This cannot be undone.</b> Gone for good:
            <ul>
              {volumes.map((v) => (
                <li key={v.name}>
                  {v.name} on Tier {s.spec?.volumeTiers[v.name] ?? "default"}
                </li>
              ))}
            </ul>
            Type <code>{name}</code> to confirm: <input value={typed} onChange={(e) => setTyped(e.target.value)} style={{ width: 200 }} />
          </div>
        )}
        <div style={{ marginTop: 16, display: "flex", gap: 8 }}>
          <button disabled={deleteData && typed.trim() !== name} onClick={() => onRemove(deleteData)}>
            {deleteData ? "Remove and delete data" : "Remove, keep data"}
          </button>
          <button onClick={onCancel}>Cancel</button>
        </div>
      </div>
    </div>
  );
}

function StateBadge({ s }: { s: ServiceStatus }) {
  const map: Record<number, [string, string]> = {
    [ServiceStatus_State.RUNNING]: [s.healthy ? "running" : "starting up", s.healthy ? "#2a7" : "#c90"],
    [ServiceStatus_State.STARTING]: ["starting", "#c90"],
    [ServiceStatus_State.STOPPED]: ["stopped", "#999"],
    [ServiceStatus_State.FAILED]: ["failed", "#b00020"],
    [ServiceStatus_State.REMOVING]: ["removing", "#999"],
  };
  const [label, color] = map[s.state] ?? ["unknown", "#999"];
  return <span style={{ color, fontSize: 13 }}>● {label}</span>;
}

const row: React.CSSProperties = { display: "flex", justifyContent: "space-between", alignItems: "center", padding: "8px 0", borderBottom: "1px solid #eee", gap: 12 };
