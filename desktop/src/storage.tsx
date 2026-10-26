import { useEffect, useMemo, useState } from "react";
import { createClient } from "@connectrpc/connect";
import { StorageService, type Drive, type DriveContentsResponse } from "./gen/maitre/v1/storage_pb";
import { transportFor, type Target } from "./transport";

// Milestone 4: the Drives of one Node. Adopt keeps what is on a disk;
// Format erases it and needs the model typed back (the guard); Forget
// leaves the data in place.
export function StoragePanel({ target }: { target: Target }) {
  const client = useMemo(() => createClient(StorageService, transportFor(target)), [target.addr, target.fingerprint]);
  const [drives, setDrives] = useState<Drive[]>([]);
  const [error, setError] = useState("");
  const [dialog, setDialog] = useState<{ kind: "adopt" | "format" | "forget"; drive: Drive } | null>(null);

  const refresh = async () => {
    try {
      setDrives((await client.listDrives({})).drives);
      setError("");
    } catch (e) {
      setError(String(e));
    }
  };
  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 5000);
    return () => clearInterval(t);
  }, [client]);

  const candidates = drives.filter((d) => !d.system);
  return (
    <section>
      <h3>Drives</h3>
      {error && <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>{error}</pre>}
      {candidates.length === 0 && <p style={{ color: "#555" }}>No disks besides the system disk. Plug one in and it appears here.</p>}
      <ul style={{ listStyle: "none", padding: 0 }}>
        {candidates.map((d) => (
          <li key={d.device} style={{ display: "flex", justifyContent: "space-between", alignItems: "center", padding: "8px 0", borderBottom: "1px solid #eee", gap: 12 }}>
            <div>
              <b>{d.record?.name || d.label || d.model}</b>{" "}
              {d.adopted ? <span style={{ color: "#2a7", fontSize: 13 }}>● in the Pool</span> : <span style={{ color: "#999", fontSize: 13 }}>● not used</span>}
              {d.record?.origin === "internal" && <span style={{ color: "#555", fontSize: 13 }}> · built in (the rest of the system disk)</span>}
              <div style={{ color: "#555", fontSize: 13 }}>
                {d.model} · {gb(d.sizeBytes)} · {d.fstype || "no filesystem"} · {d.device}
                {d.adopted && d.freeBytes > 0n && ` · ${gb(d.freeBytes)} free`}
              </div>
              <div style={{ fontSize: 12, color: "#777" }}>
                {[...d.autoTags, ...(d.record?.tags ?? [])].map((t) => (
                  <span key={t} style={{ background: "#eee", borderRadius: 4, padding: "0 6px", marginRight: 4 }}>
                    {t}
                  </span>
                ))}
              </div>
            </div>
            <div style={{ display: "flex", gap: 4 }}>
              {!d.adopted && d.fstype && <button onClick={() => setDialog({ kind: "adopt", drive: d })}>Adopt</button>}
              {!d.adopted && <button onClick={() => setDialog({ kind: "format", drive: d })}>Format</button>}
              {d.adopted && d.record?.origin !== "internal" && <button onClick={() => setDialog({ kind: "forget", drive: d })}>Remove</button>}
            </div>
          </li>
        ))}
      </ul>
      {dialog && (
        <DriveDialog
          kind={dialog.kind}
          drive={dialog.drive}
          client={client}
          onDone={() => {
            setDialog(null);
            refresh();
          }}
          onError={setError}
        />
      )}
    </section>
  );
}

