import { useState } from "react";
import { createClient } from "@connectrpc/connect";
import { create, toBinary } from "@bufbuild/protobuf";
import { NodeService } from "./gen/maitre/v1/node_pb";
import { TailscaleConfigSchema } from "./gen/maitre/v1/tailscale_pb";
import { transportFor } from "./transport";
import type { ConsoleState } from "./state";

// Milestone 5: one Tailscale OAuth client for the whole Fleet. Every Node
// uses it to make its own auth key and joins the tailnet by itself.
export function TailscalePanel({ state }: { state: ConsoleState }) {
  const [open, setOpen] = useState(false);
  const [id, setId] = useState("");
  const [secret, setSecret] = useState("");
  const [tag, setTag] = useState("tag:maitre");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState(false);
  if (state.nodes.length === 0) return null;
  async function save() {
    setBusy(true);
    setError("");
    const body = toBinary(TailscaleConfigSchema, create(TailscaleConfigSchema, { oauthClientId: id.trim(), oauthClientSecret: secret.trim(), tag: tag.trim(), addedUnixMs: BigInt(Date.now()) }));
    for (const n of state.nodes)
      for (const addr of n.addresses) {
        try {
          await createClient(NodeService, transportFor({ addr, fingerprint: n.node_id })).putRecord({ record: { kind: "tailscale", id: "fleet", body } });
          setDone(true);
          setSecret("");
          setBusy(false);
          return;
        } catch (e) {
          setError(String(e));
        }
      }
    setBusy(false);
  }
  return (
    <section>
      <h2>Reach the Fleet from anywhere</h2>
      {!open ? (
        <p>
          <button onClick={() => setOpen(true)}>Connect the Fleet to Tailscale…</button>
          <span style={{ color: "#555", fontSize: 13, marginLeft: 8 }}>Each Node joins your Tailscale network, so this app and Remote Services work away from home.</span>
        </p>
      ) : (
        <div style={{ border: "1px solid #ddd", borderRadius: 8, padding: 12 }}>
          <ol style={{ color: "#555", fontSize: 13 }}>
            <li>
              In the Tailscale admin console, Access controls, add a tag owner: <code>"tagOwners": {"{"} "{tag}": ["autogroup:admin"] {"}"}</code>.
            </li>
            <li>
              Under Settings, OAuth clients, create a client with the <b>Auth Keys</b> write scope for that tag. Copy its ID and secret here. The secret is stored in the Fleet and never shown again.
            </li>
          </ol>
          <label>
            Tag <input value={tag} onChange={(e) => setTag(e.target.value)} style={{ width: 160 }} />
          </label>
          <br />
          <label>
            Client ID <input value={id} onChange={(e) => setId(e.target.value)} style={{ width: 300 }} />
          </label>
          <br />
          <label>
            Client secret <input type="password" value={secret} onChange={(e) => setSecret(e.target.value)} style={{ width: 300 }} />
          </label>
          {error && <pre style={{ color: "#b00020", whiteSpace: "pre-wrap", fontFamily: "inherit" }}>{error}</pre>}
          {done && <p style={{ color: "#2a7" }}>Saved. Each Node joins within a minute; its Node page shows its Tailscale name.</p>}
          <div style={{ marginTop: 8 }}>
            <button disabled={busy || !id.trim() || !secret.trim()} onClick={save}>
              Save to the Fleet
            </button>{" "}
            <button onClick={() => setOpen(false)}>Close</button>
          </div>
        </div>
      )}
    </section>
  );
}
