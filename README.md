<img src="assets/icon.svg" width="64" height="64" alt="">

# Maître De Serveur

An appliance Linux distribution and desktop app that lets non-technical hobbyists run a home lab of containerised apps on bare-metal machines, with as little software on the host as possible so updates never need hands-on intervention.

- **Maître Server**: a bootc image-based OS with the agent built in.
- **Maître Desktop**: the native app that administers a Fleet; its admin interface is the Console.
- **Catalog**: curated app templates, in its own repository.

Vocabulary is in [GLOSSARY.md](GLOSSARY.md). Decisions are in [docs/adr](docs/adr). The plan is [docs/v1-build-order.md](docs/v1-build-order.md).

## Status

Milestone 0. A dev Node and the desktop talk over mutual TLS with a trust list; nothing is installable yet.

## Layout

```
api/       Protobuf schema (buf); generated code is committed
server/    Go agent: `maitre dev|keygen|info` today, `agent|installer` next
desktop/   Tauri app (Rust shell, React + TypeScript UI)
image/     Containerfile and units for Maître Server
docs/      ADRs and the build plan
```

## Developing

Prerequisites: Go, Rust, pnpm, buf. For the image: podman. Linux desktop builds also need WebKitGTK 4.1 dev packages.

```
make test        # Go, Rust, TypeScript
make m0-check    # end-to-end mTLS between the Rust client and a Go dev Node
make dev         # run a dev Node on 127.0.0.1:7443
cd desktop && pnpm tauri dev
```

On first run the desktop shows its device fingerprint and the path of its public key. Trust it on the dev Node with `make dev-trust DEVICE_PUB=<that path>`, restart `make dev`, paste the printed Node fingerprint into the desktop, and connect.

## License

Server and desktop: GPL-3.0. Catalog: MIT.
