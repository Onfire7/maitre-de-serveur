#!/usr/bin/env bash
# Milestone 3 acceptance, no root or hardware: two Nodes of one Fleet on
# loopback. B joins A's Fleet, records flow both ways, a write for an
# offline owner is queued and delivered when it returns.
set -euo pipefail
MAITRE=${1:?path to maitre binary}
STATE=$(mktemp -d)
trap 'kill ${INST_PID:-} ${A_PID:-} ${B_PID:-} 2>/dev/null || true; rm -rf "$STATE"' EXIT
PA=$(( 20000 + RANDOM % 20000 )); PB=$(( PA + 1 ))
DEV="--identity $STATE/device --trust $STATE/device/trusted"

install_node() { # name port joinflags...
  local name=$1 port=$2; shift 2
  "$MAITRE" installer --no-mdns --no-screen --no-reboot --image example.invalid/maitre:test \
    --state "$STATE/inst-$name" --fake-install "$STATE/$name" --listen "127.0.0.1:$port" 2>"$STATE/inst-$name.log" & INST_PID=$!
  for _ in $(seq 1 50); do grep -q listening "$STATE/inst-$name.log" 2>/dev/null && break; sleep 0.1; done
  "$MAITRE" provision --identity "$STATE/device" --trust "$STATE/device/trusted" --addr "127.0.0.1:$port" --name "$name" --disk /dev/fake-ssd --yes "$@" > "$STATE/prov-$name.log"
  kill $INST_PID; wait $INST_PID 2>/dev/null || true
  mkdir -p "$STATE/device/trusted" && cp "$STATE/$name/node/identity.pub" "$STATE/device/trusted/$name.pub"
}
start_node() { # name port
  "$MAITRE" agent --no-screen --no-mdns --state "$STATE/$1" --listen "127.0.0.1:$2" --advertise "127.0.0.1:$2" 2>>"$STATE/agent-$1.log" &
  for _ in $(seq 1 50); do "$MAITRE" info $DEV --addr "127.0.0.1:$2" >/dev/null 2>&1 && break; sleep 0.2; done
}

install_node a "$PA" --fleet "Sync Fleet"
start_node a "$PA"; A_PID=$!
install_node b "$PB" --join-from "127.0.0.1:$PA"
grep -q "joining Fleet \"Sync Fleet\" with 1 Node" "$STATE/prov-b.log"
grep -q "introduced to the Fleet" "$STATE/prov-b.log"
start_node b "$PB"; B_PID=$!

# Both hold both Node records, the Fleet, and the same devices.
for _ in $(seq 1 50); do
  [ "$("$MAITRE" record $DEV --addr "127.0.0.1:$PB" --kind node list | wc -l)" = 2 ] && [ "$("$MAITRE" record $DEV --addr "127.0.0.1:$PA" --kind node list | wc -l)" = 2 ] && break
  sleep 0.2
done
"$MAITRE" record $DEV --addr "127.0.0.1:$PA" list > "$STATE/a.list"; "$MAITRE" record $DEV --addr "127.0.0.1:$PB" list > "$STATE/b.list"
diff "$STATE/a.list" "$STATE/b.list"
"$MAITRE" info $DEV --addr "127.0.0.1:$PA" | grep -q "^name      a"

# A write on A reaches B by push.
"$MAITRE" record $DEV --addr "127.0.0.1:$PA" --kind service --id demo --body "owned by a" put | grep -q "^applied"
for _ in $(seq 1 50); do "$MAITRE" record $DEV --addr "127.0.0.1:$PB" --kind service --id demo get 2>/dev/null | grep -q "owned by a" && break; sleep 0.2; done
"$MAITRE" record $DEV --addr "127.0.0.1:$PB" --kind service --id demo get | grep -q "owned by a"

# A write on B for a record A owns is forwarded to A.
"$MAITRE" record $DEV --addr "127.0.0.1:$PB" --kind service --id demo --body "edited on b" put | grep -q "^applied"
"$MAITRE" record $DEV --addr "127.0.0.1:$PA" --kind service --id demo get | grep -q "edited on b"

# A goes offline: the same write is queued on B, delivered when A returns.
kill $A_PID; wait $A_PID 2>/dev/null || true
"$MAITRE" record $DEV --addr "127.0.0.1:$PB" --kind service --id demo --body "edited while a was off" put | grep -q "^queued"
"$MAITRE" info $DEV --addr "127.0.0.1:$PB" >/dev/null
start_node a "$PA"; A_PID=$!
for _ in $(seq 1 100); do "$MAITRE" record $DEV --addr "127.0.0.1:$PA" --kind service --id demo get 2>/dev/null | grep -q "edited while a was off" && break; sleep 0.3; done
"$MAITRE" record $DEV --addr "127.0.0.1:$PA" --kind service --id demo get | grep -q "edited while a was off"
for _ in $(seq 1 100); do "$MAITRE" record $DEV --addr "127.0.0.1:$PB" --kind service --id demo get 2>/dev/null | grep -q "edited while a was off" && break; sleep 0.3; done
"$MAITRE" record $DEV --addr "127.0.0.1:$PB" --kind service --id demo get | grep -q "edited while a was off"
echo "m3-check: OK"
