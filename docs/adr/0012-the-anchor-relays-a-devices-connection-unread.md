---
status: accepted
---

# The anchor relays a Device's connection to other Nodes, unread

Settled in the design session of 2026-11-17. The Console reads the Fleet through the anchor (decision 159), but every action, log, metric, Page, file transfer and folder picker goes straight to the Node concerned. A Node the Desktop cannot reach itself could be seen but not used. Away from home this is the usual case: through your domain the Desktop reaches only the Node holding the router's forward (decisions 218 and 330). In decision 285, an install on Avalon failed while Boulder was being read at the domain. Now the anchor carries the Device's connection to any other Node: the **Relay**.

**The anchor cannot read the traffic.** The Device opens its usual mTLS session to the anchor and asks, with an HTTP/1.1 `Upgrade`, for a relay to a Node named by its node id. The anchor checks the caller is a Trusted Device (the Recovery Phrase's key included, Node keys refused), and dials that Node only at the addresses the Fleet's own records give it, never at an address the Console supplies, so the anchor is not an open proxy into the home network. Inside the upgraded stream the Device makes a second mTLS handshake with the target, pinned to its node id as a direct connection would be. The anchor sees only ciphertext and can neither read, change, nor pose as either end. From the anchor to the target the bytes travel as a full-duplex HTTP/2 stream over the Nodes' own mTLS link, the way `fleetnet` already carries Service traffic between Nodes (ADR 0010). The target hands the stream to its own mTLS listener in-process, authorizes the Device as for any connection, and checks that the Node that relayed it holds a Node key. The Relay is one hop: if the anchor cannot reach a Node, that Node is Unreachable.

**Direct first, but not waiting for it.** Relaying everything would double every Page stream and file transfer across the anchor's link and make every Node depend on one, so the Console still prefers the direct way. It starts the direct connection, and if the TLS handshake has not finished in about a second, starts the Relay beside it. The first to finish wins and is remembered for that Node until it fails or the Connection changes. The Desktop has one function that connects to a Node, direct or relayed. RPCs, file transfers and the loopback proxy (Pages, file contents, Terminals) all use it, so no path can forget the Relay.

**Unreachable means neither the Console nor the anchor reaches a Node.** A Node reached through the Relay is Healthy. The Connection chooser and a Node's details say how each Node is reached ("at home", "over Tailscale", "through Boulder"), so slow traffic through the anchor can be explained.

**Smaller rules.** No bandwidth cap. A cap on concurrent relayed connections per Device (256). TCP keepalives rather than idle timeouts, since a Terminal may send nothing for hours. The anchor logs each relayed connection to its journal (Device, target, start, end, bytes each way) and writes no Fleet records. When the anchor fails over, relayed connections drop. New ones go through the new anchor: Pages reload, Terminals reattach (ADR 0011), and an idempotent RPC in flight is retried once. No Node advertises the Relay as a feature. Nodes are updated together, and an anchor without it refuses the `Upgrade`, shown as that Node's connection error.

## Considered options

- **Relay always, never direct.** One path, but a Jellyfin stream or a large upload from another Node would cross the anchor's link twice, and the anchor's uplink would cap every Node.
- **Terminate TLS at the anchor and forward the calls.** The anchor would read and could change every call, Page and keystroke, contrary to ADR 0008: a compromised Node must not take over the others.
- **A plaintext preamble naming the target, then the inner TLS.** The anchor could not check who asked, and would relay for anyone on the network.
- **Carrying the Device's signature and the relaying Node's together.** A relay could still read and change the stream. Running the end-to-end session inside the relay protects the contents, not only the start.
- **Dialling peers over Tailscale from inside the agent (tsnet).** Needed only for a Fleet spread across homes. Out of scope while a Fleet is typically one home; the anchor reaches other Nodes the way the Fleet's sync does.

## Consequences

- Amends decision 159: the anchor still serves the Fleet's reads, and now also relays any other connection the Console cannot make directly.
- The Desktop's RPC client and file transfers move onto the same dial function as the loopback proxy, so reqwest needs a custom connector or the RPC client moves to hyper with tokio-rustls.
- The Relay ships before Terminals (ADR 0011), which rely on it to reach a Service's Node from away.
