# Multi-container Services, full Quadlet, and Compose import: plan

Written 2026-10-28 against `11aac44`, revised the same day after the user's answers on Catalogs. Other work takes decision numbers in the meantime (209-215 are used), so proposals here are labelled C1, C2, … and questions Q1, Q2, …; each gets the next free number when it is recorded in `v1-build-order.md`. Only what is under Decided by the user is decided.

## Goal

Any Catalog entry, from the project's Catalog or a user's own, or any Docker Compose file a user adds to a Catalog, runs as one Maître Service, however many containers it has, with every Quadlet feature available, protected options still gated by grants (ADR 0008), and nothing about today's single-container Services changing for the user.

## Re-checked since the first discussion (2026-10-25)

What changed in the repo, and what it means for this plan:

| Area | Now | Effect on the plan |
|---|---|---|
| Security boundaries (189, ADR 0008) | ADR 0008 already promises full Compose compatibility. `services/protect.go` classifies the rendered unit and requires a signed `grant` per protected option. The Console asks in `GrantDialog.tsx`. | The grant check exists. It reads parsed units (C6) and names a component in each grant (C4). |
| Service folders (170-176, 205, 207, 208) | Volumes live at `<Space>/.Services/<Service>/<volume>`, or a picked folder, plus extra mounts. `MoveVolume`, `SetFolders`, Files usage all key on Service + volume. | Volumes stay Service-level. Compose's named volumes are project-level anyway, so components refer to the Service's volumes by name and none of this code changes. |
| Shared network | `image/maitre.network` (isolate=true). Services resolve each other by Service name, through `HostName=<service>` (9471cdf). | Keep it for the main component. Other components go on a per-Service network only. Cross-Service names keep working and databases stay off the shared network. |
| Exposure (109-115) | A tsnet node, Caddy route and loopback `web_host_port` per Service. | Unchanged: they belong to the component that serves the web page. |
| Updates (milestone 6, 193, 195) | One `image` and one `previous_image` per Service. `SetImage`, `AutoUpdate`, `Rollback`. Greenboot's running-services list reads `/etc/containers/systemd/<unit>.container` and its `dev.maitre.service` label. | Images and previous images move to each component, and rollback moves every component together. Greenboot works per component unit if each unit carries the label. |
| Move to another Node (191) | Console-only, `specFor` spreads the whole spec. | New fields travel automatically. Grants are already per Node, so the target asks again. |
| Older Nodes (ADR 0007, memory rule) | No new RPC may be mandatory. A Console falls back on Unimplemented. | An old Node ignores unknown spec fields and would refuse with "service spec has no quadlet". The Console needs a feature flag to say "this Node needs updating" instead. |
| Node software | Podman 5.8.2, systemd 257, Quadlet at `/usr/libexec/podman/quadlet`, `podman.socket` enabled, skopeo 1.22. No compose provider. | Quadlet 5.8 has everything needed: `.pod`, `.network`, `.volume`, `NetworkAlias`, `Notify=healthy`, `Secret`, `HealthCmd`. |
| Libraries | compose-go v2.16.2. podman v5.8.9's `pkg/systemd/parser` imports only the standard library. Its `pkg/systemd/quadlet` pulls in more of Podman. | Copy the parser (Apache-2.0, two files) rather than importing the Podman module. Validate with the Node's own Quadlet binary, which matches its Podman exactly. |

## Decided by the user (2026-10-28)

- **The Catalog holds Quadlet bundles that are known to work.** Every entry is the complete set of units a Service needs, tested before it is published. Compose is not a Catalog format.
- **Users can have Catalogs of their own**, beside the project's.
- **Compose files are converted when they are added to a Catalog**, by project staff or by a user. Whoever converts is told at once what needed changing and what still needs a person.
- **Other projects' catalogs are converted occasionally**, not followed. An import is a one-off conversion whose results become ordinary entries.
- **Icons come from the dashboard-icons and selfh.st packs**, with attribution where the licence asks for it.
- **Q1.** A Service's own network is not internal by default. A Service definition may make it internal (C2).
- **Q2.** Settings, generated passwords included, stay readable on every Node for now, because records are signed but not encrypted. ADR 0009 says so. Per-Node sealed fields remain a possible later change.
- **Q3.** Work ships in its permanent form, not as stopgaps to be replaced.
- **Trust comes from signatures, not from inspecting definitions.** A new bundle is signed by its Catalog or by the user who uploads it, and the deploy command is signed by the Console. Container definitions and conversions operate as the Compose and Quadlet specifications define them; Maître does not second-guess them beyond the existing grants of ADR 0008.
- **Q4.** A git Catalog follows its branch, and the Console shows what changed since the user last looked (C9).

