#!/usr/bin/env bash
# End-to-end check of the Console's Service tunnel (decision 71): a dev Node
# with a built-in echo Service, and the Desktop's Rust proxy driven by a test
# that sends a plain request and a WebSocket echo through it, and opens a
# Terminal (ADR 0011) into the Service's container: once to the Node
# directly, once through a second dev Node's Relay (ADR 0012). Without
# podman here, the container is scripts/fake-podman's: a real shell on a
# real PTY, held by `maitre terminal-hold`, only the container pretend.
# `maitre terminal-bridge` (Open in your terminal) attaches to that Terminal
# beside the view, as the person's own terminal app would run it.
set -euo pipefail
cd "$(dirname "$0")/.."
MAITRE=${MAITRE:-./server/bin/maitre}
MANIFEST=${MANIFEST:-desktop/src-tauri/Cargo.toml}
T=$(mktemp -d)
trap 'kill $(cat "$T/pid" "$T/anchor.pid" 2>/dev/null) 2>/dev/null || true; rm -rf "$T"' EXIT
# started <log>: waits up to 60 s, polling, for a dev Node to say it is
# listening (a loaded machine takes a while to start one); fails, showing
# its log, if it never does.
started() {
  for _ in $(seq 1 600); do grep -q listening "$1" && return; sleep 0.1; done
  echo "tunnel-check: the dev Node did not start within 60 s:" >&2; cat "$1" >&2; exit 1
}
mkdir -p "$T/node/trusted" "$T/device" "$T/anchor/trusted"
# The test's identity must be trusted before the Node starts; create it first.
MAITRE_TUNNEL_TERMINAL= MAITRE_TUNNEL_NODE=127.0.0.1:1 MAITRE_TUNNEL_FP=x MAITRE_TUNNEL_IDENTITY="$T/device" \
  cargo test --quiet --manifest-path "$MANIFEST" --test tunnel -- --ignored >/dev/null 2>&1 || true
cp "$T/device/identity.pub" "$T/node/trusted/device.pub"
cp "$T/device/identity.pub" "$T/anchor/trusted/device.pub"
# A second dev Node is the anchor whose Relay (ADR 0012) reaches the first.
# The echo Service's container, with bash, for its Terminal.
mkdir -p "$T/containers/echo/bin"
ln -s "$(command -v bash)" "$T/containers/echo/bin/bash"
export MAITRE_PODMAN="$PWD/scripts/fake-podman" MAITRE_FAKE_PODMAN_ROOT="$T/containers" MAITRE_TUNNEL_TERMINAL=1
# Open in your terminal (#11): the bridge is the same binary, bundled as the Desktop's helper.
export MAITRE_TUNNEL_BRIDGE="$(cd "$(dirname "$MAITRE")" && pwd)/$(basename "$MAITRE")"
AFP=$("$MAITRE" keygen --out "$T/anchor/node" | sed -n 's/^identity //p')
"$MAITRE" dev --state "$T/node" --listen 127.0.0.1:17999 --fake-service echo --relay-node "$AFP=127.0.0.1:17998" >"$T/node.log" 2>&1 &
echo $! >"$T/pid"
started "$T/node.log"
FP=$(grep -o 'node_id=[^ ]*' "$T/node.log" | head -1 | cut -d= -f2)
MAITRE_TUNNEL_NODE=127.0.0.1:17999 MAITRE_TUNNEL_FP="$FP" MAITRE_TUNNEL_IDENTITY="$T/device" \
  cargo test --quiet --manifest-path "$MANIFEST" --test tunnel -- --ignored
"$MAITRE" dev --state "$T/anchor" --listen 127.0.0.1:17998 --relay-node "$FP=127.0.0.1:17999" >"$T/anchor.log" 2>&1 &
echo $! >"$T/anchor.pid"
started "$T/anchor.log"
# The Node's own address does not answer for the Desktop; only the anchor does.
MAITRE_TUNNEL_NODE=127.0.0.1:1 MAITRE_TUNNEL_FP="$FP" MAITRE_TUNNEL_IDENTITY="$T/device" \
MAITRE_TUNNEL_ANCHOR=127.0.0.1:17998 MAITRE_TUNNEL_ANCHOR_FP="$AFP" \
  cargo test --quiet --manifest-path "$MANIFEST" --test tunnel -- --ignored
# The anchor journals each relayed connection when it closes.
for _ in $(seq 1 50); do grep -q "relayed connection" "$T/anchor.log" && break; sleep 0.1; done
grep -q "relayed connection" "$T/anchor.log"
echo "tunnel-check: OK (HTTP, WebSocket, a Terminal and its bridge through Desktop proxy -> Node mTLS -> Service, directly and through the anchor's Relay)"
