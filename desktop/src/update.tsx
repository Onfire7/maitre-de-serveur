import { useEffect, useMemo, useState } from "react";
import { createClient } from "@connectrpc/connect";
import { NodeService, UpdateStatus_State, type ImageDeployment, type UpdateStatus } from "./gen/maitre/v1/node_pb";
import { transportFor, type Target } from "./transport";

// Decision 82: updates are staged by the Node; the Console chooses whether to
// reboot right away (the default) or later.
/** What the Node is about to do, so the Node view can switch to the maintenance screen. */
export type Maintenance = { kind: "update"; target: string; reboot: boolean } | { kind: "rollback"; reboot: boolean } | { kind: "reboot" };

export function UpdatePanel({ target, onStarted }: { target: Target; onStarted: (m: Maintenance) => void }) {
  const client = useMemo(() => createClient(NodeService, transportFor(target)), [target.addr, target.fingerprint]);
  const [status, setStatus] = useState<UpdateStatus | null>(null);
  const [error, setError] = useState("");
  const [image, setImage] = useState("");
  const [reboot, setReboot] = useState(true);
  const [insecure, setInsecure] = useState(false);
  const [busy, setBusy] = useState(false);
  const [unsupported, setUnsupported] = useState(false);
  const [depot, setDepot] = useState<string[]>([]);

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
        .catch((e) => {
          if (stop) return;
          // A Node running an image from before updates existed.
          if (String(e).includes("unimplemented") || String(e).includes("not registered")) setUnsupported(true);
          else setError(String(e));
        });
    tick();
    const t = setInterval(tick, 2000);
    return () => {
      stop = true;
      clearInterval(t);
    };
  }, [client]);

  // Decision 91: offer what the local Depot has, when there is one.
  useEffect(() => {
    const ctl = new AbortController();
    const t = setTimeout(() => ctl.abort(), 3000);
    fetch("http://maitre-depot.local:8080/api/images", { signal: ctl.signal })
      .then((r) => r.json())
      .then((d: { registry: string; images: Record<string, string[]> }) =>
        setDepot(Object.entries(d.images).flatMap(([name, tags]) => tags.map((t) => `${d.registry}/${name}:${t}`))),
      )
      .catch(() => {})
      .finally(() => clearTimeout(t));
    return () => ctl.abort();
  }, []);

  // Suggest the image the Node runs now, so a retag/rebuild is one click.
  useEffect(() => {
    if (!image && status?.booted?.image) setImage(status.booted.image);
  }, [status?.booted?.image]);

  // A registry on a .local name, a bare IP, or a non-443 port is a LAN Depot.
  useEffect(() => {
    const host = image.split("/")[0] ?? "";
    setInsecure(/\.local(:\d+)?$/.test(host) || /^\d+\.\d+\.\d+\.\d+(:\d+)?$/.test(host) || /^localhost(:\d+)?$/.test(host));
  }, [image]);

  async function run(fn: () => Promise<unknown>, started: Maintenance) {
    setBusy(true);
    try {
      await fn();
      setError("");
      onStarted(started);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  if (unsupported)
    return (
      <section>
        <h3>Maître Server image</h3>
        <p style={{ color: "#555" }}>This Node runs a Maître Server from before updates existed. Reinstall it from current media or over the network to manage its image here.</p>
      </section>
    );
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
          <input value={image} onChange={(e) => setImage(e.target.value)} style={{ width: "32em" }} disabled={updating} placeholder="registry/maitre-server:tag" list="depot-images" />
          <datalist id="depot-images">
            {depot.map((ref) => (
              <option key={ref} value={ref} />
            ))}
          </datalist>
        </label>
        {depot.length > 0 && (
          <div style={{ color: "#555", fontSize: 13 }}>
            From your Depot:{" "}
            {depot.map((ref) => (
              <button key={ref} type="button" style={{ marginRight: 4 }} disabled={updating} onClick={() => setImage(ref)}>
                {ref.split("/").slice(1).join("/")}
              </button>
            ))}
          </div>
        )}
        <label>
          <input type="checkbox" checked={reboot} onChange={(e) => setReboot(e.target.checked)} /> Reboot now
        </label>
        <label title="Set automatically for .local names, IP addresses and localhost. The Node marks that registry as insecure in containers-registries.conf.">
          <input type="checkbox" checked={insecure} onChange={(e) => setInsecure(e.target.checked)} /> Registry uses plain HTTP (local Depot)
        </label>
        <div style={{ display: "flex", gap: 6 }}>
          <button disabled={busy || updating || !image.trim()} onClick={() => run(() => client.updateImage({ image: image.trim(), reboot, insecureRegistry: insecure }), { kind: "update", target: image.trim(), reboot })}>
            Update
          </button>
          <button
            disabled={busy || updating || !status.rollback}
            onClick={() => {
              if (confirm(`Boot the previous image (${status.rollback?.version || status.rollback?.digest.slice(0, 19)})${reboot ? " and reboot now" : ""}?`))
                run(() => client.rollback({ reboot }), { kind: "rollback", reboot });
            }}
          >
            Roll back
          </button>
          <button
            disabled={busy || updating}
            onClick={() => {
              if (confirm("Reboot this Node now? Services will be unavailable for a minute.")) run(() => client.reboot({}), { kind: "reboot" });
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

/**
 * Shown instead of the Node view while an update, rollback or reboot runs.
 * Polls the Node, treats connection failures as "rebooting", and hands back
 * once the Node answers again (or the update failed without a reboot).
 */
export function MaintenanceScreen({ target, name, m, onDone }: { target: Target; name: string; m: Maintenance; onDone: () => void }) {
  const client = useMemo(() => createClient(NodeService, transportFor(target)), [target.addr, target.fingerprint]);
  const [status, setStatus] = useState<UpdateStatus | null>(null);
  const [phase, setPhase] = useState<"working" | "down" | "back" | "failed">("working");
  const [failure, setFailure] = useState("");
  const [seconds, setSeconds] = useState(0);
  const expectReboot = m.kind === "reboot" || m.reboot;

  useEffect(() => {
    let stop = false;
    let wentDown = m.kind === "reboot";
    const started = Date.now();
    const tick = async () => {
      if (stop) return;
      setSeconds(Math.round((Date.now() - started) / 1000));
      try {
        const st = (await client.getUpdateStatus({})).status;
        if (stop || !st) return;
        setStatus(st);
        if (wentDown) {
          // The Node answered after going away: it is back.
          setPhase("back");
          setTimeout(() => !stop && onDone(), 1500);
          stop = true;
          return;
        }
        if (st.state === UpdateStatus_State.FAILED) {
          setPhase("failed");
          setFailure(st.message);
          stop = true;
          return;
        }
        if (st.state === UpdateStatus_State.REBOOTING) {
          wentDown = true;
          setPhase("down");
        } else if (st.state === UpdateStatus_State.IDLE && !expectReboot) {
          // Staged without a reboot: nothing more to wait for.
          setPhase("back");
          setTimeout(() => !stop && onDone(), 1500);
          stop = true;
          return;
        }
      } catch {
        // Connection refused or reset: the Node is rebooting.
        wentDown = true;
        setPhase("down");
      }
      if (!stop) setTimeout(tick, 1500);
    };
    tick();
    return () => {
      stop = true;
    };
  }, [client]);

  const what =
    m.kind === "update" ? `Updating ${name} to ${m.target}` : m.kind === "rollback" ? `Rolling ${name} back to its previous image` : `Rebooting ${name}`;
  return (
    <section>
      <h2>{what}</h2>
      {phase === "working" && (
        <>
          <progress value={status?.progress ?? 0} max={1} style={{ width: "100%" }} />
          <p>{status?.message || "starting"}</p>
        </>
      )}
      {phase === "down" && (
        <>
          <progress style={{ width: "100%" }} />
          <p>The Node is restarting. Waiting for it to answer again ({seconds}s).</p>
          <p style={{ color: "#555", fontSize: 13 }}>A Node normally takes under a minute. Its monitor shows the Maître banner when it is back.</p>
        </>
      )}
      {phase === "back" && <p>{name} is back.</p>}
      {phase === "failed" && (
        <>
          <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>{failure}</pre>
          <button onClick={onDone}>Back to the Node</button>
        </>
      )}
      {phase === "down" && seconds > 180 && (
        <p>
          Taking longer than expected. <button onClick={onDone}>Back to the Node</button>
        </p>
      )}
    </section>
  );
}