function DriveDialog({
  kind,
  drive,
  client,
  onDone,
  onError,
}: {
  kind: "adopt" | "format" | "forget";
  drive: Drive;
  client: ReturnType<typeof createClient<typeof StorageService>>;
  onDone: () => void;
  onError: (e: string) => void;
}) {
  // Decision 104: the Node proposes a name from its letter's list.
  const [name, setName] = useState(drive.record?.name || drive.suggestedName || drive.label || drive.model);
  const [tags, setTags] = useState("");
  const [confirm, setConfirm] = useState("");
  const [contents, setContents] = useState<DriveContentsResponse | null>(null);
  const [busy, setBusy] = useState(false);
  const expected = kind === "format" ? (drive.model.endsWith(" disk") && !drive.serial ? drive.device : drive.model) : drive.record?.name ?? "";
  useEffect(() => {
    if (kind !== "forget" && drive.fstype) client.driveContents({ device: drive.device }).then(setContents).catch(() => setContents(null));
  }, [kind, drive.device, drive.fstype, client]);
  async function go() {
    setBusy(true);
    try {
      const tagList = tags.split(/[,\s]+/).filter(Boolean);
      if (kind === "adopt") await client.adoptDrive({ device: drive.device, name, tags: tagList });
      if (kind === "format") await client.formatDrive({ device: drive.device, name, tags: tagList, confirm });
      if (kind === "forget") await client.forgetDrive({ driveId: drive.driveId, confirm });
      onError("");
      onDone();
    } catch (e) {
      onError(String(e));
      onDone();
    } finally {
      setBusy(false);
    }
  }
  return (
    <div style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,.35)", display: "flex", alignItems: "center", justifyContent: "center" }}>
      <div style={{ background: "#fff", padding: 24, borderRadius: 8, width: 520, maxHeight: "80vh", overflow: "auto" }}>
        <h3 style={{ marginTop: 0 }}>
          {kind === "adopt" && `Adopt ${drive.model}`}
          {kind === "format" && `Format ${drive.model}`}
          {kind === "forget" && `Remove ${drive.record?.name}`}
        </h3>
        {kind === "adopt" && <p>The disk joins this Node's Pool with everything on it kept. Services will see its files.</p>}
        {kind === "format" && (
          <p style={{ color: "#b00020" }}>
            <b>Everything on this disk will be erased.</b> It becomes a btrfs Drive of this Node.
          </p>
        )}
        {kind === "forget" && <p>The Drive leaves the Pool. Files on it stay on the disk but Services can no longer reach them; anything a Service stored there becomes unavailable until it is adopted again.</p>}
        {contents && kind !== "forget" && (
          <div style={{ background: "#f6f6f6", padding: 8, borderRadius: 6, fontSize: 13, marginBottom: 8 }}>
            {contents.empty ? "The disk is empty." : `On the disk now (${gb(contents.usedBytes)} used):`}
            <ul style={{ margin: "4px 0 0 16px", padding: 0 }}>
              {contents.entries.map((e) => (
                <li key={e}>{e}</li>
              ))}
            </ul>
          </div>
        )}
        {kind !== "forget" && (
          <>
            <label>
              Name <input value={name} onChange={(e) => setName(e.target.value)} style={{ width: 240 }} />
            </label>
            <br />
            <label>
              Tags <input value={tags} onChange={(e) => setTags(e.target.value)} placeholder="media, backups" style={{ width: 240 }} />
            </label>
            <div style={{ color: "#555", fontSize: 13 }}>Tiers pick Drives by tag. Automatic tags: {drive.autoTags.join(", ")}.</div>
          </>
        )}
        {(kind === "format" || kind === "forget") && (
          <p>
            Type <code>{expected}</code> to confirm: <input value={confirm} onChange={(e) => setConfirm(e.target.value)} style={{ width: 260 }} />
          </p>
        )}
        <div style={{ marginTop: 16, display: "flex", gap: 8 }}>
          <button disabled={busy || ((kind === "format" || kind === "forget") && confirm.trim() !== expected)} onClick={go}>
            {kind === "adopt" ? "Adopt" : kind === "format" ? "Erase and format" : "Remove from the Pool"}
          </button>
          <button onClick={onDone}>Cancel</button>
        </div>
      </div>
    </div>
  );
}

function gb(n: bigint | number) {
  const v = Number(n);
  return v >= 1e12 ? `${(v / 1e12).toFixed(1)} TB` : `${Math.round(v / 1e9)} GB`;
}
