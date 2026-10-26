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
- 99, 100, 101 decided and built 2026-10-25 (see below; `make m3-check` covers the phrase and code paths, the join-by-approval path is Console-only). Built the same day: installing an Infrastructure Service pins the Node's current IPv4 address, gateway and DNS as static through NetworkManager (the install fails if that cannot be done); the Console's Services panel shows "Changed from another device" when a Service record's version moved without this Console writing it. Milestone 3 is complete except for a live two-machine run of the Console join flow.
- 99. A new Console announces itself over mDNS (`_maitre-device._tcp`) with its key; a trusted Console lists devices asking to join, both show a code derived from the two device keys, Approve writes the Trusted Device to any online Node.
- 100. "Recover with your phrase": the recovery key is recovery-only; Nodes accept it for exactly one call, adding a Trusted Device.
- 101. Recovery is chosen at the machine's keyboard: the reboot question offers "reboot into recovery", which boots with networking off, shows a one-time code, and on the next normal boot honours that code for 15 minutes (a device presenting it with its key is added as a Trusted Device, on a dedicated listener that accepts any client certificate for that one call). Nothing on the network can request recovery.


### Milestone 4 progress (2026-10-25)

- Drives (`server/internal/storage`): detection by lsblk with automatic tags (ssd/hdd, usb/nvme, size class), Adopt keeps the filesystem (ext4, xfs, btrfs, NTFS via ntfs3, exFAT) and mounts by UUID, Format wipes to btrfs behind a typed-model guard, Remove leaves data in place. Drive records are owned by the Node and synced. A USB stick as the only disk must keep its data partition unless the Fleet already has a Node.
- Tiers (`tier` records, Fleet-wide): required tags, optional Nodes, priority; a Drive belongs to the highest-priority matching Tier, "default" (created by the agent) takes the rest. Each Tier with Drives on a Node gets a mergerfs Pool at `/var/mnt/pools/<tier>`; branch changes are applied in place through the mergerfs xattr. Service volumes carry a Tier each, chosen in the install dialog (107); a volume whose Tier has no Pool on the Node refuses to install there.
- 103 (UUID identity), 104 (rules), 107 (per-volume Tier) built on the plan's wording; 105 (NFS trust) and 106 (spanning placement) wait for answers. The guard is complete: Format repeats the disk's model, removing a Drive repeats its name, deleting a Service's data lists the volumes and repeats the Service's name, removing a Node from the Fleet lists the Services that become unavailable and repeats the Node's name (a tombstone in the Node's own name that every peer applies). 105 and 106 agreed and built 2026-10-25: every Node exports its Pools over NFSv4 to the Fleet's other Nodes' addresses only (`/etc/exports.d/maitre.exports`, fsid per Tier, mergerfs mounted with allow_other); a Spanning Tier's Pool on a Node is mergerfs over its own Drives first, then peers' Pools mounted soft (`soft,timeo=30,retrans=2`, 20-second mount timeout) at `/var/mnt/remote/<node>/<tier>`, with `category.create=ff` so new files land locally while there is room; a peer that is off makes its files absent. The Tiers panel previews which Drives a rule takes as it is typed (104) and lists each Tier's Drives per Node. Still after v1: Parity Drives.
- 104 continued (2026-10-25): the Tiers panel opens with a map of the Fleet, one rounded box per Node holding a rounded box per Drive, coloured by the Tier that claims it; pointing at a Tier, or typing a rule, lights the Drives it takes. Each Node holds one letter (the first free one, chosen by the Node itself and kept in its Node record; the younger of two Nodes that collide yields) and its Drives are named from a list for that letter (Apollo, Atlas, Aurora… for A; Beacon, Birch… for B; `server/internal/storage/names.go`), so a Drive's name says where it sits. The Adopt and Format dialogs propose the next name; the user can still type another. The NFS exposure (any LAN device able to spoof a peer address can read Pool data) is accepted for v1; a WireGuard tunnel between Nodes is the later fix.

### Milestone 5 progress (2026-10-25)

