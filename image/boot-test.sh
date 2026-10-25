#!/usr/bin/env bash
# Milestone 1 demo, automated: boot the image as installer media in QEMU with
# a blank second disk, provision it from the host with `maitre provision`
# (the CLI stand-in for the Console), let it reboot into the installed Node,
# and call GetNodeInfo as the Trusted Device that provisioned it.
#
# Requires: rootful podman (bootc-image-builder), qemu-system-x86_64 with KVM,
# the maitre binary, and a free TCP port on the host for the registry.
set -euo pipefail
IMAGE=${1:?image ref, e.g. localhost/maitre-server:abc}
MAITRE=${MAITRE:-./server/bin/maitre}
OUT=${OUT:-$(mktemp -d)}
TIMEOUT=${TIMEOUT:-900}
REG_PORT=${REG_PORT:-5000}
FWD_PORT=${FWD_PORT:-17443}
WEB_PORT=${WEB_PORT:-18080}
# 10.0.2.2 is the host as seen from QEMU user networking.
GUEST_IMAGE="10.0.2.2:${REG_PORT}/maitre-server:test"

cleanup() {
  kill "${QEMU:-}" 2>/dev/null || true
  sudo podman rm -f maitre-test-registry >/dev/null 2>&1 || true
}
trap cleanup EXIT

# 1. A throwaway registry on the host holding the image to install.
sudo podman rm -f maitre-test-registry >/dev/null 2>&1 || true
sudo podman run -d --name maitre-test-registry -p "${REG_PORT}:5000" docker.io/library/registry:2 >/dev/null
if ! sudo podman image exists "$IMAGE"; then
  echo "copying $IMAGE into rootful podman storage"
  podman save "$IMAGE" | sudo podman load
fi
sudo podman tag "$IMAGE" "localhost:${REG_PORT}/maitre-server:test"
for _ in $(seq 1 30); do curl -fs "http://127.0.0.1:${REG_PORT}/v2/" >/dev/null && break; sleep 1; done
sudo podman push --tls-verify=false "localhost:${REG_PORT}/maitre-server:test"

# 2. Installer media: the same image, with kernel arguments selecting
#    installer mode and naming the image to install.
for i in $(seq 1 10); do
  sudo podman pull quay.io/centos-bootc/bootc-image-builder:latest >/dev/null && break
  [ "$i" -lt 10 ] || { echo "boot-test: could not pull bootc-image-builder"; exit 1; }
  echo "pull failed; retrying in 30s"; sleep 30
done
cat > "$OUT/config.toml" <<TOML
[customizations.kernel]
append = "maitre.installer maitre.image=${GUEST_IMAGE} maitre.insecure_registry=10.0.2.2:${REG_PORT} console=ttyS0,115200n8"
TOML
sudo podman run --rm --privileged --security-opt label=type:unconfined_t \
  -v "$OUT:/output" -v "$OUT/config.toml:/config.toml:ro" \
  -v /var/lib/containers/storage:/var/lib/containers/storage \
  quay.io/centos-bootc/bootc-image-builder:latest \
  --type qcow2 --rootfs xfs --config /config.toml --local "$IMAGE"
sudo chown -R "$(id -u):$(id -g)" "$OUT"
INSTALLER=$(find "$OUT" -name '*.qcow2' | head -1)
[ -n "$INSTALLER" ] || { echo "boot-test: no qcow2 produced"; ls -R "$OUT"; exit 1; }
TARGET="$OUT/target.qcow2"
qemu-img create -f qcow2 "$TARGET" 40G >/dev/null

if [ ! -r /dev/kvm ] || [ ! -w /dev/kvm ]; then sudo chmod 666 /dev/kvm; fi

# 3. Boot under UEFI, like real hardware. The empty target has bootindex 0 so
#    that after the install the machine boots the new Node; the firmware
#    skips it while it is blank and boots the installer.
OVMF_CODE=""; OVMF_VARS=""
for f in /usr/share/OVMF/OVMF_CODE_4M.fd /usr/share/OVMF/OVMF_CODE.fd /usr/share/edk2/x64/OVMF_CODE.4m.fd /usr/share/edk2/ovmf/OVMF_CODE.fd; do
  [ -z "$OVMF_CODE" ] && [ -f "$f" ] && OVMF_CODE=$f
done
for f in /usr/share/OVMF/OVMF_VARS_4M.fd /usr/share/OVMF/OVMF_VARS.fd /usr/share/edk2/x64/OVMF_VARS.4m.fd /usr/share/edk2/ovmf/OVMF_VARS.fd; do
  [ -z "$OVMF_VARS" ] && [ -f "$f" ] && OVMF_VARS=$f
