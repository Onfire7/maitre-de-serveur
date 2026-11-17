# To do

Work that is agreed but not yet done, grouped by the part of the system it touches. Ideas that still need planning go under **Ideas** until they are broken down into the sections above. Terms are as defined in `GLOSSARY.md`.

## Maître Server

The agent on every Node (`server/`).

- **Relay through the anchor.** Planned in the design session of 2026-11-17; the decisions are ADR 0012 and the term is in `GLOSSARY.md`. Today the Console reads the Fleet through the anchor, but every action, Page and file transfer goes straight to the Node concerned, so a Node the Desktop cannot reach itself can be seen but not used (decision 285). Ships before Terminals. Ready to break down into tickets:
  - **Node: the anchor's side.** An HTTP/1.1 `Upgrade` on the mTLS listener that relays to a Node named by node id, for Trusted Devices only (the Recovery Phrase's key included, Node keys refused), dialling only the target's recorded addresses. One hop. At most 256 relayed connections per Device, no bandwidth cap, TCP keepalives instead of idle timeouts. Each relayed connection is logged to the journal (Device, target, start, end, bytes each way).
  - **Node: anchor to target.** A full-duplex HTTP/2 stream over the Nodes' own mTLS link, sharing `fleetnet`'s carry code. The target hands it to its own mTLS listener in-process, authorizes the Device as usual, and checks the relaying peer holds a Node key.
  - **Desktop: one way to dial a Node.** A Rust function that returns a verified mTLS stream to a Node, direct or through the anchor's Relay (outer session to the anchor, inner handshake pinned to the target's node id). RPCs, file transfers and the loopback proxy all use it; reqwest gets a custom connector, or the RPC client moves to hyper with tokio-rustls.
  - **Desktop: direct first, Relay after a second.** Start direct; if TLS has not finished in about a second, start the Relay beside it; keep the first to finish for that Node until it fails or the Connection changes. On anchor failover relayed connections drop and reconnect through the new anchor; an idempotent RPC in flight is retried once.
  - **Console: showing it.** A Node reached through the Relay is Healthy; Unreachable means neither the Console nor the anchor reaches it. The Connection chooser and a Node's details say how each Node is reached ("at home", "over Tailscale", "through Boulder").
  - **Out of scope.** Dialling peers over Tailscale from inside the agent, needed only for a Fleet spread across homes.

## Image

The bootc image, its units and updates (`image/`).

## API

The Protobuf schema between the Console and Nodes (`api/`).

## Maître Desktop

The Tauri shell and the Console (`desktop/`).

