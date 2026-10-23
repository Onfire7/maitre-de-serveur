#!/usr/bin/env bash
# Milestone 0 acceptance: a Rust Trusted Device reaches a Go dev Node over
# mutual TLS, and both sides refuse unknown keys.
set -euo pipefail
MAITRE=${1:?path to maitre binary}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
STATE=$(mktemp -d)
trap 'kill ${NODE_PID:-} 2>/dev/null || true; rm -rf "$STATE"' EXIT
export MAITRE_STATE="$STATE"
PORT=$(( 20000 + RANDOM % 20000 ))

NODE_FP=$("$MAITRE" keygen --out "$STATE/node" | awk '/^identity/{print $2}')

# Generate the Rust device key (test skips the network part without a node).
( cd "$ROOT/desktop/src-tauri" && MAITRE_TEST_DEVICE_DIR="$STATE/rustdev" cargo test -q --lib rpc::tests::trusted >/dev/null )
mkdir -p "$STATE/trusted" && cp "$STATE/rustdev/identity.pub" "$STATE/trusted/rustdev.pub"

"$MAITRE" dev --listen "127.0.0.1:$PORT" 2>"$STATE/node.log" & NODE_PID=$!
for _ in $(seq 1 50); do grep -q listening "$STATE/node.log" 2>/dev/null && break; sleep 0.1; done

( cd "$ROOT/desktop/src-tauri" && \
  MAITRE_TEST_NODE_ADDR="127.0.0.1:$PORT" MAITRE_TEST_NODE_FP="$NODE_FP" MAITRE_TEST_DEVICE_DIR="$STATE/rustdev" \
  cargo test -q --lib rpc::tests )

# Go CLI device, untrusted by the node: must be refused.
"$MAITRE" keygen --out "$STATE/device" >/dev/null
mkdir -p "$STATE/device/trusted" && cp "$STATE/node/identity.pub" "$STATE/device/trusted/node.pub"
if "$MAITRE" info --addr "127.0.0.1:$PORT" >/dev/null 2>&1; then
  echo "FAIL: untrusted Go device was accepted"; exit 1
fi
echo "m0-check: OK"
