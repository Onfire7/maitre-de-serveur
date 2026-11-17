#!/usr/bin/env bash
# End-to-end check of the Relay (ADR 0012), no root or hardware: two Nodes
# of one Fleet on loopback, A the anchor and B a Node the Desktop cannot
# reach itself (it is given an address of B's that does not answer). The
# Desktop's Rust client uses B through A's Relay: B answers as itself, an
# action and its logs work, and the file-contents proxy reaches B.
set -euo pipefail
cd "$(dirname "$0")/.."
MAITRE=${1:-./server/bin/maitre}
MANIFEST=${MANIFEST:-desktop/src-tauri/Cargo.toml}
STATE=$(mktemp -d)
trap 'kill ${INST_PID:-} ${A_PID:-} ${B_PID:-} 2>/dev/null || true; rm -rf "$STATE"' EXIT
PA=$(( 20000 + RANDOM % 20000 )); PB=$(( PA + 1 ))
"$MAITRE" keygen --out "$STATE/device" >/dev/null
DEV="--identity $STATE/device --trust $STATE/device/trusted"

install_node() { # name port joinflags...
  local name=$1 port=$2; shift 2
  "$MAITRE" installer --no-mdns --no-screen --no-reboot --image example.invalid/maitre:test \
    --state "$STATE/inst-$name" --fake-install "$STATE/$name" --listen "127.0.0.1:$port" 2>"$STATE/inst-$name.log" & INST_PID=$!
  # Up to 60 s: a loaded machine takes a while to start one.
  for _ in $(seq 1 600); do grep -q listening "$STATE/inst-$name.log" 2>/dev/null && break; sleep 0.1; done
  "$MAITRE" provision $DEV --addr "127.0.0.1:$port" --name "$name" --disk /dev/fake-ssd --yes "$@" > "$STATE/prov-$name.log"
  kill $INST_PID; wait $INST_PID 2>/dev/null || true
  mkdir -p "$STATE/device/trusted" && cp "$STATE/$name/node/identity.pub" "$STATE/device/trusted/$name.pub"
}
start_node() { # name port
  "$MAITRE" agent --no-screen --no-mdns --no-tailscale --caddy "" --state "$STATE/$1" --listen "127.0.0.1:$2" --advertise "127.0.0.1:$2" 2>>"$STATE/agent-$1.log" &
  for _ in $(seq 1 300); do "$MAITRE" info $DEV --addr "127.0.0.1:$2" >/dev/null 2>&1 && break; sleep 0.2; done
}
fp() { "$MAITRE" keygen --out "$STATE/$1/node" | sed -n 's/^identity //p'; }

install_node a "$PA" --fleet "Relay Fleet"
start_node a "$PA"; A_PID=$!
install_node b "$PB" --join-from "127.0.0.1:$PA"
start_node b "$PB"; B_PID=$!
AFP=$(fp a); BFP=$(fp b)
# A knows B as a Node of the Fleet, with its address, before the Relay is asked.
for _ in $(seq 1 100); do
  [ "$("$MAITRE" record $DEV --addr "127.0.0.1:$PA" --kind node list | wc -l)" = 2 ] && break
  sleep 0.2
done

# A machine without journalctl has no logs to give; the Node says so itself.
command -v journalctl >/dev/null || export MAITRE_RELAY_NO_JOURNAL=1
# B's address as the Desktop sees it: nothing answers there.
MAITRE_RELAY_ANCHOR="127.0.0.1:$PA" MAITRE_RELAY_ANCHOR_FP="$AFP" \
MAITRE_RELAY_NODE="127.0.0.1:1" MAITRE_RELAY_NODE_FP="$BFP" MAITRE_RELAY_NODE_NAME=b \
MAITRE_RELAY_IDENTITY="$STATE/device" \
  cargo test --quiet --manifest-path "$MANIFEST" --test relay -- --ignored --test-threads 1

# The rename was B's own, and the anchor journaled the relayed connections.
for _ in $(seq 1 50); do "$MAITRE" info $DEV --addr "127.0.0.1:$PB" | grep -q "^name      b-relayed" && break; sleep 0.2; done
"$MAITRE" info $DEV --addr "127.0.0.1:$PB" | grep -q "^name      b-relayed"
for _ in $(seq 1 50); do grep -q "relayed connection.*target=$BFP" "$STATE/agent-a.log" && break; sleep 0.1; done
grep -q "relayed connection.*target=$BFP" "$STATE/agent-a.log"
echo "relay-check: OK (a Node reached only through the anchor: info, rename, logs, file contents proxy)"