- **Say how long this Node takes to update.** Planned 2026-11-17. The Update confirmation says "A Node normally takes under a minute", and the Restart and Roll back dialogs say "stop for a minute or two"; both are guesses, and a real update stages and reboots for several minutes. Each Node already keeps its last 20 `UpdateRun`s, but a run's start and end span two different things: staging (downloading and preparing the image, Services still running, as long as the download takes) and the restart (Services down, as long as the machine takes). Ready to break down:
  - **Record the restart.** The Node records when it begins restarting, in `NodeUpdate` and carried into `UpdateRun`, so each run has a total and a downtime. Runs from before this have no restart time and count toward the total only.
  - **The figures.** The median of the Node's last 5 runs that ended `OUTCOME_UPDATED` (rolled-back runs restart twice and staged ones never, so neither counts), for the total and for the downtime, rounded to whole minutes, "under a minute" below one. Fewer than 3 such runs (with a restart time, for the downtime): no number, and the wording falls back to "a few minutes". Never borrowed from another Node; machines differ. The Node computes them, one implementation in Go, and reports them in its `GetUpdateStatus`; the rollout line uses the same function on the first Node's record; the Console only shows them.
  - **Where they appear.** The Update confirmation: "Updates usually take about 6 minutes; its Services are down for about 2", or "its Services keep running until it next restarts" when restarting is unticked. Each Node's card, on its details and on the Updates page: the same line. The Restart (Nodes list) and Roll back dialogs: "Its Services stop for about 2 minutes", the restart being the same boot. The rollout line that names the first Node: "waiting for Avalon to take it first (usually about 6 minutes)", the first Node's figure only, never a sum for the Fleet. The Terminal warnings before an Update or a Restart (Terminals, ticket #9) say how long the Services will be down.
  - **The progress bar for an update.** One bar in three sections: *Downloading*, *Restarting*, *Overtime*. Staging and restarting take widths from the Node's medians (staging = total minus downtime), scaled so the typical end sits at 75%, marked with a line labelled "usually about 6 min"; the last 25% is overtime. Downloading fills by the real staging percentage, never spilling over; Restarting fills by the time since the restart began. Once the restart passes its median plus 10%, the bar turns red and fills Overtime, with "Taking longer than usual. Its monitor shows the Maître banner when it is back, or an error if it is not." Past the end it stays full and red. The bar is labelled with the total elapsed time. An update staged without a restart shows Downloading only.
  - **The progress bar for a Restart or Roll back.** One timed section, the downtime median at 75% with its marker, red past the median plus 10%, the same hint.
  - **Without enough history.** The staging percentage, then today's pulsing bar labelled with the elapsed time, no marker.
  - **Out of scope.** An estimate for a rollout across the Fleet.

## Catalog

The Catalog entries and their format (`catalog/`, Onfire7/maitre-catalog).

- **Add lawn** ([pingdotgg/lawn](https://github.com/pingdotgg/lawn), MIT, lawn.video): video review for creative teams, a self-hosted stand-in for Frame.io. It is not ready to package as it is. It deploys only to Vercel with a hosted Convex backend, ships no container image or Compose file, and its `.env.example` expects Clerk (sign-in), Stripe and Autumn (billing), Mux and Chunkify (video), and an S3 bucket. An entry needs a self-hosted Convex backend as a component, a local S3 store (or a volume) for the videos, and a way to run without Stripe and Autumn. Clerk and Mux are hosted services with no self-hosted version, so check first whether the app can run without them; if it can't, the entry needs a fork or an upstream change.

- **AdGuard Home's Page breaks after its own setup wizard.** The entry declares the web Page on port 3000, which is only where AdGuard serves its first-run wizard; the wizard asks for the web port and writes whatever the person picks into `AdGuardHome.yaml`. Avalon, 2026-11-12: the person picked 80, so after the restart systemd said running, DNS answered, and the Console said "not answering its health check" because the probe at 3000 was refused, with nothing to say why. The entry is only right while the person types 3000 in a wizard that never mentions it. Overriding the port with `--web-addr` would make the wizard's field a silent lie, and seeding the config would have Maître handle a password the app's wizard is meant to own; neither is acceptable. The fix is in two ideas below: **Typed health checks**, so the Console says which check failed and the DNS check stays honest, and the **Configuration Wizard**, so the person is told to keep 3000 before the app's wizard asks. Until then, the entry's description should at least say so.

## CI and tooling

GitHub Actions, the QEMU checks, `make` targets and the `maitre` development subcommands.

## Ideas

### Service commands

A Catalog entry can declare commands, and each one appears in that Service's menu in the Console. Choosing one opens a terminal, instead of a Page, running that command in the right container of the Service. This is for setup and maintenance that an app only offers from its command line: making a pairing code, resetting an admin password, running a migration or a cleanup.

A sketch of the manifest field, beside `pages:`:

```yaml
commands:
  - name: pair                 # [a-z0-9-], unique within the entry
    label: Make a pairing code # what the menu calls it
    component: server          # which container; required when the entry has components
    run: [app, pair, --new]    # argv, not a shell string
```

Settled with Terminals (ADR 0011, 2026-11-17): a command is a Terminal started by name, over the same stream; the Node resolves the argv from the signed entry, and the Console never sends a command line. It always opens a new Terminal titled with the command's label, which keeps running when the view closes or the connection drops, and is disabled, with the reason, while the Service is stopped. A Node without `terminal` in its features hides the menu items. An entry whose image has no shell offers only its commands.

To work out before this is broken down:

- **Arguments.** Fixed argv only, or prompts filled from a small form (like `settings:`) before the terminal opens?
- **Trust.** The commands come from the signed Catalog bundle, the same as the rest of the entry.
- **Custom Services.** Can a user add their own commands in Service Configuration, the way they can override other options?

### Terminals

Planned in the design session of 2026-11-17; the decisions are ADR 0011 and the term is in `GLOSSARY.md`. A Terminal is a shell or command inside one of a Service's containers, opened from the Console by a Trusted Device, held by the Service's Node. **Relay through the anchor** (under Maître Server) ships first. It gives Service commands their transport and their view. Ready to break down into tickets:

- **API.** Protobuf messages for the stream's frames (start: Service, component, argv or the shell, or a Service command's name, user, size; input; resize; output; exit with its code), a way to list a Service's Terminals with their state for the Console, and `terminal` in `NodeInfo.features`.
- **Node: the session.** Each Terminal runs `podman exec` with a TTY in a transient systemd unit of its own that holds the PTY, so the agent can restart without ending it. About 1 MiB of recent output in memory for replay on attach; any number of Devices attached at once, the last to type or resize setting the size; at most 10 per container. It ends when its process exits, on End (SIGHUP, then SIGKILL after 5 s), or when its container stops; no idle timeout. A finished Terminal keeps its exit code until a Device has seen it, or 24 hours.
- **Node: the shell and the user.** The component's `shell:` from the Catalog entry, else the same field set in Service Configuration (custom Services), else `/bin/bash`, else `/bin/sh`, else "This container has no shell" and only the entry's Service commands. The image's default user, or root when asked.
- **Node: the endpoint and its checks.** A WebSocket on the Service tunnel (`svcproxy`), the frames as binary messages. Only a full Trusted Device; never a Node key or the Recovery Phrase. Records, without keystrokes or output: Device, Service, component, argv, user, start, end, exit code, and each attach.
- **Node: what ends Terminals.** Automatic rollouts on a Node wait up to a day for its Terminals to finish.
- **Desktop: the loopback proxy.** A one-time token in the URL for each Terminal connection, valid for one upgrade or 30 s, checked before the proxy relays.
- **Desktop: the view.** A `TerminalView` in the Service's area, beside its Pages, labelled with the container and user ("server · root"), never dropped for being idle and not counted in the frames' limit. It shows the exit code and **Run again** when its process ends. A Service with protected grants gets a warning line naming them. The Service's area lists its running and finished Terminals; disabled, with the reason, while the Service is not running.
- **Desktop: the warnings.** Stop, Restart, Update, Move, Remove, applying Service Configuration, and a Node's Update or Reboot name the running Terminals they will end, without refusing.
- **Desktop: which terminal library.** Decided by a throwaway prototype in the Tauri app on Linux (WebKitGTK above all), macOS and Windows, not by guessing. Candidates: xterm.js with its WebGL renderer, Unicode 11 widths and the fit addon; ghostty-web (Ghostty's engine as WASM on a canvas, the same API); and other libraries still marked unstable if they are generally said to work well. The checks: the app's shortcuts never steal the Terminal's keys (Ctrl+W, Ctrl+Tab), Option/Alt as Meta, dead keys and IME, copy and paste per platform, `vim`, `htop` and `mc`, a 50 MB `cat`, emoji and CJK widths, crisp text at fractional scaling, a password prompt, and resizing during output. A native libghostty surface is reconsidered only if every web candidate fails on WebKitGTK. Revisit ghostty-web once it builds on Ghostty's own WASM release.
- **Desktop, after the view: Open in your terminal.** A small bridge binary shipped as a Tauri sidecar, launched in the person's own terminal app (Windows Terminal or conhost, the macOS default, `xdg-terminal-exec` or a short list on Linux), attached to the same session through the same token.
- **Considered and rejected.** A busybox sidecar for images without a shell: it would pull an image no Catalog entry signed and run outside the confinement of ADR 0008. A bidirectional Connect RPC: see ADR 0011.

### Typed health checks

A Service has one health check today: the Node sends an HTTP GET to the first Page's published loopback port, at `health.http` or the Page's path (`services.go`, `probe`), and the answer is one flag. Components may also carry a Quadlet `HealthCmd=`, which only `wait_healthy` uses. The two never meet, and a failed check has no reason the Console can show. AdGuard Home (above) is the case: the check that mattered, DNS, answered the whole time, and the one that failed, the web page, failed for a reason one sentence would have explained.

A sketch of `health:` as a list of checks, each of one type:

```yaml
health:
  - name: dns
    dns: { port: 53, query: example.com }   # a query gets an answer
  - name: web
    http: { page: web, path: / }            # today's check, by Page
  - name: db
    component: db
    cmd: [pg_isready, -U, docmost]          # rendered to HealthCmd=, read back from Podman
  - name: api
    tcp: { port: 8080 }                     # the port accepts a connection
```

Rules: every check passes means Online; any failure means Attention, with a message that names each check and what it tried ("web: port 3000 refused; dns: answered"). A failed `http` check also marks that Page as not answering, so the Pages list shows it. Values may use the entry's settings, `{{ .Settings.lan_address }}` style, the same as the container templates. The one-form `health: { http: / }` stays and reads as a list of one, so no entry changes.

To work out before this is broken down:

- **Where a check is sent.** From the Node to the Service's address on the shared maitre network, not its LAN address: a macvlan parent cannot reach its children, so a DNS check at `lan_address` would always fail from the Node. Whether phones on the LAN can reach it is a different check, from another Node or not at all, and a separate decision.
- **The API.** `PageStatus.healthy` becomes a list of check results on `ServiceStatus` (name, passed, what it tried), gossiped like the rest of the status (ADR 0006). Following the "Console vs older Nodes" rule, a Node without it keeps sending the flag.
- **`cmd` checks.** Rendered to `HealthCmd=` with `Notify=healthy` where `wait_healthy` asks for it; the Node reads the result from Podman rather than running the command itself.
- **Interval and grace.** One interval for all, or per check, and how long after start before a failure counts.

### Configuration Wizard

A Catalog entry can carry its own Configuration Wizard, a file beside the manifest, that the Console runs when the Service is installed. It is a sequence of steps; each shows text, some of the entry's `settings:`, or one of its Pages. Its purpose is the facts an app's own first-run wizard does not know and the person cannot guess: keep the web port at 3000, pick DNS on all interfaces, give your router this address afterwards. The app's wizard stays the owner of its own secrets; Maître never seeds them, it only says what to type where.

A sketch of `install.yaml`:

```yaml
steps:
  - title: Address on your home network
    text: Pick one outside your router's DHCP pool. Your devices will use it as their DNS server.
    settings: [lan_address]
  - title: AdGuard's own setup
    text: |
      AdGuard Home opens its setup wizard on first start. On its "Admin Web Interface"
      step keep port 3000: Maître reaches the page there, and a different port breaks it.
      Listen for DNS on all interfaces, port 53. Choose an admin login; Maître does not keep it.
    page: web                 # the Service starts first; the Page opens here once its check passes
  - title: Point your devices at it
    text: Give your router {{ .Settings.lan_address }} as its DNS server, or set it on each device.
```

The flow: Install opens the Wizard in the Service's area. Steps with `settings:` show the existing settings form for those keys, which keeps one concept for environment variables and settings. The first step with `page:` deploys the Service and waits for its health check, then opens that Page inside the step. The last step leads to the Service's area as today. An entry with no `install.yaml` gets today's one-page form, so nothing changes for the rest of the Catalog.

To work out before this is broken down:

- **Knowing a wizard is done.** The person clicks Next, or the Service's health passes after the wizard restarts the app, or both.
- **Waiting on a Page.** `page:` needs the Service deployed before the step: which steps run before deploy (settings) and which after (pages, final text), and whether that is implied by order or declared.
- **Coming back.** A "Run the Wizard again" item in the Service's menu, for a reinstall, a move, or a person who closed it; and whether a step can be marked done.
- **Converted entries.** `convert` cannot write one; a converted entry without one is fine, so this is never a reason to mark an entry unfinished.
- **Where it runs.** The Console renders it from the signed bundle, so no Node change is needed beyond what typed health checks already add. A Node without those still works: the Page step opens on the running state alone.

### A Claude-powered video editing Service

Create a video editing Service that uses Claude to do the editing. Nothing is planned yet. To work out: what the person asks for and how (a chat beside a timeline, or edits described in plain words), what Claude actually drives (ffmpeg, or an existing editor's project format), whose Claude account and API key it uses, and where the footage lives (a shared folder, so the Service works on files other Services can already see). The T3 Code entry already runs Claude Code inside a Service, which may be a starting point.
