# Popularity, and Catalog entries that need grants

Status: built (2026-10-29), except Bluetooth (G6) and devices other than GPUs, which wait for SELinux work checked on a Node. Follows docs/services-from-compose.md.

Two additions to the Catalog. The Catalog page can sort by an approximation of popularity. And entries may need grants (decision 189): an entry declares each protected option it needs and why, the Console asks before installing, and the person can install it fully, without its optional features, or not at all. This opens Home Assistant, Plex, Homarr, Dozzle and others that the "no grants" rule kept out.

## Popularity

### P1. The metric: stars of the app's own project

Each entry names its project: the repository the app is developed in.

```yaml
project: https://github.com/home-assistant/core
```

Popularity is that project's star count. Stars are chosen over registry pull counts because:

- They exist for every entry. Docker Hub has a public pull count, but GHCR has no API for its download counts, and Codeberg's registry has none at all. GitHub, Codeberg (Forgejo/Gitea's API) and GitLab all report stars.
- Pull counts measure machines, not people. CI systems and auto-updaters pull the same image over and over, and old images accumulate billions (Nextcloud, Postgres). Stars are people saying they care.

Stars are an approximation and are labelled as such: the Console says "by popularity (stars of the project)".

### P2. Where the numbers live

`popularity.json` sits beside `entries/` in a Catalog:

```json
{"checked": "2026-10-29", "stars": {"home-assistant": 81234, "jellyfin": 41000}}
```

It is kept out of the manifests on purpose. The numbers change every week, and the Console's "what changed since you last looked" compares entries; stars in the manifest would mark every entry as changed every week. A future bundle signature (docs/services-from-compose.md) also has no reason to cover them.

`maitre catalog-index` reads `popularity.json` when it exists and adds `stars` to each entry of the index. A user Catalog without the file still loads, unsorted by popularity.

### P3. Updating them

`maitre catalog popularity [--write] <entries dir>` asks each entry's project host for its stars (GitHub's API with `GITHUB_TOKEN` when set, since anonymous calls are limited to 60 an hour), prints them, and with `--write` rewrites `popularity.json`. The weekly `catalog-outdated` job runs it with the image lookup, so the same pull requests carry both.

Stars are not comparable across hosts: Codeberg's and GitLab's run lower than GitHub's for a project of the same size (Forgejo has 5.7k on Codeberg). This is accepted for now; most entries are on GitHub.

An entry without `project:` has no stars and is reported. `catalog-check` does not fail it: popularity is a nicety, not a correctness rule.

### P4. Filling `project:`

`maitre catalog add` fills it from the image's `org.opencontainers.image.source` label, which most images set to their repository, when that label names GitHub, Codeberg or GitLab. Otherwise it is a note in the report. Existing entries get it once, by hand, from the same label or the project's page.

### P5. The Console

The Catalog page gets a sort control: Popular (the default) or A to Z. Entries without stars go after those with them, alphabetically. Each card shows its stars, rounded ("81k"). User Catalog entries sort with the project's.

## Grants in the Catalog

### G1. Entries declare their grants

```yaml
grants:
  - option: Network=host
    why: Finds devices on your network (Chromecast, HomeKit, smart plugs) by listening for their announcements, and opens port 8123 on the Node's own address.
    optional: discovery
    without: Add devices by their address instead; most integrations still work.
  - option: AddDevice=*
    component: ""                 # in an entry of several, the component that needs it
    why: Talks to a Zigbee or Z-Wave USB stick plugged into the Node.
    optional: radio
    without: No Zigbee or Z-Wave devices.
```

- `option` is a protected option as `services.Protected` words it (`Network=host`, `AddCapability=NET_ADMIN`, `Volume=/run/dbus`). A trailing `*` matches any value, for options whose value comes from a setting (`AddDevice=*`).
- `why` is required: the reason this app needs this power, shown in the warning.
- `optional`, when given, names the feature the grant enables. The template puts the option inside `{{ if .Grants.<name> }}...{{ end }}`, so it can be left out. Several grants can share one feature (Bluetooth needs a D-Bus mount and two capabilities). `without` says what the person loses.
- A grant without `optional` is required: the entry does not run without it.

### G2. `catalog-check` checks declarations and support

`catalog-check` renders each entry twice, with every optional feature on and with every one off, and reads the protected options of each.

- Every protected option with features on must match a declared grant. Otherwise: FAIL, "needs X but does not declare it; add it under grants: with why".
- With features off, only required grants may remain. Otherwise: FAIL, "X is optional (feature) but stays in the unit when the feature is off; put it inside {{ if .Grants.feature }}".
- A declared grant must be used by the units. Otherwise: FAIL, "declares X, which no unit uses".
- Every declared grant must be supported (G3). Otherwise: FAIL, with what Maître needs before it can be.

