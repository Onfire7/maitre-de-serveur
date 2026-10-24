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

The Catalog is a separate repository (`maitre-catalog`), MIT licensed.

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
- Embedded Caddy: routes `<service>.maitre.local` to the Service. mDNS publishes the names. Private exposure only in this milestone.
- Console: Catalog browser, install flow with settings form, Service page with status, logs, and the built-in browser tab.
- Observability v1: Node gauges (CPU, memory, Pool capacity, network, temperature if available).

Demo: install Jellyfin from the Console, open `jellyfin.maitre.local` inside the Console.

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
- 74. Catalog lives in `catalog/` in the monorepo until a separate repository is named.

## Open questions to settle before Milestone 2

1. Console webview trust for Private Services (see Milestone 2). Recommended: loopback proxy over mTLS.
2. Console status screen on the Node: plain text from the agent, or a small TUI library. Recommended: plain text, it is read once.
3. Catalog index delivery: git clone on the Node or a generated JSON index fetched over HTTPS. Recommended: JSON index, signed with the same key that signs the image once signing exists.
