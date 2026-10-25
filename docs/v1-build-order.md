# Maître v1 build order

Status: draft, 2026-10-23. Scope is the v1 cut agreed in the design session (Q64). Terms are as defined in `GLOSSARY.md`; decisions are in `docs/adr/`.

The order follows dependencies, not importance. Each milestone ends in something you can demo, and nothing in a later milestone is needed to demo an earlier one. Milestones are sequential; the work inside a milestone can be parallel.

## Principles that shape the order

- **A Node that boots and pairs comes first.** Everything else is layered on a machine that can be installed from the Console and trusted by it.
- **Multi-Node before cross-Node storage.** Spanning Tiers and NFS need the sync layer working across Nodes, so the Fleet milestone precedes the storage milestone.
- **Dev loop without bootc.** The agent runs in a "dev mode" on any Linux box (or a plain Fedora VM) so day-to-day work does not need an image rebuild. The real image is exercised in QEMU in CI from Milestone 1 on.
- **No feature lands without its guard.** Destructive storage actions ship with the "show what you lose, type the name" guard in the same change, never after.

## Repository layout

```
/server/        Go agent (module: one binary, subcommands: agent, installer, dev)
/image/         Containerfile and bootc install config for Maître Server
/desktop/       Tauri app (Rust shell, TypeScript UI)
/api/           Protobuf schema; generated Go and TypeScript are committed
/docs/          ADRs, this plan
```

The Catalog is a separate repository, github.com/Onfire7/maitre-catalog, MIT licensed, included here as a submodule.

## Milestone 0: Skeleton and dev loop

Goal: both halves build in CI, talk to each other over mTLS on a developer machine, and the image builds and boots in QEMU.

- Go module, Tauri scaffold, Protobuf schema with Connect (works over HTTP/2 with mutual TLS, generates both sides).
- Agent `dev` subcommand: runs on any Linux host with Podman installed, state in a local directory.
- Image: Fedora bootc base, plus podman, mergerfs, nfs-utils, avahi or Go-side mDNS, the agent, and a systemd unit for it. Builds in CI, boots in QEMU in CI, agent comes up.
- CI: GitHub Actions. Linux runners for Go, image, and QEMU; macOS and Windows runners for Tauri builds.

Demo: `make dev` on a laptop, Console connects to the local dev agent over mTLS with a hard-coded trust list.

## Milestone 1: A Node boots and pairs

Goal: a blank machine becomes a Node of a Fleet using only the Console and a pairing code on the machine's screen.

- Console first run: create a Fleet, generate the device key, show the Recovery Phrase.
- Installer mode: the image boots from USB into the agent's `installer` subcommand, which advertises on mDNS and shows a status screen (network state, pairing code) on the console. No TUI installer.
- Console: discover the Installer, name it, pick the OS disk, confirm the pairing code. The agent runs `bootc install to-disk` with the three-partition layout (OS, config, optional data), writes the Node key and Fleet identity to the config partition, reboots.
- Installed Node: agent starts, loads identity, advertises on mDNS, accepts mTLS only from keys in the trust list.
- SQLite schema and the ownership sync layer in single-Node form: record, owner, version, timestamp, and the Console's forward-to-owner write path. Multi-Node exchange comes in Milestone 3, but the record format is final here.
- USB as OS disk is supported from this milestone; data partition is off by default on USB.

Demo: boot the Installer in QEMU with a blank disk, provision it from the Console, reboot, see the Node online in the Console.

## Milestone 2: Services run on one Node

Goal: install a Catalog entry from the Console and open it in the built-in browser by name.

- Catalog repository with the manifest format: Quadlet files plus manifest (name, icon, description, volumes, exposure default, has own login, user-editable settings, infrastructure flag, declared host ports, health check). Start with three entries that cover the shapes: Jellyfin (media, big volume), Uptime Kuma (tiny, own login), AdGuard Home (Infrastructure, host ports).
- Agent: fetch Catalog index, create a Service, write Quadlet units, start, stop, remove, status, log tail, restart count. Catalog images pinned by digest.
- Default Tier (Local placement) with a single-Drive Pool on the data partition, mounted through MergerFS. Volumes at `<pool>/<service>/<volume>`.
- Embedded Caddy: routes `<service>.local` to the Service. mDNS publishes the names. Private exposure only in this milestone.
- Console: Catalog browser, install flow with settings form, Service page with status, logs, and the built-in browser tab.
- Observability v1: Node gauges (CPU, memory, Pool capacity, network, temperature if available).