- Groundwork: `server/internal/tailnet` wraps tsnet. The Fleet-wide `tailscale` Record holds an OAuth client (id, secret, tag) entered once in the Console; each Node makes itself a single-use pre-authorised auth key through it and joins the tailnet as a tsnet node named after the Node, with state on the config partition. The agent's mTLS listener is served on the tailnet too and the tailnet address joins the Node record, so the Console reaches the Node away from home through the user's own Tailscale client. Without an OAuth client the Node reports a login URL instead. `--no-tailscale` keeps a Node off. Tier and Tailscale records are Fleet-wide (newest-wins).
- The agent binary grows to about 55 MB with tsnet.
- 109 to 111 built on the recommendations (2026-10-25): every Remote or Public Service runs its own tsnet node named after it (`server/internal/remote`), serving HTTPS with Tailscale's certificate at `https://<name>.<tailnet>.ts.net` and proxying to the Service's loopback port; Public adds Funnel. `SetExposure` switches a Service between private, remote and public; Infrastructure Services never go public. The Public choice carries the plain-language warning and needs an acknowledgement. Untested against a real tailnet yet.
- 110 answered 2026-10-25: Services stay at home only until the user changes them, so no Tailscale hostname is taken without being asked; the install dialog now defaults to private whatever the tailnet state.
- 109 (Tailscale's free Personal plan, checked 2026-10-25): $0 for non-commercial use, up to six users, user devices unlimited, up to 50 tagged resources. Every Node and every Remote or Public Service is a tagged resource (the OAuth client tags them), so the budget is roughly 50 Nodes plus exposed Services; 110's default keeps that from being spent by accident. Funnel is included.
- 112 built 2026-10-25, "Public, at your own address" (exposure `direct`, `ServiceSpec.public_host`): Caddy on the Node gets an HTTPS server on :443 for every such host with automatic HTTPS left on, so it obtains and renews a Let's Encrypt certificate itself (HTTP-01 answered on the existing :80 server, which also redirects those hosts to HTTPS; TLS-ALPN-01 on :443). The agent learns the Node's public address from a public IP service (api.ipify.org, then checkip.amazonaws.com, then icanhazip.com; hourly, only while needed) and reports it in `NodeInfo.public_ip`; the exposure dialog tells the user the A record to make and the two ports to forward. `server/internal/direct` watches each host and the Service line shows the step reached: no DNS yet, DNS pointing elsewhere, waiting for a certificate (ports not reachable), up. A certificate in hand proves the internet reached the Node, which is the plan's "guided check that ports are reachable". No Tailscale needed for this path. Reserved suffixes (.local, .lan, .internal, .home.arpa, .localhost) are refused. Open: dynamic DNS updates when the home address changes (113), ACME account email (114), router port mapping by UPnP (115); recommendations in the session.

### Milestone 6 progress (2026-10-25)

- greenboot is in the image with a required check (`image/greenboot/10-maitre.sh`): agent active and accepting connections, podman working, every Maître Service that was active before the update active again (the agent lists them in `/var/lib/maitre/running-services` before rebooting into an update). Repeated failures roll the image back; the red script leaves `/var/lib/maitre/last-rollback`, shown on the Console's Update panel.
- Services: `ACTION_UPDATE` pulls the newest image behind the tag, pins the new digest and keeps the old one in `previous_image`; `ACTION_ROLLBACK` swaps them. `auto_update` (chosen at install, default on) runs Update daily. Quadlet's own AutoUpdate is off so the pin stays ours.
- Staged rollout (`server/internal/rollout`): a Fleet-wide `update_policy` (automatic, window hours, stage hours, paused). Each Node records its booted image reference, digest and boot time in its Node record. Hourly, inside the window, the first Node by id takes a newer registry digest; the others follow once the first has run it for the stage period. The decision is shown on the Update panel. The policy lives on the Console's home page.
- Sigstore: CI signs every published image by digest with the workflow's GitHub identity (cosign keyless, logged in Rekor), as a warning-only step so a signing outage never blocks a build. Nodes do not verify signatures yet: enforcing a `sigstoreSigned` policy for ghcr.io on bootc needs the Fulcio root and Rekor key pinned in the image and an exemption for Depots, which is the follow-up. A live rollback test waits for a deliberately broken image at the first release.

### Milestone 7 progress (2026-10-25)

- "Prepare an installer USB" in the Console: downloads `maitre-installer.img.xz` from the latest release once, lists removable disks through the bundled helper, and writes the image elevated (`maitre media write`, decompressing on the fly, unmounting the target first, with progress events) after the disk's model is typed back. CI now produces the raw xz image alongside the qcow2 and attaches it to releases.
- aarch64: CI builds the image on an arm64 runner and pushes `main-arm64`; not boot-tested, not released.
- Not yet: signed and notarised Desktop builds (need Apple and Microsoft credentials from the project owner), first-run and empty-state copy pass, user documentation, a `v0.1.0` tag to create the first release (question 97).

### Decisions 108, 113-115 and the Test2 CPU fix (2026-10-25)

- Test2 at a steady 75% CPU with nothing installed: found over the agent's own RPCs (new `maitre metrics` subcommand reads `GetNodeMetrics`; load 3.05 on four cores) and read out of the code. The three mDNS publishers (Node announcement, Service names, peer browser) shared godbus's one `SystemBus()` connection; an hour after boot the auto-update timer republishes the Service names, which closed that shared connection, which closed every Avahi client's signal channel, and go-avahi v1.1.0's dispatch loop spins on a closed channel (`continue`) forever: one core per client. The Node also stopped announcing itself. Fix: a private D-Bus connection per advertisement or browser, the Avahi client stopped before its connection closes, and a vendored go-avahi (`server/third_party/go-avahi`, replace directive, MAITRE-PATCH.md) whose loop ends when the bus goes away, so an avahi-daemon restart cannot bring the spin back either. Regression tests in `internal/mdns` run against the dev box's Avahi and were checked to fail without the patch. Both test Nodes updated from the Depot and checked past the one-hour mark.
- 108 agreed: TPM-bound recovery codes; built next.
- 113 agreed: dynamic DNS deferred (later, a Cloudflare or DuckDNS token).
- 114 built: `PublicSettings.acme_email` in a Fleet-wide `public_settings` record, optional, edited in the "Public, at your own address" section of the exposure dialog, given to Caddy's ACME issuer (Let's Encrypt sends expiry and policy notices there).
- 115 built: UPnP port forwarding in `server/internal/portmap` (huin/goupnp, which Tailscale already brought in; WANIPConnection2, then 1, then WANPPPConnection1; one-hour leases renewed at half-time, permanent lease retried for old gateways, mappings deleted when unwanted and at shutdown). Two sources of wants, both explicit: a Service whose Catalog entry says `port_forward: true` (so declares `host_ports`) and whose user turned on "Reachable from the internet" (`ServiceSpec.forward_ports`, new `SetPortForwarding` RPC, dialog with the open-to-everyone warning); and 80/443 for Caddy whenever a `direct` Service exists on the Node. `ServiceStatus.forward_state` and `NodeInfo.port_mapping` show the router found and what it accepted or refused; the exposure dialog's step 2 now says the Node asks the router itself. The dev LAN's eero answered the discovery probe (`MAITRE_UPNP_PROBE=1 go test ./internal/portmap -run Live`). The Catalog gained `port_forward` and a Minecraft Java server entry (itzg/minecraft-server pinned, 25565/tcp, EULA as a bool setting) as the first game server. Not yet seen live: a mapping accepted by the eero from a Node.
- Not now: release signing (user: at v1.0.0); USB writer test (no stick at hand).