done
[ -n "$OVMF_CODE" ] && [ -n "$OVMF_VARS" ] || { echo "boot-test: OVMF firmware not found (install ovmf/edk2-ovmf)"; exit 1; }
cp "$OVMF_VARS" "$OUT/vars.fd"
echo "booting installer $INSTALLER with blank target $TARGET (UEFI $OVMF_CODE)"
qemu-system-x86_64 \
  -m 3072 -smp 2 -nographic -enable-kvm -cpu host -machine q35 \
  -drive "if=pflash,format=raw,readonly=on,file=$OVMF_CODE" \
  -drive "if=pflash,format=raw,file=$OUT/vars.fd" \
  -drive "file=$TARGET,format=qcow2,if=none,id=target" -device virtio-blk-pci,drive=target,bootindex=0 \
  -drive "file=$INSTALLER,format=qcow2,if=none,id=installer" -device virtio-blk-pci,drive=installer,bootindex=1 \
  -netdev "user,id=n0,hostfwd=tcp:127.0.0.1:${FWD_PORT}-:7443,hostfwd=tcp:127.0.0.1:${WEB_PORT}-:80" -device virtio-net-pci,netdev=n0 \
  >"$OUT/serial.log" 2>&1 &
QEMU=$!

wait_for() { # pattern, seconds
  for _ in $(seq "$2"); do
    grep -qE "$1" "$OUT/serial.log" && return 0
    kill -0 "$QEMU" 2>/dev/null || { echo "boot-test: qemu exited"; tail -80 "$OUT/serial.log"; return 1; }
    sleep 1
  done
  echo "boot-test: timed out waiting for /$1/"; tail -120 "$OUT/serial.log"; return 1
}

wait_for 'maitre installer' 300
echo "installer is up; provisioning from the host"
DEV="$OUT/device"
"$MAITRE" provision --identity "$DEV" --addr "127.0.0.1:${FWD_PORT}" --fleet "CI Fleet" --name ci-node \
  --disk /dev/vda --yes --save-node-pub "$OUT/node.pub" | tee "$OUT/provision.log"

# 4. The machine reboots into the installed Node.
echo "waiting for the installed node"
# The installer's own 'listening' line is already in the log; look for the agent's banner.
wait_for 'msg="maitre agent"' "$TIMEOUT"
wait_for 'maitre agent.*|listening addr=0.0.0.0:7443' 120
sleep 2
mkdir -p "$DEV/trusted" && cp "$OUT/node.pub" "$DEV/trusted/node.pub"
for i in $(seq 1 30); do
  if "$MAITRE" info --identity "$DEV" --trust "$DEV/trusted" --addr "127.0.0.1:${FWD_PORT}" | tee "$OUT/info.log"; then break; fi
  [ "$i" -lt 30 ] || { echo "boot-test: node never answered"; exit 1; }
  sleep 2
done
grep -q "^name      ci-node" "$OUT/info.log"
grep -q "^dev_mode  false" "$OUT/info.log"
echo "boot-test: node OK (installed from USB-equivalent media, rebooted, answered as a Node)"

# 5. Milestone 2: install a Service from the Catalog and reach it by name
#    through the Node's Caddy (plain HTTP on the LAN, decision 71).
"$MAITRE" service --identity "$DEV" --trust "$DEV/trusted" --addr "127.0.0.1:${FWD_PORT}" catalog
"$MAITRE" service --identity "$DEV" --trust "$DEV/trusted" --addr "127.0.0.1:${FWD_PORT}" --wait 10m install uptime-kuma
for i in $(seq 1 30); do
  # Follow the app's own redirects (Uptime Kuma sends first visitors to /setup).
  code=$(curl -s -L -o "$OUT/web.html" -w '%{http_code}' -H 'Host: uptime-kuma.local' "http://127.0.0.1:${WEB_PORT}/" || true)
  [ "$code" = 200 ] && break
  [ "$i" -lt 30 ] || { echo "boot-test: service not reachable through caddy (last status $code)"; "$MAITRE" service --identity "$DEV" --trust "$DEV/trusted" --addr "127.0.0.1:${FWD_PORT}" logs uptime-kuma | tail -30; exit 1; }
  sleep 2
done
grep -qi "uptime kuma" "$OUT/web.html"
"$MAITRE" service --identity "$DEV" --trust "$DEV/trusted" --addr "127.0.0.1:${FWD_PORT}" list
echo "boot-test: OK (Uptime Kuma installed from the Catalog and served at uptime-kuma.local)"
