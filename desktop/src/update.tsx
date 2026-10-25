import { useEffect, useMemo, useState } from "react";
import { createClient } from "@connectrpc/connect";
import { NodeService, UpdateStatus_State, type ImageDeployment, type UpdateStatus } from "./gen/maitre/v1/node_pb";
import { transportFor, type Target } from "./transport";

// Decision 82: updates are staged by the Node; the Console chooses whether to
// reboot right away (the default) or later.
export function UpdatePanel({ target }: { target: Target }) {
  const client = useMemo(() => createClient(NodeService, transportFor(target)), [target.addr, target.fingerprint]);
  const [status, setStatus] = useState<UpdateStatus | null>(null);
  const [error, setError] = useState("");
  const [image, setImage] = useState("");
  const [reboot, setReboot] = useState(true);
  const [insecure, setInsecure] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let stop = false;
    const tick = () =>
      client
        .getUpdateStatus({})
        .then((r) => {
          if (stop) return;
          setStatus(r.status ?? null);
          setError("");
        })
        .catch((e) => !stop && setError(String(e)));
    tick();
    const t = setInterval(tick, 2000);
    return () => {
      stop = true;
      clearInterval(t);
    };
  }, [client]);

  // Suggest the image the Node runs now, so a retag/rebuild is one click.
  useEffect(() => {
    if (!image && status?.booted?.image) setImage(status.booted.image);
  }, [status?.booted?.image]);

  // A registry on a .local name, a bare IP, or a non-443 port is a LAN Depot.
  useEffect(() => {
    const host = image.split("/")[0] ?? "";
    setInsecure(/\.local(:\d+)?$/.test(host) || /^\d+\.\d+\.\d+\.\d+(:\d+)?$/.test(host) || /^localhost(:\d+)?$/.test(host));
  }, [image]);

  async function run(fn: () => Promise<unknown>) {
    setBusy(true);
    try {
      await fn();
      setError("");
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  if (!status) return null;
  if (!status.supported) return <p style={{ color: "#555" }}>This Node does not run on bootc, so it cannot update itself.</p>;

  const updating = status.state === UpdateStatus_State.UPDATING || status.state === UpdateStatus_State.REBOOTING;
  return (
    <section>
      <h3>Maître Server image</h3>
      {error && <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>{error}</pre>}
      <table>
        <tbody>
          <Deployment label="Running" d={status.booted} />
          {status.staged && <Deployment label="Staged for next boot" d={status.staged} />}
          {status.rollback && <Deployment label={status.rollbackQueued ? "Rollback (boots next)" : "Previous"} d={status.rollback} />}
        </tbody>
      </table>

      {updating && (
        <div style={{ margin: "8px 0" }}>
          <progress value={status.progress} max={1} style={{ width: "100%" }} />
          <div style={{ color: "#555", fontSize: 13 }}>
            {status.state === UpdateStatus_State.REBOOTING ? "Rebooting into the new image…" : `Updating to ${status.target}: ${status.message}`}
          </div>
        </div>
      )}
      {status.state === UpdateStatus_State.FAILED && (
        <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>
          Update to {status.target} failed: {status.message}
        </pre>
      )}
      {!updating && status.staged && !status.rollbackQueued && (
        <p style={{ color: "#555", fontSize: 13 }}>An update is staged. Reboot to start running it.</p>
      )}

      <div style={{ display: "flex", flexDirection: "column", gap: 6, marginTop: 8 }}>
        <label>
          Image{" "}
          <input value={image} onChange={(e) => setImage(e.target.value)} style={{ width: "32em" }} disabled={updating} placeholder="registry/maitre-server:tag" />
        </label>
        <label>
          <input type="checkbox" checked={reboot} onChange={(e) => setReboot(e.target.checked)} /> Reboot now
        </label>
        <label title="Set automatically for .local names, IP addresses and localhost. The Node marks that registry as insecure in containers-registries.conf.">
          <input type="checkbox" checked={insecure} onChange={(e) => setInsecure(e.target.checked)} /> Registry uses plain HTTP (local Depot)
        </label>
        <div style={{ display: "flex", gap: 6 }}>
          <button disabled={busy || updating || !image.trim()} onClick={() => run(() => client.updateImage({ image: image.trim(), reboot, insecureRegistry: insecure }))}>
            Update
          </button>
          <button
            disabled={busy || updating || !status.rollback}
            onClick={() => {
              if (confirm(`Boot the previous image (${status.rollback?.version || status.rollback?.digest.slice(0, 19)})${reboot ? " and reboot now" : ""}?`))
                run(() => client.rollback({ reboot }));
            }}
          >
            Roll back
          </button>
          <button
            disabled={busy || updating}
            onClick={() => {
              if (confirm("Reboot this Node now? Services will be unavailable for a minute.")) run(() => client.reboot({}));
            }}
          >
            Reboot
          </button>
        </div>
      </div>
    </section>
  );
}

function Deployment({ label, d }: { label: string; d?: ImageDeployment }) {
  if (!d) return null;
  return (
    <tr>
      <td>{label}</td>
      <td>
        <code>{d.image}</code>
        <div style={{ color: "#555", fontSize: 13 }}>
          {d.version && `version ${d.version} · `}
          {d.digest && <code title={d.digest}>{d.digest.slice(0, 19)}</code>}
          {d.timestamp && ` · built ${new Date(d.timestamp).toLocaleString()}`}
        </div>
      </td>
    </tr>
  );
}