Demo: install Jellyfin from the Console, open `jellyfin.local` inside the Console.

Open question to resolve in this milestone: how the Console's webview trusts the TLS certificate for Private Services. Caddy's internal CA can issue them, but getting a Tauri webview to trust a custom CA is awkward on all three platforms. Preferred answer: the Console runs a loopback proxy that tunnels Service traffic over the existing mTLS channel, so the webview talks plain HTTP to localhost. Decide here because it affects how Caddy is configured for Private.

## Milestone 3: Fleet with more than one Node

Goal: a second machine joins, the Console can connect to any Node, and every Node holds the full Fleet state.

- Pair a second Installer into an existing Fleet from the Console. Nodes discover each other on mDNS and open mTLS connections both ways.
- Sync layer multi-Node: "what do you have newer than X" exchange, newest-wins for Fleet-wide records, queueing of writes to an offline owner, "changed on another device" notice in the Console.
- Console: connect to any reachable Node; reads local, writes forwarded. Service placement: choose which Node runs a Service at install time.
- Trust hardening: approve a second Trusted Device from the first; Recovery Phrase path; Recovery boot mode entry with its 15-minute window.
- Config partition backup to every Pool on the Node on each change; Installer scans Drives for a backup and offers "Restore this Node" before anything else.
- Infrastructure Services: installing one reserves the Node's current address as static, starts the Service before network-online, and the agent uses remembered peer addresses and mDNS, never DNS, for Fleet peers.

Demo: two Nodes in QEMU; stop one, edit its Services from the Console, watch the edit apply when it returns. Reinstall a Node from USB and restore it from a backup on its data Drive.

## Milestone 4: Storage

Goal: Drives, Tiers, Pools, and cross-Node data as designed, with the destructive-action guard everywhere.

- Drive detection with automatic tags (ssd, hdd, usb, size, Node) and user tags. Adopt (ext4, btrfs, xfs, NTFS, exFAT read-write) and Format (btrfs), with the contents summary shown before Format.
- Tiers: rules by tags and Nodes, Placement Local or Spanning. Synced is present as a value but behaves as single-Node until the storage release after v1.
- Pools: agent writes a MergerFS unit per Tier per Node from the matching Drives. Adding a Drive updates the branches, never the mount point.
- Cross-Node: every Pool is exported over NFS to Fleet peers only; a Node without a local Pool for a Tier mounts a peer's over soft NFS with timeouts. Spanning Tiers merge local Drives and remote Pools, with local-first file creation.
- Guard: removing a Drive from a Tier, Format, deleting Service data, and removing a Node all show exactly what becomes unavailable or lost and require typing the name.
- USB-OS Nodes: provisioning requires at least one Drive adopted or formatted, or the Fleet having a Pool elsewhere, before it completes.

Demo: a Node with no Drives runs a Service whose data lives on another Node's Pool. A media Spanning Tier across two Nodes keeps serving local files when the other Node is off.

## Milestone 5: Remote and Public

Goal: the Fleet is reachable from outside the house without being on the open internet, and a Service can be made Public without network knowledge.

- Tailscale via tsnet in the agent. Console walks through one Tailscale login, stores an OAuth client secret scoped to auth-key creation in the Fleet database, and every Node enrolls itself. Per-Node login link as fallback.
- Remote exposure becomes the default for new Services. The Console connects over the tailnet when the LAN is not reachable.
- Public via Funnel: zero-setup path. Public via Direct: user's own domain, ACME certificates through Caddy, guided check that ports are reachable.
- Public confirmation dialog with the plain-language warning. Infrastructure Services and Services declaring host ports are not offered Funnel.

