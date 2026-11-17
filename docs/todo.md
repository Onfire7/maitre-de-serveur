# To do

Work that is agreed but not yet done, grouped by the part of the system it touches. Ideas that still need planning go under **Ideas** until they are broken down into the sections above. Terms are as defined in `GLOSSARY.md`.

## Maître Server

The agent on every Node (`server/`).

## Image

The bootc image, its units and updates (`image/`).

## API

The Protobuf schema between the Console and Nodes (`api/`).

## Maître Desktop

The Tauri shell and the Console (`desktop/`).

- **Say how long this Node takes to update.** The Update confirmation says "A Node normally takes under a minute" (`app/NodeDetails.tsx`), which is wrong: a real update stages and reboots, and takes several minutes. "Stop for a minute or two" (`NodeDetails.tsx`, `app/NodesTab.tsx`) is the same guess. Each Node already keeps its `UpdateRun` history, with start and end times and an outcome. Use the average of its recent `OUTCOME_UPDATED` runs ("usually about 6 minutes"), and keep a plain fallback, without a number, for a Node with no finished update yet. To decide: the average or the median, how many runs to count, and whether a progress view should show the elapsed time against it.

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

To work out before this is broken down:

- **Interactive or not.** A real terminal (a PTY, keystrokes in, output streamed out, resize) or only streamed output? Some admin tools prompt for input, so a terminal is the likely answer. That needs a streaming RPC, which none of the Service RPCs are yet.
- **Which RPC.** It would be a new Service RPC that runs an entry's declared command by name. The Console never sends a command line, so this is not a general exec, and it is not `DebugExec`, which only debug builds have. Following the "Console vs older Nodes" rule, a Node without the RPC hides the menu items instead of failing.
- **Arguments.** Fixed argv only, or prompts filled from a small form (like `settings:`) before the terminal opens?
- **Where the terminal lives.** A Console view in the Service's area, next to its Pages, with a terminal component in the Desktop. What happens on close: the command is killed, or it keeps running and you can come back to it?
- **The container's state.** The command runs only while the Service is running. Show the item disabled, with the reason, when it is stopped?
- **Trust.** The commands come from the signed Catalog bundle, the same as the rest of the entry. A free-form shell into a container is a separate decision, below under **Interactive Service console**.
- **Custom Services.** Can a user add their own commands in Service Configuration, the way they can override other options?

### Interactive Service console

A terminal in the Console attached to one of a Service's containers: a shell, or any command typed into it, with keystrokes in, output out, and resizing. Service commands need the same machinery, a PTY in the container streamed to a terminal view in the Desktop, so building the console first gives commands their transport and their view, and a command becomes a console that starts with a fixed argv.

To work out before this is broken down:

- **The RPC.** A bidirectional stream: a start message (Service, component, argv or the image's shell, terminal size), then input and resize frames in and output frames out, ending with the exit code. Connect over HTTP/2 carries bidi streams, but nothing in the API uses one yet, so check the Desktop's Rust client and the loopback proxy pass it through. On the Node it is `podman exec -it` or the Podman API's exec with a TTY. Following the "Console vs older Nodes" rule, a Node without the RPC hides the console instead of failing.
- **Which shell.** Many images have no shell at all (distroless, scratch). Fall back to `/bin/sh`, say plainly when there is none, and think about a debug sidecar with busybox that joins the container's namespaces.
- **The terminal view.** A terminal component (xterm.js or similar) placed like a Page in the Service's area, so it can sit beside the Service's Pages. On close: kill the process, or keep the session so it can be reattached, which needs the Node to hold it.
- **Trust.** This is the free-form shell the Service commands sketch left out. It cuts against "a Node is not an administrator" (ADR 0008): a container with a granted host mount, or one that runs privileged, is a root shell on the Node, and the Node has no terminal of its own on purpose. Options: refuse the console for Services with host grants, or warn and require typing the Service's name like other destructive actions. It is only offered to paired devices over mTLS either way, and could be left out of a shipped image like the debug tier if it should not be a product feature.
- **Which user.** The container's default user, or a choice of root, which most images run as anyway.
- **Records.** Whether the Node logs who opened a console on what, the same way an update run now records who asked for it.

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
