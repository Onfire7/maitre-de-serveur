#!/usr/bin/env bash
# Milestone 1 acceptance, no root or hardware needed: an Installer (with the
# fake install runner) is paired and provisioned by the CLI stand-in for the
# Console, then `maitre agent` starts from the seeded config partition and
# accepts exactly the device that provisioned it.
set -euo pipefail
MAITRE=${1:?path to maitre binary}
STATE=$(mktemp -d)
trap 'kill ${INST_PID:-} ${AGENT_PID:-} 2>/dev/null || true; rm -rf "$STATE"' EXIT
PORT=$(( 20000 + RANDOM % 20000 ))

"$MAITRE" installer --no-mdns --no-screen --no-reboot --image example.invalid/maitre:test \
  --state "$STATE/installer" --fake-install "$STATE/config" --listen "127.0.0.1:$PORT" \
  2>"$STATE/installer.log" & INST_PID=$!
for _ in $(seq 1 50); do grep -q listening "$STATE/installer.log" 2>/dev/null && break; sleep 0.1; done

"$MAITRE" provision --identity "$STATE/device" --addr "127.0.0.1:$PORT" \
  --fleet "Test Fleet" --name "node-one" --disk /dev/fake-ssd --yes | tee "$STATE/provision.log"
grep -q "Pairing code: [0-9]\{3\} [0-9]\{3\}" "$STATE/provision.log"
grep -q "Recovery Phrase" "$STATE/provision.log"
kill $INST_PID; wait $INST_PID 2>/dev/null || true

test -f "$STATE/config/node/identity.key" && test -f "$STATE/config/fleet.db"

"$MAITRE" agent --no-screen --state "$STATE/config" --no-mdns --listen "127.0.0.1:$PORT" 2>"$STATE/agent.log" & AGENT_PID=$!
for _ in $(seq 1 50); do grep -q listening "$STATE/agent.log" 2>/dev/null && break; sleep 0.1; done

mkdir -p "$STATE/device/trusted" && cp "$STATE/config/node/identity.pub" "$STATE/device/trusted/node.pub"
"$MAITRE" info --identity "$STATE/device" --trust "$STATE/device/trusted" --addr "127.0.0.1:$PORT" | tee "$STATE/info.log"
grep -q "^name      node-one" "$STATE/info.log"
grep -q "^dev_mode  false" "$STATE/info.log"
grep -Eq "^fleet_id  [0-9a-f]{32}" "$STATE/info.log"

# A device that was not in the trust list at provisioning must be refused.
"$MAITRE" keygen --out "$STATE/stranger" >/dev/null
mkdir -p "$STATE/stranger/trusted" && cp "$STATE/config/node/identity.pub" "$STATE/stranger/trusted/node.pub"
if "$MAITRE" info --identity "$STATE/stranger" --trust "$STATE/stranger/trusted" --addr "127.0.0.1:$PORT" >/dev/null 2>&1; then
  echo "FAIL: untrusted device was accepted by the provisioned node"; exit 1
fi
echo "m1-check: OK"