Demo: from a phone's hotspot, open the Console, reach a Remote Service, then share a Public Funnel link to Uptime Kuma.

## Milestone 6: Updates

Goal: Nodes and Services update themselves and roll back on failure, with a staged rollout across the Fleet.

- CI publishes Maître Server images to GHCR on tag. Sigstore signing is attempted here; if it blocks, it slips without blocking the milestone.
- bootc auto-update with greenboot: health is agent up, Podman up, a peer or the Console reached, and every previously running Service running again. Failure rolls back and the Console shows it.
- Staged rollout: one Node updates first, the rest a day later if it stays healthy. Maintenance window and pause per Fleet.
- Service auto-update: podman auto-update against pinned digests from the Catalog, previous image kept, one-click rollback per Service.

Demo: publish a deliberately broken image, watch a Node roll back and report it, publish a fixed one, watch the staged rollout complete.

## Milestone 7: Release polish

Goal: a stranger can go from download to a running Service without help.

- "Prepare an installer USB" in the Console, downloading the current image and writing it.
- Signed and notarised Tauri builds for macOS and Windows; AppImage and Flatpak for Linux.
- First-run and empty states in the Console, written for a non-technical reader.
- Documentation: what physical access means (no disk encryption), what Remote and Public mean, how to recover a Fleet.
- aarch64 image kept building in CI but not released.

Demo: a fresh laptop, download Maître Desktop, prepare a USB, install a Node, run Jellyfin, open it from outside the house.

## Cross-cutting

- **Testing.** Go unit tests for the sync layer and Tier matching, with property tests for sync convergence. Integration tests run the real image in QEMU with KVM on Linux runners, covering install, pairing, two-Node sync, and rollback. Tauri UI tests only for first run and pairing.
- **Security review points.** End of Milestone 1 (pairing and trust list), end of Milestone 5 (exposure). Both use only standard-library TLS and Tailscale's own library; the review is of how they are wired, not of any custom cryptography.
- **Deferred to after v1**, in roughly this order: mirrored copies and Synced placement, SnapRAID parity on Parity Drives, backups, drive migration, Members and gated access, UPnP, dynamic DNS.

## Decisions made while building (continuing the numbering)

- 66. Pairing code: numeric comparison from both keys, one tap. ADR 0005.
- 67. Recovery Phrase: BIP39 twelve words deriving an Ed25519 key; public half stored recovery-only. ADR 0005.
- 68. Installer media: a raw disk image of the same bootc image with `maitre.installer` and `maitre.image=` kernel arguments. No separate ISO.
- 69. Install networking: Ethernet with DHCP only; the Installer screen says so until it has an address.
- 70. Image source during install: pulled from the registry over the internet (bootc cannot copy the running system). Offline media is deferred.

- 71. Console reaches Private Services through a loopback proxy that tunnels over the agent's mTLS listener (X-Maitre-Service header); Caddy serves plain HTTP on the LAN.
- 72. Node status screens are plain text.
- 73. Catalog is delivered as a generated JSON index, baked into the image and refreshed over HTTPS later.
- 74. Catalog is its own MIT repository, github.com/Onfire7/maitre-catalog, included in the monorepo as a git submodule at `catalog/` so the image bakes a pinned Catalog revision (decided 2026-10-24).

## Open questions to settle before Milestone 2

1. Console webview trust for Private Services (see Milestone 2). Recommended: loopback proxy over mTLS.
2. Console status screen on the Node: plain text from the agent, or a small TUI library. Recommended: plain text, it is read once.
3. Catalog index delivery: git clone on the Node or a generated JSON index fetched over HTTPS. Recommended: JSON index, signed with the same key that signs the image once signing exists.

## Open questions found while building

