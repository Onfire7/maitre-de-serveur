<img src="assets/icon.svg" width="64" height="64" alt="">

# Maître De Serveur

An appliance Linux distribution and desktop app that lets non-technical hobbyists run a home lab of containerised apps on bare-metal machines, with as little software on the host as possible so updates never need hands-on intervention.

- **Maître Server**: a bootc image-based OS with the agent built in.
- **Maître Desktop**: the native app that administers a Fleet; its admin interface is the Console.
- **Catalog**: curated app templates, in its own repository.

Vocabulary is in [GLOSSARY.md](GLOSSARY.md). Decisions are in [docs/adr](docs/adr). The plan is [docs/v1-build-order.md](docs/v1-build-order.md).

## Status

Milestone 2 in progress. The Console creates a Fleet and shows the Recovery Phrase, finds machines booted from installer media on the LAN, pairs with a six-digit code, and installs Maître Server to a chosen disk. The installed Node boots, mounts its config partition, assembles its default Pool, and accepts only the devices that provisioned it. Services install from the Catalog ([Onfire7/maitre-catalog](https://github.com/Onfire7/maitre-catalog), a submodule at `catalog/`) as Podman Quadlet units, are reachable as `<name>.maitre.local` through the Node's Caddy, and open inside the Console through a loopback proxy over the same mutual TLS. CI runs install-from-media, reboot, and a Service install in QEMU (`make image-boot`). Not yet: a second Node, remote access, OS updates.

## Layout

```
api/       Protobuf schema (buf); generated code is committed
server/    Go agent: `maitre agent|installer` on the image; `dev|keygen|info|provision` for development
desktop/   Tauri app (Rust shell, React + TypeScript UI)
image/     Containerfile and units for Maître Server
catalog/   Service Catalog (MIT); git submodule of github.com/Onfire7/maitre-catalog
docs/      ADRs and the build plan
```

## Developing

Clone with `git clone --recurse-submodules` (or run `git submodule update --init` afterwards) so the Catalog is present; the image build needs it.

Prerequisites: Go, Rust, pnpm, buf. For the image: podman. Linux desktop builds also need WebKitGTK 4.1 dev packages.

```
make test        # Go, Rust, TypeScript
make m0-check    # end-to-end mTLS between the Rust client and a Go dev Node
make m1-check    # pair + provision a fake Installer, start the agent from its config
make dev         # run a dev Node on 127.0.0.1:7443
cd desktop && pnpm tauri dev
```

To try the real thing without hardware, boot Maître Server in a VM and start the Desktop against it (needs QEMU with KVM, OVMF, and the `gh` CLI; CI builds the installer media because the image build needs podman):

```
make vm                  # terminal 1: first run boots the installer; later runs boot the Node you installed
make desktop             # terminal 2: the Console with live reload
make vm FRESH=1          # wipe the VM and start over
```

The VM's window is the machine's monitor and shows the pairing code. Run `scripts/vm-net-setup.sh` once (it asks for sudo) to put the VM on a host-only bridge: it then has its own address, the Console discovers it, and `http://<service>.local/` works from this machine. Without the bridge the VM sits behind QEMU's NAT: the Console sees its announcement but cannot reach the 10.0.2.x address, and offers `127.0.0.1:17443` instead; Services are reachable with `curl -H 'Host: <name>.local' http://127.0.0.1:18080/`. The installer pulls the OS from `ghcr.io/onfire7/maitre-server`, which CI publishes on every push to main.

On Linux the Desktop disables WebKitGTK's DMA-BUF renderer by default, because it crashes with a Wayland protocol error on NVIDIA. Set `WEBKIT_DISABLE_DMABUF_RENDERER=0` to opt back in.

`make tunnel-check` exercises the Console's Service tunnel without a VM: a dev Node with a built-in echo Service, and the Desktop's relay carrying a plain request and a WebSocket through it.

To walk only the pairing flow, run an Installer with the fake runner and let the Console find it over mDNS:

```
./server/bin/maitre installer --no-screen --no-reboot --image x --state /tmp/inst --fake-install /tmp/node-config
```

Then in the Console: create a Fleet, write down the phrase, click "Set up" on the discovered machine, confirm the code (the fake Installer prints nothing, so trust the Console here), choose "Fake SSD", install. Afterwards `./server/bin/maitre agent --state /tmp/node-config --listen 127.0.0.1:7443` is the Node you just made, and the Console's device is already trusted by it.

For a `maitre dev` Node instead, the Developer section at the bottom of the Console shows the device's public key path; trust it with `make dev-trust DEVICE_PUB=<that path>`, restart `make dev`, and add the Node by address and fingerprint.

## License

Server and desktop: GPL-3.0. Catalog ([Onfire7/maitre-catalog](https://github.com/Onfire7/maitre-catalog)): MIT.