A device setting (G5) has no value in the check, so the check fills each with a sample path.

### G3. Supported grants, and what the rest need

`server/internal/catalog/grants.go` keeps the table of options Maître runs correctly. Each unsupported kind carries the sentence `catalog-check` prints, naming the support that must be added. The table grows until it covers everything Quadlet can say.

Supported:

| Option | Why it is supported |
|---|---|
| `Network=host` | The Node serves the app's web port as it is (G4). |
| `AddDevice=/dev/dri...` | GPUs: container-selinux lets containers use them (`container_use_dri_devices`, on by default). A fixed path, or a device setting (G5). |
| `AddCapability=<cap>` | Podman applies it; the warning names the capability. |
| `Sysctl=<key>` | Podman applies it inside the container's namespaces. |
| `Volume=<host path>` | Any folder of the Node, named in the warning, except the two below. |
| `UserNS=host`, `NoNewPrivileges=false` | Podman applies them. |

Unsupported, with what they need:

| Option | Needs |
|---|---|
| `Volume=/run/podman/podman.sock` (and Docker's) | SELinux letting a container use the Podman socket without turning SELinux off for it (`label=disable`, Podman's usual answer), and a warning that says the socket is root on the Node. The Node already runs `podman.socket`. Dozzle and Homarr's container view wait for it. |
| `Volume=/run/dbus` | Bluetooth (G6). |
| `AddDevice=` other devices (serial sticks, `/dev/video*`) | SELinux letting a container use the device: container-selinux's `container_use_devices` boolean, off on Fedora, or a narrower rule, set in the Node image and checked on a Node with the device plugged in. Home Assistant's Zigbee and Z-Wave sticks wait for it. |
| `Network=container:...`, `Network=ns:...` | Maître to manage the other container's lifecycle with this one. |
| `PodmanArgs=...`, `GlobalArgs=...` | Nothing in Maître can check raw arguments; ask for the specific capability, device or folder instead, or teach `protect.go` the Quadlet key that says it. |
| `SecurityLabelDisable`, `SeccompProfile`, `SecurityLabel*` | A design for weakening SELinux or seccomp per Service. |
| `[Service]`, `[Unit]` and other sections | A design for Node-side commands; they run as root outside any container. |
| Any key `protect.go` does not know | Classifying the key in `protect.go`. |

### G4. Host networking on the Node

A Service whose web container uses `Network=host` listens on the Node's own address, not on a loopback port Maître publishes. At install and on every re-render, the Node:

- sets the Service's web host port to its web port, so Caddy, Tailscale and the health probe reach `127.0.0.1:<web port>` as before;
- refuses the install when another Service's web host port or host ports already hold that port, naming it;
- writes no `PublishPort=` (Podman discards it under host networking) and no `HostName=` (already the case).

The app is then also reachable on the home network at the Node's address and that port, outside Maître's private and remote exposure. The grant's `why` must say so, and the Console's note for `Network=host` says it too.

### G5. A device setting

```yaml
settings:
  - key: ZIGBEE_DEVICE
    label: Zigbee or Z-Wave stick
    type: device
    device: serial                # serial | video | any
```

- The Console offers the target Node's devices of that kind, from a new `ListDevices` call: serial devices by their stable `/dev/serial/by-id/` names (else `/dev/ttyUSB*`, `/dev/ttyACM*`), video by `/dev/dri/renderD*`. A Node without the call (Unimplemented) gets a text field.
- Empty means none. The value must be a path under `/dev/`.
- The template writes `{{ if .Grants.radio }}{{ with .Settings.ZIGBEE_DEVICE }}AddDevice={{ . }}{{ end }}{{ end }}`, and the grant is `AddDevice=*`.

### G6. Bluetooth (not yet supported)

Home Assistant uses Bluetooth through the host's BlueZ over D-Bus, as its documentation says, with `/run/dbus` mounted. That needs two things the Node does not have yet: BlueZ (`bluetooth.service`) in the image, and SELinux letting a container connect to the Node's system bus (`container_t` to `system_dbusd_t`), which Fedora's policy is expected to refuse. Both must be checked on a real Node. Until then `Volume=/run/dbus` is unsupported, and `catalog-check` says what it needs; Home Assistant ships without Bluetooth.

### G7. Generated secrets in other shapes

```yaml
  - key: SECRET_ENCRYPTION_KEY
    type: password
    default: generate
    format: hex                   # alnum (default) | hex | base64
    bytes: 32                     # random bytes for hex and base64; alnum is always 32 characters
```