## Design

### A Service is a set of components (C1)

```proto
message ServiceComponent {
  string name = 1;               // [a-z0-9-]; "" only in a single-component Service
  string image = 2;              // pinned by digest
  string previous_image = 3;     // for Rollback
  string quadlet = 4;            // a [Container] body as a Go template, as ServiceSpec.quadlet is today
  repeated string after = 5;     // components to start first
  bool wait_healthy = 6;         // dependants wait for this one's health check (Notify=healthy)
  bool egress = 7;               // reaches the outside even when the Service's network is internal
}

message ServiceUnit {
  string name = 1;               // file name without the Service prefix, e.g. "cache.volume"
  string body = 2;               // a .volume or .network unit, as a Go template
}

message ServiceSpec {
  …everything as now…
  repeated ServiceComponent components = 24;
  string web_component = 25;     // which component web_port, health_http and exposure refer to
  repeated ServiceUnit units = 26;  // the bundle's .volume and .network units, if it has any
  bool network_internal = 27;    // the Service's own network has no route out (Q1)
}
```

- **Single component stays the current path.** With `components` empty, `image`, `previous_image` and `quadlet` mean what they mean now, and the Node writes `<service>.container` exactly as today. No migration, no change for existing Services, and old Consoles keep working.
- **Several components.** Unit names are `<service>-<component>.container`, and container names match. Each unit gets `HostName=<component>`, `NetworkAlias=<component>` and `Label=dev.maitre.component=<component>`, alongside the existing `dev.maitre.service` label. Greenboot's list of running Services and the Node's screen pick components up through those labels.
- **Lifecycle through a target.** The agent writes `<service>.target`. Each component has `PartOf=` and `WantedBy=` on it. Start, Stop and Restart act on the target, so the existing `systemctl … <service>.…` call sites change in one place. `after` and `wait_healthy` become `After=`, `Requires=` and `Notify=healthy`, written by the agent from those fields. They never come from template text, so the rule that sections other than `[Container]` are protected stays as it is.
- **Networks (C2).** Every component joins `maitre-<service>.network`, which the agent writes. Only the web component also joins `maitre.network`, under the Service's name. Compose's DNS by service name works unchanged, the Service stays reachable by name from other Services, and its database is not.
  - The Service's network has a route out by default. A definition may set `network_internal` (manifest `network: internal`), and then the agent writes it with `Internal=true`.
  - In an internal Service, a component marked `egress` also joins `maitre-<service>-egress.network`, which the agent writes with a route out and no other members. The web component keeps its place on `maitre.network` either way.
  - Conversion follows the Compose file. If every network the file declares is `internal: true`, the Service is internal. A container that is also on a non-internal network gets `egress`.
- **Pods: not used (C3).** A pod shares one network namespace. Compose files expect `db:5432`, not `localhost:5432`.
- **Bundle units.** A `.volume` or `.network` in the bundle is written as `<service>-<name>`, and references to it in the bundle's containers are rewritten to match. Most bundles need neither, because Service volumes are folders in Spaces and the agent supplies the network.
- **Status, logs, health.** Status rolls up: Running when every component runs, Failed naming the component that failed, healthy when the web component's check passes and every `wait_healthy` component is healthy. `ServiceStatus` gains `repeated ComponentStatus components`. `GetServiceLogs` gains an optional `component`. Left empty, it interleaves every component's journal, which is what the Console's Logs tab shows today.
- **Images.** `SetImage` gains an optional `component`. `AutoUpdate` checks every component, and `Rollback` moves all of them back at once.
- **Grants (C4).** `Grant` gains `component`, and `GrantID` hashes it in. The image repository is per component. A grant with no component matches a single-component Service, so existing grants keep working.
- **Feature flag (C5).** `GetNodeInfo` gains `repeated string features`, starting with `"components"`. The Console offers multi-container entries only on Nodes that list it. Otherwise it says the Node needs updating, as it does for SetSettings and SetImage.

