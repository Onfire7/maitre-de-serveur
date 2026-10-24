<img src="assets/icon.svg" width="64" height="64" alt="">

# Maître De Serveur

An appliance Linux distribution and desktop app that lets non-technical hobbyists run a home lab of containerised apps on bare-metal machines, with as little software on the host as possible so updates never need hands-on intervention.

- **Maître Server**: a bootc image-based OS with the agent built in.
- **Maître Desktop**: the native app that administers a Fleet; its admin interface is the Console.
- **Catalog**: curated app templates, in its own repository.

Vocabulary is in [GLOSSARY.md](GLOSSARY.md). Decisions are in [docs/adr](docs/adr). The plan is [docs/v1-build-order.md](docs/v1-build-order.md).

## Status

Milestone 1 in progress. The Console creates a Fleet and shows the Recovery Phrase, finds machines booted from installer media on the LAN, pairs with a six-digit code, and installs Maître Server to a chosen disk. The installed Node boots, mounts its config partition, and accepts only the devices that provisioned it. CI runs the whole thing in QEMU (`make image-boot`). Not yet: Services, a second Node, remote access.

## Layout

```
api/       Protobuf schema (buf); generated code is committed
server/    Go agent: `maitre agent|installer` on the image; `dev|keygen|info|provision` for development
desktop/   Tauri app (Rust shell, React + TypeScript UI)
image/     Containerfile and units for Maître Server
docs/      ADRs and the build plan
```

## Developing

Prerequisites: Go, Rust, pnpm, buf. For the image: podman. Linux desktop builds also need WebKitGTK 4.1 dev packages.

```
make test        # Go, Rust, TypeScript
make m0-check    # end-to-end mTLS between the Rust client and a Go dev Node
make m1-check    # pair + provision a fake Installer, start the agent from its config
make dev         # run a dev Node on 127.0.0.1:7443
cd desktop && pnpm tauri dev
```

To walk the install flow on one machine without hardware, run an Installer with the fake runner and let the Console find it over mDNS:

```
./server/bin/maitre installer --no-screen --no-reboot --image x --state /tmp/inst --fake-install /tmp/node-config
```

Then in the Console: create a Fleet, write down the phrase, click "Set up" on the discovered machine, confirm the code (the fake Installer prints nothing, so trust the Console here), choose "Fake SSD", install. Afterwards `./server/bin/maitre agent --state /tmp/node-config --listen 127.0.0.1:7443` is the Node you just made, and the Console's device is already trusted by it.

For a `maitre dev` Node instead, the Developer section at the bottom of the Console shows the device's public key path; trust it with `make dev-trust DEVICE_PUB=<that path>`, restart `make dev`, and add the Node by address and fingerprint.

## License

Server and desktop: GPL-3.0. Catalog: MIT.