Open (recommendations):

- 116. NAT-PMP/PCP next to UPnP: Apple and some newer routers speak only those. Recommend: add later behind the same `portmap.Router` interface (jackpal/go-nat-pmp); UPnP first since that is what game-hosting guides assume.
- 117. Should a Service with "Reachable from the internet" also show the public address and port to hand to friends (e.g. `203.0.113.5:25565`) on its line? Recommend: yes, from `NodeInfo.public_ip`; small Console change.
- 118. A Node that has any mapping keeps asking the router every minute while the router is missing. Recommend: keep (cheap SSDP multicast), but back off to five minutes after the first hour.
- Built-in Drive (2026-10-25, user report): the Installer's `maitre-data` partition (the rest of the system disk: 443 GB on Test1, 86 GB on Test2) was listed as "holds the running system", so the Console hid it and no Tier rule could select it; it only backed the default Pool through a hard-coded path in `pool.Up`. Now it is a Drive like any other: `storage.AdoptInternal` records it at agent start and in `maitre pool-up` (origin `internal`, auto tag `internal`, a letter-based name), it mounts at `/var/mnt/drives/<uuid>`, Tier rules see it, the Console shows it as "built in" without a Remove button, and `Forget` refuses it. `pool.Up` no longer special-cases the label. New `maitre drives` subcommand lists a Node's Drives from the dev box.
- Node renaming (2026-10-25, user request): `RenameNode` RPC; the agent rewrites its Node record, pushes it to peers, sets the host name and re-announces over mDNS at once (the tailnet name follows on the next start, since tsnet takes its host name at start). The Console's Node page has Rename… with a dice button, the setup screen draws a name by default; `desktop/src/names.ts` holds the list (trees, stones, a few landscape words), never one the Fleet already uses. `maitre info -rename NAME` from the CLI.
- Three Avahi clients, asked by the user: the Node announcement (`_maitre-node._tcp`, lives for the whole run), the Service host names (`<name>.local` address records, torn down and republished whenever Services change) and the peer browser (watches for other Nodes). Each helper opened its own client because they are created in different places with different lifetimes; that was harmless until the first teardown closed the connection they all shared. Now each has its own connection, which is what Avahi expects; one shared client would save two D-Bus connections and nothing else.
- 119 revisited (user: no fully trusted Console is left in the recovery case, so nobody could promote; and would a changed boot entry not break image signing?). Image signing (cosign on the container image) is checked when the image is pulled; it says nothing about the boot entries, GRUB config or kernel arguments on the boot partition, which root edits freely, and an attacker does not even need to: the genuine recovery entry is itself signed and legitimate, and root's own units in the writable /etc run inside that boot. Closing that needs two things Fedora bootc 42 does not ship yet: a sealed root (bootc's signed composefs, where /usr's digest is part of the signed, measured boot) and a recovery session that runs from the image alone, ignoring /etc and /var. Until then the TPM can bind a code to "a recovery boot happened", not to "the real recovery screen ran". Proposal 120 below.
- 120. Recovery trust with no full device left: make Fleet-wide trust through recovery need physical presence at two Nodes when the Fleet has two or more (recovery code at Node A adds the device for A only; a recovery code at any other Node within 24 hours promotes it Fleet-wide); a one-Node Fleet promotes directly, since there is nothing else to protect. A compromised Node then cannot mint Fleet-wide devices alone. Recommend this now, and the TPM/sealed-image path when bootc's sealed images land (tracked as a v2 item).
- 116 agreed (NAT-PMP/PCP later, behind `portmap.Router`). 117 built: a forwarded Service's row shows `<public ip>:<port>` for each host port once the router accepted the mapping. 118 built: router discovery every pass for the first hour, then every five minutes (test with an injected clock).
- 120 built: `TrustedDevice.node_scope`. In a Fleet of two or more Nodes a recovery code adds the device with `node_scope` = that Node; `peers.TrustedFingerprints` ignores devices scoped to another Node, so only that Node answers it. The same device bringing a code to a second Node within 24 hours (`recoverymode.PromoteWithin`) clears the scope: Fleet-wide. `AuthorizeResponse.node_only` tells the Console; the home page shows a banner naming the trusting Node and the deadline with "Enter a code from another Node" (the code screen outside first run, with promotion wording). A one-Node Fleet promotes directly. The TPM/sealed-image version stays a v2 item until bootc ships sealed images.
- Node names follow the Node's letter (user): `desktop/src/names.ts` has five names per letter, disjoint from the Drive lists; setup computes the letter the new Node will take (first free, same rule as the agent) and says so; the rename dice draws from the Node's own letter.
- Drive names for big servers (user): 24 names per letter (624 in all, disjoint from the Node name lists), and past 24 the list is walked again with a round number ("Apollo 2", "Atlas 2", ... "Apollo 3"), so names stay letter-led and pronounceable for any bay count. The first six of each letter are unchanged, so existing Drives keep their names.
- One name list for Nodes and Drives (user, 2026-10-26): the Node names joined the per-letter lists in `server/internal/storage/names.go` (29 per letter); `maitre names-index` exports them to `desktop/src/names.json` under `make gen` (checked by gen-check), and the Console draws Node names from it. "Used" on both sides is every Node name and every Drive name in the Fleet, so Node A and its Drives share the A list without repeats.
- Boulder's screen and the spanning Media Tier (user, 2026-10-26). Screen: the VT's geometry changes after the agent starts and a service never gets SIGWINCH, so the TUI drew for the boot-time size until a tab switch; it now re-reads the console size every tick and the resulting WindowSizeMsg repaints every line, which also rubs out kernel or systemd lines printed over it; kargs add `systemd.show_status=false` so unit lines stay off the screen. Tier: a Tier with no required tags and no Nodes matched every Drive, so a Media Tier created with the tags field empty took both built-in Drives and the default Pool fell back to the OS partition (metrics said 31 GiB); `TierOf` now treats an empty rule as matching nothing and the Console refuses to create one. The PutRecord error was a timeout: Pool assembly (NFS mounts of peers, up to 20 s each) ran inside the request; it now runs in a background worker that coalesces requests. `NodeMetrics.pool_fallback` is read live from mergerfs's branch xattr instead of the boot-time value.
- "I was intending to select all drives" (user, 2026-10-26): every Drive across Nodes in one Pool is the default Tier spanning, not a second Tier claiming everything (which empties Default and strands Services on the OS partition). The Default row in the Console has a "spans Nodes" toggle; `pool.UpSpanning` appends peers' default Pools (NFS) after the local Drives with first-found creation so local Drives fill first; `RemoteBranches` already handled any spanning Tier including default. Empty rules stay "match nothing".