### Units are read and written as Quadlet reads them (C6)

Bundles come from many authors, so the Node handles every unit exactly as the Quadlet specification does.

- Copy `pkg/systemd/parser` (unitfile.go, split.go) into `server/internal/quadlet/parser` with its Apache-2.0 licence. Templates are rendered, then parsed with it, so line continuations, quoting, comments and repeated keys mean what they mean to Podman.
- The agent adds its own keys (HostName, PublishPort, labels, networks, the `[Unit]`, `[Service]` and `[Install]` lines) to the parsed unit and writes it back out, instead of splicing text.
- The existing grant check (ADR 0008) reads the parsed values, so it sees the same keys Quadlet sees, and names components (C4).
- Before writing any file, the Node runs its own Quadlet on the rendered units in a temporary directory, with the network and volume units they refer to:

  ```
  QUADLET_UNIT_DIRS=<tmp> /usr/libexec/podman/quadlet -dryrun
  ```

  Anything Quadlet rejects, such as an unknown key, fails the call with Quadlet's own message instead of producing a unit that silently does not start.

### Generated passwords (C7)

- A `password` setting may say `default: generate`. The Console fills it at install with 32 random letters and digits (about 190 bits; letters and digits because apps such as Immich take nothing else in a database password) and offers a copy button. It stays in the Service's settings. Built in step 3, with the first entry that needed it.
- It is an ordinary setting. Records are readable on every Node of the Fleet (Q2, accepted), so a Podman secret would add a per-Node lifecycle (create, rotate, delete, recreate after a move) without protecting anything more.
- Podman secrets are used only where a Compose file's `secrets:` expects a file inside the container.

### Catalog format: Quadlet bundles (C8)

An entry is a folder:

```
entries/immich/
  manifest.yaml          as now, plus components, web.component and source
  server.container       one [Container] template per component
  machine-learning.container
  redis.container
  database.container
  icon.svg               copied from an icon pack (C12)
  compose.yaml           only when converted: the file it came from, unmodified
  CONVERSION.md          only when converted: the report (C10)
```

```yaml
components:
  - name: server
    image: ghcr.io/immich-app/immich-server:v2.7.2@sha256:…
    after: [redis, database]
  - name: database
    image: ghcr.io/immich-app/postgres:14-vectorchord0.4.3@sha256:…
    wait_healthy: true
web:
  component: server
  port: 2283
source:                  # only for converted or imported entries
  from: compose          # compose | casaos | hand
  url: https://github.com/IceWhaleTech/CasaOS-AppStore/tree/main/Apps/Immich
  licence: Apache-2.0
  converted: 2026-10-28
```

- Today's single-file entries (`<id>.container` with `image:` at the top) stay valid, so the four existing entries need no change.
- `maitre catalog-index`, which `make gen` already runs, reads the new layout and refuses an entry whose images lack a digest.
- **Known to work** means three checks, run in CI on every changed entry. First, the index builds. Second, every bundle passes the Node's policy check and Quadlet dry run inside the `maitre-server` image, which has Quadlet. Third, the boot test VM installs the entry and waits for it to be healthy. The third check is the expensive one, so it runs only for entries the change touches.

### User Catalogs (C9)

- A Catalog is a folder of entries in the format above. The project's Catalog is one, built into the Console as now.
- A user adds a Catalog in the Console from one of two places: a folder on their computer, or a git repository the Console clones and pulls. The list is a Fleet record signed by a Trusted Device, so every Console of the Fleet sees the same Catalogs. Each Console reads the entries itself, with the bundled `maitre` binary building the index.
- Entries show which Catalog they come from. Installing one works exactly as for the project's Catalog, grants included. A user Catalog's bundle gets no trust the Node's own check doesn't give it.
- A git Catalog follows its branch (Q4). The Console pulls it when it opens the Catalog page and shows, per Catalog, the entries added, changed or removed since the user last looked, with the diff of each changed bundle. Installed Services are not touched. A changed entry reaches a Node only when someone installs or updates from it, and the Node's own check still applies.
- Refreshing the project's Catalog over HTTPS stays deferred, as before.

