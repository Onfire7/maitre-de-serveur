#!/usr/bin/env bash
# End-to-end check of the Console's Service tunnel (decision 71): a dev Node
# with a built-in echo Service, and the Desktop's Rust relay driven by a test
# that sends a plain request and a WebSocket echo through it.
set -euo pipefail
cd "$(dirname "$0")/.."
MAITRE=${MAITRE:-./server/bin/maitre}
T=$(mktemp -d)
trap 'kill $(cat "$T/pid" 2>/dev/null) 2>/dev/null || true; rm -rf "$T"' EXIT
mkdir -p "$T/node/trusted" "$T/device"
# The test's identity must be trusted before the Node starts; create it first.
MAITRE_TUNNEL_NODE=127.0.0.1:1 MAITRE_TUNNEL_FP=x MAITRE_TUNNEL_IDENTITY="$T/device" \
  cargo test --quiet --manifest-path desktop/src-tauri/Cargo.toml --test tunnel -- --ignored >/dev/null 2>&1 || true
cp "$T/device/identity.pub" "$T/node/trusted/device.pub"
"$MAITRE" dev --state "$T/node" --listen 127.0.0.1:17999 --fake-service echo >"$T/node.log" 2>&1 &
echo $! >"$T/pid"
for _ in $(seq 1 50); do grep -q listening "$T/node.log" && break; sleep 0.1; done
FP=$(grep -o 'node_id=[^ ]*' "$T/node.log" | head -1 | cut -d= -f2)
MAITRE_TUNNEL_NODE=127.0.0.1:17999 MAITRE_TUNNEL_FP="$FP" MAITRE_TUNNEL_IDENTITY="$T/device" \
  cargo test --quiet --manifest-path desktop/src-tauri/Cargo.toml --test tunnel -- --ignored
echo "tunnel-check: OK (HTTP and WebSocket through Desktop relay -> Node mTLS -> Service)"