- 75. `<service>.maitre.local` does not resolve from Linux desktops: nss-mdns's default `mdns_minimal` module rejects names with more than two labels. Decided 2026-10-24: (b) Services are `<name>.local`; the Node publishes those names over mDNS and Caddy routes them. A name clash with another device on the LAN is the user's to avoid.
- 76. Offline install from the media (follow-up to 68 and 70). The installer media is the same image, yet `bootc install` pulls it again from the registry. Recommended: bake an OCI archive of the image into a partition on the installer media (`podman save --format oci-archive`), and have the Installer compare the registry's digest with the baked one; install from the archive when it matches or when the registry is unreachable, pull only when the registry has a newer image. Also removes the need for a registry when installing a locally built image. Built 2026-10-25: `image/image-partition.sh` packs the image's OCI layout into an ext4 partition labelled `maitre-image` (built root-free with mke2fs -d) that `installer-media.sh` appends to the media; the Installer mounts it, compares its manifest digest with the registry's, and runs bootc with `--source-imgref oci:...` when they match or the registry is unreachable, pulling only when the registry is newer. Media installs therefore use the same direct bootc path as netboot; the podman path remains only for media without the partition.
- 77. Local image deployment and Node updates (brought forward from Milestone 6). Recommended: a `maitre image-push` / `make image-push` that runs a registry container on the developer machine and pushes the local build; a NodeService `UpdateImage(ref, insecure_registry)` RPC that runs `bootc switch --apply`; a Console control in the Node view to enter an image reference. Netboot (PXE) for new installs is deferred until there is a second physical machine to test it on. Superseded 2026-10-24: physical test machines exist now, so netboot and updates arrive together as the Depot (79 to 87).
- 78. Catalog knowledge moves to the Desktop (73 revised). The Node's InstallService should take a complete ServiceSpec (image, ports, volumes, environment, health check, web path) instead of a Catalog id, so custom and customised Services need no Node support. The Node keeps nothing from the Catalog but the index baked into the image for the CLI. Agreed 2026-10-24; built 2026-10-25: `InstallServiceRequest.spec` is a complete `ServiceSpec` (quadlet template, image, volumes, web port and path, health path, infrastructure flag, settings, exposure); the Node validates it, picks the loopback port and tiers, and never consults a Catalog. The Console bundles the Catalog index generated from the `catalog/` submodule by `make gen` (`desktop/src/catalog/index.json`). The image still carries the index and `GetCatalog` so `maitre service install` on a terminal keeps working; refresh over HTTPS stays deferred.

Decisions 66 to 73 were confirmed 2026-10-24, with these notes: 68 and 70 stand until 76 is built; 72 adds text art at the end of startup and for the pairing code, a graphical boot logo may come once there is a logo, never a graphical session; 73 is superseded by 78.

### The Depot (decided 2026-10-24 and 2026-10-25)

A Depot is `maitre depot`, one Go binary on the developer's workstation and nothing else at runtime: a read-only OCI pull registry served from an OCI layout directory, HTTP for netboot files, and proxyDHCP plus TFTP. Publishing a build is `skopeo copy` into the Depot directory plus a `podman run` of the image's own squashfs-tools to produce the live installer root. The alternatives weighed were host dnsmasq for the PXE half (less code, two processes, harder to test), containers (registry, dnsmasq, nginx; nothing to write but three daemons on host networking), and a plain OCI archive over HTTP (no registry, but `bootc status` then shows a file path and the image loses its tag and digest identity).