### Storage Bins replace Tiers (decisions 121-128, 2026-10-26)

The user's model, built as internals only (the UI sprint owns the Console; see docs/ui-needs.md):

- 128 name: **Storage Bin** ("Bin"), a place for one kind of data. The glossary term Tier is retired; Pool stays the per-Node materialisation of a Bin. Record kind `bin`, message `Bin` (fields: id, name, placement, require_tags, node_ids, priority, created, span_nodes, exclusive, default_for_new).
- Membership: a Drive serves every Bin whose rule matches it; if any exclusive Bin matches, the highest-priority exclusive Bin alone (`storage.BinsOf`). An empty rule means every Drive and is refused for an exclusive Bin (`ValidateBin`, enforced in PutRecord).
- 121 layout: each Bin keeps a folder `<drive>/<bin_id>/` on each member Drive (marker file `.maitre-bin`); the Pool is mergerfs over those folders at `/var/mnt/pools/<bin_id>`. Migration, once per Node: Tier records convert to Bins (the former `default` becomes the Bin marked default for new volumes), and on Drives Maître formatted the files at the root move into that Bin's folder (same-filesystem renames). Drives adopted with their own files are left alone (`.maitre-layout` marker only).
- 123 no built-in Bin: the Fleet has none until the user creates one; exactly one is marked `default_for_new`; installing a Service with no such Bin fails with a clear message. The Node's Pool gauge shows the default Bin's Pool; `pool_fallback` now means "no default Bin Pool on this Node".
- Placement per Bin: `local_first` (mergerfs mfs locally, ff with peers after local branches), `most_free` (mfs), `most_free_percent` (pfrd); settable live through the mergerfs xattr.
- 127 spanning per Bin (`span_nodes`): peers' Pools of that Bin mounted soft over NFS after the local folders, as before.
- 124 folders of removed Bins and pre-adoption files: `ListDrives` reports each adopted Drive's `bins` and top-level `folders` with kind bin | orphan | unsorted; the Node never deletes any of them. The file manager (122, next) moves them.
- 125 capacity: Bins on the same Drives share free space; the Console shows each Bin's own used space and the free space of its unique Drives (UI need).
- 126 Fleet map: a Drive shows one stripe per Bin and a lock for exclusive membership; the minimal Console does this with a striped border.

