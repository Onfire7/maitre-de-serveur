#!/bin/bash
# greenboot required check (Milestone 6): the Node is healthy after a boot
# when its agent answers, podman works, and every Maître Service that ran
# before the update runs again. Three failed boots roll the image back.
set -u
fail() { echo "maitre health: $1" >&2; exit 1; }
for _ in $(seq 1 60); do
  systemctl is-active --quiet maitre-agent.service && break
  sleep 2
done
systemctl is-active --quiet maitre-agent.service || fail "agent is not running"
timeout 5 bash -c 'exec 3<>/dev/tcp/127.0.0.1/7443' 2>/dev/null || fail "agent does not accept connections"
podman info >/dev/null 2>&1 || fail "podman is not working"
if [ -f /var/lib/maitre/running-services ]; then
  while read -r unit; do
    [ -n "$unit" ] || continue
    ok=0
    for _ in $(seq 1 90); do
      state=$(systemctl show "$unit" -p ActiveState --value 2>/dev/null)
      [ "$state" = active ] && { ok=1; break; }
      [ "$state" = failed ] && break
      sleep 2
    done
    [ $ok = 1 ] || fail "$unit did not come back"
  done < /var/lib/maitre/running-services
fi
echo "maitre health: ok"
