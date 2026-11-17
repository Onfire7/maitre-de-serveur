---
status: accepted
---

# A Terminal is a Trusted Device's shell into a container, held by its Node

Settled in the design session of 2026-11-17. Some apps can only be set up or repaired from their own command line: making a pairing code, resetting an admin password, running a migration. A Node has no terminal of its own on purpose, so the Console gains a **Terminal**: a shell or command running inside one of a Service's containers, with keystrokes in and output out. Service commands, declared by a Catalog entry, are Terminals that start with the entry's argv; there is one mechanism, not two. Four parts of this would surprise a later reader, so this ADR records them.

**A Terminal is a Trusted Device's power and needs no grant.** Only a full Trusted Device may open or attach to one; a Node key or the Recovery Phrase alone may not. It ships in every image, not only in debug builds like `DebugExec`. It runs inside the container, as the image's default user or as root if the person picks "Run as root", and stays inside the container's confinement (ADR 0008). A Service whose grants include protected options can reach the Node through them, and so can a Terminal inside it; the Terminal shows a warning line naming those grants but asks for no confirmation, because a Trusted Device that signed the grants could already do as much. The Node records who opened or attached to which Terminal, on which Service and container, with what argv and user, when it started and ended, and its exit code. It never records keystrokes or output, since those include passwords typed at prompts.

**The Node holds the session, not the Console.** Losing a half-finished migration because a laptop slept or the agent was updated would be worse than having no Terminal at all. So each Terminal runs in its own transient systemd unit that holds the PTY; the agent connects to it and can restart without ending it. A Terminal ends only when its process exits, when someone presses End (SIGHUP, then SIGKILL after 5 s), or when its container stops; there is no idle timeout. The Node keeps about 1 MiB of recent output in memory, never on disk, to replay to whoever attaches, and keeps a finished Terminal's exit code until a Device has seen it or for 24 hours. Sessions are shared: any Trusted Device may attach to a running Terminal, and the Device that last typed or resized sets its size. A container may hold up to 10 Terminals. Stopping, restarting, updating, moving or removing a Service, applying its Service Configuration, and updating or rebooting its Node warn and name the Terminals they will end, without refusing; automatic rollouts wait up to a day for them.

**The stream is a WebSocket over the Service tunnel, not a Connect RPC.** Every RPC in `api/` is unary, the Desktop's `rpc` command buffers whole requests and responses, and connect-web cannot carry a bidirectional stream from a webview. The Service tunnel (decision 71) already carries WebSocket upgrades from the loopback proxy to the Node end to end. A Terminal is a WebSocket on that tunnel whose binary frames are Protobuf messages defined in `api/` (start, input, resize, output, exit), so the schema stays the one source. Because the loopback proxy listens on 127.0.0.1 and any local process could reach it, each Terminal connection carries a one-time token from the Desktop that expires after one upgrade or 30 s. A Console sees `terminal` in `NodeInfo.features` before it offers one. A Device that can reach only the anchor reaches the Service's Node through the anchor's relay, an opaque TCP pipe carrying the Device's own mTLS session, so the anchor can neither read nor inject keystrokes.

**No helper container for images without a shell.** The shell is the one the Catalog entry's component declares (`shell:`), or the one set in Service Configuration for a custom Service, or else `/bin/bash`, then `/bin/sh`. A container with none says so and offers only its Service commands. A busybox sidecar joining the container's namespaces was rejected: it would have the Node pull an image no Catalog entry signed and start a container outside the confinement ADR 0008 describes.

## Considered options

- **The Console holds the session; closing or disconnecting kills the process.** Simplest, but a dropped connection or an agent update would leave a migration half-done.
- **The agent holds the PTY.** Survives a dropped connection but not an agent restart, which is the most common cause of one.
- **One Terminal per container, or a second Device taking it over.** Shared sessions are simpler, and two people typing at once is not a real worry in a home lab.
- **Refusing a Terminal into a Service with protected grants, or asking for its name.** Blocks the Services most often repaired by hand, and protects nothing a signed grant has not already given.
- **A bidirectional Connect RPC.** Needs the first streaming client in the Desktop's Rust and a Tauri Channel to the webview; the tunnel already carries the stream.

## Consequences

- The Desktop draws the Terminal in a view inside its main webview, beside the Service's Pages; an open Terminal is never dropped for being idle. Which terminal library it uses is chosen by trying candidates in the Tauri app on every platform, WebKitGTK above all. "Open in your terminal" later attaches the person's own terminal app to the same session.
- ADR 0008 is amended: "a Node is not an administrator" still holds, since only a Trusted Device opens a Terminal; and the Node still has no terminal of its own, only its Services' containers do.