- 79. Netboot serves the image's own `shimx64.efi` (Microsoft-signed) over TFTP, which loads `grubx64.efi` from the same directory, so Secure Boot may stay on or off. Revised 2026-10-25 after a Dell laptop with Secure Boot on rejected GRUB served directly and fell into SupportAssist.
- 80. Test machines have 4 to 16 GB of RAM; the live installer root is a squashfs loaded into RAM by dracut-live.
- 81. Machines are wired. The Depot is `maitre-depot.local` over mDNS for the Console and Nodes; GRUB and the kernel command line use the workstation's IP because the initramfs has no mDNS.
- 82. Updates are staged: `UpdateImage(image, reboot, insecure_registry)` runs `bootc switch` in the background and reboots only when asked; `Rollback(reboot)` and `Reboot` are separate; `GetUpdateStatus` reports bootc's booted, staged and rollback images plus progress. The Console's Update form defaults to "Reboot now".
- 83. The Depot may ask for sudo (or a one-time setcap) to bind DHCP and TFTP ports.
- 84. Option A (single Go binary) chosen over host dnsmasq.
- 85. Depot storage is `~/.maitre/depot` (`images/` OCI layout, `boot/` netboot files).
- 86. The netbooted Installer behaves exactly like the media Installer: same screen, same mDNS advertisement, pairing from the Console, so headless machines install unchanged.
- 87. `scripts/vm-net-setup.sh` enslaves the detected wired NIC to the `maitre0` bridge with NetworkManager instead of creating a host-only shared network.
- 88. Plain-HTTP Depot registries: the Node writes a `containers-registries.conf.d` drop-in marking that host insecure before `bootc switch`; the Console ticks the box automatically for `.local` names, IP addresses and localhost.
- 90. One PXE interface per Depot, the one holding the LAN address; `--interface` overrides it. Agreed 2026-10-25.
- 91. The Console's Update panel lists what `maitre-depot.local` serves (`/api/images`, CORS-open, read-only) as one-click choices next to the free-form field. Agreed 2026-10-25.
- 89. Netboot transport split, found while building: GRUB's HTTP client speaks only to port 80 and has no port syntax, so GRUB loads the kernel and initramfs over TFTP from the server it booted from (the Depot, or QEMU's own TFTP behind NAT), and dracut fetches the squashed root over HTTP from the Depot's port. The live Installer runs with SELinux permissive because a rootless squashfs build carries no labels; bootc labels the installed disk from the image's policy.

### Netboot from the Console (decided 2026-10-25)

- 92. The Desktop bundles the Go `maitre` binary as a sidecar and runs `maitre netboot` elevated (pkexec on Linux, the administrator prompt on macOS, UAC on Windows) rather than reimplementing DHCP and TFTP in Rust. One implementation, a small privileged helper, the app itself stays unprivileged. The helper writes JSON events to a file the app tails and quits when a stop file appears, since elevation on macOS and Windows gives no live stdout.
- 93. Boot files come from CI: the image job publishes a `netboot` bundle (shim, GRUB, kernel, live initramfs, squashed root, `netboot.json` with digests and the image reference), attached to GitHub releases on `v*` tags. The Console downloads it once into its data directory and verifies every digest. Developers use a Depot's `/boot/` as the source instead.
- 94. Netboot is an explicit "Boot a new machine from the network" switch in the Console, elevating when turned on. The helper stops itself after an idle period with no client activity, selectable, default one hour.
- 95. Designed for Linux, macOS and Windows; only Linux is verified in this pass.

### Milestone 1 security review (2026-10-25)

Scope: pairing, trust list, mTLS wiring, identity storage, and the code added since (Depot, netboot helper run elevated, update RPCs, media image source, Service tunnel). One independent read-through plus the author's. Findings and outcomes:

- Fixed: the Console's netboot bundle download took file names from the downloaded manifest and joined them to its data directory; now only the six known boot files are accepted and digests must be well formed.
- Fixed: the Console preferred any host answering as `maitre-depot.local` over the release, and passed its registry to the root helper as an insecure registry. The Depot is now an explicit checkbox naming the host; the release is the default.
- Fixed: `UpdateImage` could mark any registry (including ghcr.io) insecure in a drop-in that outlived the update. Only IP literals, localhost and `.local` names may skip TLS, one fixed file is used, and a secure update removes it. The Installer's `--insecure-registry` has the same rule.
- Fixed: image references are validated and passed after `--` to bootc, so a reference starting with a dash cannot become an option; the netboot helper validates `--image` before it reaches the GRUB template.
- Fixed: the netboot helper served its whole directory (events and stop files included) on every interface; it now serves only the boot files, bound to the LAN address, TFTP too, and stops after 24 hours regardless of activity.
- Added: `InstallerStatus` reports the image, its source and any insecure registry, and the Console shows them before the disk is chosen, so a substituted image on an unsigned netboot can be noticed.
- Design question 96, decided B on 2026-10-25: `Provision` keeps accepting the first caller presenting any key, with the pairing code compared by the person at the Console; the race by another LAN host during the Installer's window is accepted as a home-LAN risk and documented. In return the Console lists every Depot announcing itself (`_maitre-depot._tcp`) and warns when more than one is on the network, and the Depot is only ever used when chosen by name.
- Accepted for now: plain-HTTP Depot for developers; no image or boot-file signing until Milestone 6; `Infrastructure` and other LAN exposure is unchanged.
- Checked and fine: registry and TFTP path handling, no shell interpolation of caller input, every Node RPC and the Service tunnel sit behind the trust-list mTLS listener, key files are 0600, the recovery key derivation is plain SHA-256 over BIP39 entropy with a domain label.