Homarr wants 64 hex characters; Laravel apps want `base64:` and 32 random bytes, written in the template as `APP_KEY=base64:{{ .Settings.APP_KEY }}`. Go's `GenerateSecret` and the Console's `generateSecret` make the same shapes.

### G8. Asking before installing

Today the Node refuses an install that needs grants, and the Console then shows a warning naming each option. For an entry that declares grants, the Console asks first, with the entry's words:

- Each option is shown with the entry's `why`, and Maître's own note on what the option allows. Options are grouped by feature.
- Each optional feature has its own switch, on until the person turns it off, with what is lost without it.
- The buttons are **Don't install** and **Allow and install**, which becomes **Install without** *features* once a switch is off.
- Allow signs a grant for every option and installs. Without installs with those features off (`optional_grants` in the spec, G9), signing grants only for the options still needed. Don't install stops.
- `maitre service install --allow [--without features]` does the same from the command line, for the boot test.

The Node still enforces the grants, so nothing changes for safety. If an option appears that the entry did not declare, the Console falls back to the existing warning.

### G9. Features in the Service spec

`ServiceSpec.optional_grants` (map of feature to on or off) is filled from the entry by `specFor` and `SpecFor`, every feature on by default. The Node gives templates `.Grants` from it, and keeps it across updates, rollbacks and image changes. A Node older than this field fails such an entry's template with an error that names `.Grants`. The Console falls back on that, as on any older Node.

## Steps

Each ends with the Go and Console tests, `make lint`, and `catalog-check` over the Catalog.

1. **Popularity** (P1-P5): `project:` in the format, `catalog popularity`, `stars` in the index, the Console's sort, `project:` for the 22 entries, and the weekly job.
2. **Declared grants and the support table** (G1-G3, G9): the format, `catalog-check`'s two renders, `optional_grants` and `.Grants` on the Node.
3. **Host networking** (G4) on the Node.
4. **Device settings** (G5): the `device` type, `ListDevices`, the Console's picker.
5. **Generated secret shapes** (G7). Bluetooth (G6) waits for a Node to check SELinux on.
6. **The Console's question** (G8), on the demo Fleet first.
7. **Home Assistant**, then the other entries left out: Plex, Homarr, Dozzle, PhotoPrism, Wallabag, Linkding. Then batches of ten. Built: Home Assistant (discovery as an optional feature; no Bluetooth or radio sticks until G6 and the device SELinux work). Batch 2: Plex (discovery and GPU transcoding as optional features, a claim token without discovery; no stars, since it is not developed in public), Homarr (its key made up in hex; no Docker socket), PhotoPrism, Wallabag, linkding (by hand), Sonarr, Radarr, Prowlarr, qBittorrent and Jellyseerr. Dozzle waits for the Podman socket. Batch 3: Mosquitto (headless, MQTT on the Node's port 1883, without passwords), Node-RED (on its own network), ESPHome (discovery optional, ping without it), n8n, Gotify, Changedetection.io, Kavita, Bazarr, Lidarr and code-server. Batch 5: Dashy, Duplicati, Emby (GPU transcoding optional), Excalidraw, File Browser, Heimdall, Joplin Server, Vikunja, Trilium Notes and LibreSpeed; Kiwix, Kopia and SABnzbd left out (docs/catalog-features.md). It taught the converter to drop the comma left after a read-only host mount, and the icon lookup to use GITHUB_TOKEN. Batch 4: Open WebUI, Ollama (on the processor), Grafana, BookStack, Wiki.js (SQLite), Tandoor Recipes, Grocy, Komga, Tautulli and Transmission; it taught the converter to read an upper-case port protocol (51413/UDP). Batch 2 taught the converter that a service on the host's network takes CasaOS's port as its page and gets no network of Maître's beside the host's, and that a mount Maître cannot support says what it needs.

## Where the batches stopped

After batch 3 (43 entries) the next most asked-for apps all need something `catalog-check` reports as unsupported, all of it SELinux work to be checked on a real Node:

- serial devices (`container_use_devices` or a narrower rule): Zigbee2MQTT, Z-Wave JS UI, Home Assistant's radio sticks, ESPHome's USB flashing from the Node;
- `/dev/net/tun` (the same rule): WireGuard, Tailscale, Gluetun and other VPN containers;
- the Podman socket: Dozzle, Portainer, Homepage's and Homarr's container views;
- Bluetooth over the system bus: Home Assistant's Bluetooth.

None of the 31 entries added since the first twelve has been installed on a Node yet: the boot test installs each changed entry in CI, one at a time.
