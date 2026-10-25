import { useEffect, useMemo, useRef, useState } from "react";
import { createClient } from "@connectrpc/connect";
import { invoke } from "@tauri-apps/api/core";
import { ServiceService, ServiceStatus_State, ControlServiceRequest_Action, ServiceSpecSchema, type ServiceStatus } from "./gen/maitre/v1/service_pb";
import { create } from "@bufbuild/protobuf";
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

export function ServicesPanel({ target, tiers = [] }: { target: Target; tiers?: { tierId: string; name: string }[] }) {
  const client = useMemo(() => createClient(ServiceService, transportFor(target)), [target.addr, target.fingerprint]);
  const [services, setServices] = useState<ServiceStatus[]>([]);
  const catalog = bundledCatalog as CatalogIndex;
  const [error, setError] = useState("");
  const [installing, setInstalling] = useState<CatalogEntry | null>(null);
  const [open, setOpen] = useState<{ name: string; url: string } | null>(null);
  const [logsFor, setLogsFor] = useState<{ name: string; lines: string[] } | null>(null);
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
                </div>
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
                <button
                  onClick={() => {
                    if (confirm(`Remove ${s.spec?.name}? Its data stays on the Node's Pool.`)) control(s.spec!.name, ControlServiceRequest_Action.REMOVE);
                  }}
                >
                  Remove
                </button>
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

      {installing && (
        <InstallDialog
          entry={installing}
          tiers={tiers}
          onCancel={() => setInstalling(null)}
          onInstall={async (name, settings, volumeTiers) => {
            try {
              lastWrite.current = Date.now();
              const spec = specFor(installing, name, settings);
              spec.volumeTiers = volumeTiers;
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
    webPort: e.web?.port ?? 0,
    webPath: e.web?.path ?? "",
    healthHttp: e.health?.http ?? "",
    volumes: e.volumes.map((v) => ({ name: v.name, path: v.path, size: v.size ?? "" })),
  });
}

function InstallDialog({
  entry,
  tiers,
  onCancel,
  onInstall,
}: {
  entry: CatalogEntry;
  tiers: { tierId: string; name: string }[];
  onCancel: () => void;
  onInstall: (name: string, settings: Record<string, string>, volumeTiers: Record<string, string>) => void;
}) {
  const [name, setName] = useState(entry.id);
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
        {entry.own_login && <p style={{ color: "#555", fontSize: 13 }}>{entry.name} has its own accounts; you will set one up the first time you open it.</p>}
        <div style={{ marginTop: 16, display: "flex", gap: 8 }}>
          <button disabled={!/^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$/.test(name)} onClick={() => onInstall(name, settings, volumeTiers)}>
            Install
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