### Node screen and Depot (2026-10-25)

- 72 revised: the Node's screen is a text interface on tty1 with no login (`server/internal/tui`, Bubble Tea): Status (banner, network, discovery, trusted devices, Services, CPU, memory, Pool, network, temperature), Containers (sortable table with CPU and memory from podman stats, Enter opens its logs), Logs (journal of the agent or a Service), `r` reboots after confirmation. The agent owns tty1 with keyboard (`StandardInput=tty`) and logs only to the journal; the kernel's console log level is lowered while the screen runs. `maitre tui --demo` previews it in a terminal. Without a terminal the plain status page is drawn instead.
- 98. `make depot` rebuilds and republishes the image when Enter is pressed (`depot serve --rebuild CMD`), running the build as the invoking user when the Depot is elevated for netboot; the registry keeps serving meanwhile and grub.cfg is refreshed afterwards.

### Milestone 3 progress (2026-10-25)

- Sync layer built (`server/internal/peers`): every Node holds every Record; a write pushes to all peers; every 20 seconds each peer's whole database (kilobytes) is pulled and applied newest-wins, and what the peer lacks is pushed back; a write for a Record another Node owns is forwarded to that Node, or queued in an outbox and delivered when it returns. Node records can be introduced (version 0, owned by the Node described) so a Node trusts and finds peers before meeting them; the Node's own first write supersedes. The trust list now holds Trusted Devices plus every other Node. Peers are found from Node record addresses and mDNS. `make m3-check` runs two Nodes on loopback through join, push, forwarding and the queue. The "what do you have newer than X" exchange of ADR 0003 is replaced by the full pull for now; it is cheap at this size and converges by construction.
- `maitre provision --join-from NODE` joins an existing Fleet: copies its Fleet, devices and Nodes into the new Node and introduces the new Node to the Fleet. Node names must be unique within a Fleet.
- Console join flow and peer display built the same day: adding a Node copies devices and Nodes from an online Node, refuses duplicate names, introduces the newcomer; Nodes that do not know each other are introduced automatically when both answer.
- 102 (built): after every change the agent copies the config partition (key, Fleet database snapshot via VACUUM INTO, meta.json) to `.maitre/backup/<node_id>/` on every mounted Drive, debounced. The Installer mounts each partition of every non-installer disk read-only, lists the backups in `InstallerStatus.backups`, and `ProvisionRequest.restore_node_id` restores one instead of seeding a new identity (refused when the backup sits on the target disk). The Console offers "Restore <name>" before the name and disk step; a restored Node keeps its id. Unit-tested; a live restore needs a second Drive, which arrives with Milestone 4.
- 99, 100, 101 decided and built 2026-10-25 (see below; `make m3-check` covers the phrase and code paths, the join-by-approval path is Console-only). Not yet: "changed on another device" notices, Infrastructure Service address reservation.
- 99. A new Console announces itself over mDNS (`_maitre-device._tcp`) with its key; a trusted Console lists devices asking to join, both show a code derived from the two device keys, Approve writes the Trusted Device to any online Node.
- 100. "Recover with your phrase": the recovery key is recovery-only; Nodes accept it for exactly one call, adding a Trusted Device.
- 101. Recovery is chosen at the machine's keyboard: the reboot question offers "reboot into recovery", which boots with networking off, shows a one-time code, and on the next normal boot honours that code for 15 minutes (a device presenting it with its key is added as a Trusted Device, on a dedicated listener that accepts any client certificate for that one call). Nothing on the network can request recovery.