### Converting when a Compose file is added (C10)

One command does the conversion for staff and users alike. The Console calls it through the bundled binary.

```
maitre catalog add <catalog-folder> <compose.yaml | casaos-app-folder>
```

1. compose-go loads the file, and compose2quadlet produces units (comquad, MIT, pinned version). Maître's adapter turns them into components. It names them, makes volumes and relative bind mounts into Service volumes, lifts `${VAR}` placeholders into settings, and replaces compose2quadlet's `ExecStartPre` health-wait loop with `Notify=healthy` on the dependency, so it does not need a grant.
2. Every image tag is resolved to its digest with go-containerregistry (Apache-2.0, works on every Desktop platform).
3. The bundle is checked: the grant check lists options that will ask for grants, and a Node's Quadlet dry run catches anything Podman would refuse. Staff run this in CI. In the Console, a new optional Node call, `CheckService`, runs the same check without installing. A Node that lacks the call gets the local checks only, and the Console says the Node should be updated for the full check.
4. The report is shown at once and saved as `CONVERSION.md` beside the bundle. It has three levels:

   | Level | Examples | Effect |
   |---|---|---|
   | Must fix | `build:`, `network_mode: service:x`, an image that cannot be resolved, a key Quadlet refuses | no bundle is written |
   | Needs a person | which component is the web page when it is not clear, an absolute bind mount to point at a folder, protected options that will ask for grants | bundle written, marked unfinished, not installable until resolved |
   | Changed for you | networks replaced by the Service's own, the health-wait loop replaced, tags pinned to digests | bundle written, listed for review |

5. For an entry imported from CasaOS, `x-casaos` fills the manifest: title, tagline, description, category, main component, web port and path, and per-setting labels.

Staff then edit the bundle as needed and test it like any other entry. Re-running the conversion later is only needed by choice, because the bundle, not the Compose file, is the entry.

### Other projects' catalogs, occasionally (C11)

- Usable sources: the CasaOS AppStore (Apache-2.0) and Big Bear CasaOS (MIT). Runtipi's store is GPL-3.0 and can't go into an MIT Catalog. Umbrel's has no licence and can't be used at all.
- An import is `maitre catalog add` run over a chosen set of apps. The resulting entries record their source and licence, and the Catalog's `NOTICE` file carries the copyright notices those licences require. Apache-2.0 also asks that changed files say they were changed, which `CONVERSION.md` does.
- Nothing follows those stores afterwards. An entry changes only when someone changes it.

### Icons (C12)

- Icons are copied into each entry as `icon.svg` (or `.png`) when it is added, not linked, so the Console never depends on another project's server.
- Preferred source: homarr-labs/dashboard-icons (Apache-2.0). Second: selfh.st/icons (CC-BY-4.0), which requires attribution.
- `source.icon` in the manifest records the pack and licence. The Console's About screen gets a Credits section listing both packs, and the Catalog's `NOTICE` lists every copied icon.

### Keeping images current (C13)

- `maitre catalog outdated <catalog-folder>` looks up each pinned tag's current digest, and a newer tag in the same series, with go-containerregistry. It lists what moved, and with `--write` it repins.
- The project's Catalog runs it on a schedule in CI and opens a pull request. The pull request then goes through the same three checks as any change. This is the same lookup code the conversion already needs, so no outside service is involved.

## What a tag and a digest are

An image reference such as `jellyfin/jellyfin:10.10.7` names a tag. A tag is a label the publisher can move: tomorrow `10.10.7` can point at a rebuilt image. A digest such as `sha256:7ae36a…` is the hash of the image's manifest. It names exactly one set of bytes and can never point anywhere else.

Pinning means storing both, as `jellyfin/jellyfin:10.10.7@sha256:7ae36a…`. The tag tells people which version it is, and the digest makes every Node run exactly the image that was tested. Updating is then a deliberate act: someone moves the pin and the entry is tested again. Maître already pins every Catalog image and every `SetImage`.

## Steps

Each step ships on its own, keeps single-container Services exactly as they are, and ends with `make lint`, the Go and Console tests, and the boot test in CI (the image job only runs there).

0. **Units through Podman's parser** (C6): the copied parser, rendering into a parsed unit and writing it back, the grant check reading parsed values, and the Quadlet dry run. Existing single-container Services render to equivalent units.
1. **Components on the Node** (C1-C5, C2's internal networks). Proto changes regenerated for Go and TypeScript, the bundle writer, target and per-Service networks, and Control, Status, Logs, Update, AutoUpdate, Rollback, SetImage, EnforceGrants and uninstall over components. Tested with a fake Runner, plus a two-component Service in the boot test where one waits for the other's health.
2. **Console for components**: a component list in a Service's open row, logs per component, Change image per component, grants per component, and the feature-flag fallback. Demo Fleet fixtures and UI tests.
3. **Catalog bundles** (C8): the new manifest fields, `catalog-index` for both layouts, and the three CI checks. First multi-container entry written by hand: Immich.
4. **Conversion** (C10; C7 came with step 3). Values from a Compose file have `$` and `%` doubled, since Quadlet copies them into `ExecStart` where systemd would expand them; setting values are escaped the same way when a Node renders any template. A missing `env_file` is read as empty, CasaOS's `/DATA/AppData/<app>` folders become Service volumes and its other `/DATA` folders shared ones. the adapter over compose2quadlet, digest resolution, the report, `CheckService`, `maitre catalog add`, and a test corpus of unmodified upstream Compose files with golden bundles.
5. **Icons and credits** (C12), and the first occasional import (C11). Built: `maitre catalog icon`, icons inlined in the index, `catalog-notice` and `LICENSES/`, Credits in the Console's Preferences. Imported Vaultwarden, Navidrome, Pi-hole, Syncthing and Nextcloud from the CasaOS AppStore and Paperless-ngx from Big Bear, each edited by hand where its CONVERSION.md says; Forgejo written here, since neither store has it. The converter learned from the import: a password written into the file in two places becomes one made-up setting, a placeholder such as change-me is made up, and a password written once is left for a person. Suggested first batch: Vaultwarden, Navidrome, Pi-hole, Syncthing, Forgejo, Paperless-ngx, Nextcloud.
6. **User Catalogs in the Console** (C9): adding a folder or git repository, the Fleet record, entries labelled by Catalog, what changed since the last look, and Add to Catalog with the report shown in place. Built: the `catalogs` record (a Trusted Device's, Fleet-wide), `maitre catalog sync` (go-git, so no git install is needed) and `catalog-index --lenient` run by the Console's `catalog_load`, a folder Catalog read only on the computer that added it, `CheckService` after Add a Compose file, and the changes kept per Console in its own storage.
7. **`maitre catalog outdated`** (C13) and its scheduled CI job.

ADR 0009 would record C1-C3, C6, C8 and C9: a new Service model, units handled as Quadlet handles them, and Catalogs as tested bundles from more than one source. Signing of bundles and deploy commands gets its own decision when it is designed. The rest go in the decision log.

## Built, reused, refused

| Piece | Source |
|---|---|
| Compose loading | compose-go v2, imported |
| Compose to Quadlet mapping | compose2quadlet from comquad (MIT), imported at a pinned version |
| Adapter to Maître's components, health-wait replacement, report | written here |
| Unit file parsing | podman `pkg/systemd/parser`, copied (standard library only) |
| Quadlet validation | the Node's `/usr/libexec/podman/quadlet -dryrun`, run on the Node or in the `maitre-server` image in CI |
| Digests and newer tags | go-containerregistry, imported |
| Running, ordering, health gating | systemd and Quadlet on the Node |
| Icons | dashboard-icons (Apache-2.0) and selfh.st (CC-BY-4.0), copied with credit |
| Occasional app imports | CasaOS AppStore (Apache-2.0) and Big Bear CasaOS (MIT), converted once |
| Component model, network layout, lifecycle target, Catalog sources | written here |
| Console screens | written here |
| podlet, podman-compose, Docker's compose CLI, Kompose | not used: a second toolchain, no library, no systemd supervision, or pods instead of containers |
| Renovate, Runtipi and Umbrel stores | not used: an outside service, or a licence the Catalog can't take |

## Questions

None open. Q1-Q4 were answered on 2026-10-28 (see Decided by the user).
