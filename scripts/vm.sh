#!/usr/bin/env bash
# Developer VM without hardware or podman: boot Maître Server in QEMU.
# Run Maître Desktop separately with `make desktop`.
#
#   make vm                  # first run: boots the installer; later runs: boots the Node you installed
#   make vm FRESH=1          # wipe the virtual disk and start over from the installer
#   RUN=<ci run id> ...      # use a specific CI run's installer media instead of the latest on main
#   DISPLAY_BACKEND=none ... # headless: no VM window, drive it with the maitre CLI
#
# Ctrl-C stops the VM. The installer media comes from CI (the `installer-media`
# artifact of the image job), because building the image needs podman. The
# installer pulls the OS from ghcr.io, so the VM needs internet access.
#
# QEMU's user-mode network relays the VM's mDNS announcement, but the address
# it advertises (10.0.2.x) is unreachable from the host: when the Console says
# it cannot reach the machine, enter 127.0.0.1:${FWD_PORT}. The VM's display
# window is the machine's monitor: the pairing code shows there.
set -euo pipefail
cd "$(dirname "$0")/.."
WORK=${WORK:-$HOME/.maitre/qemu}
FWD_PORT=${FWD_PORT:-17443}
WEB_PORT=${WEB_PORT:-18080}
DISK_SIZE=${DISK_SIZE:-40G}
REPO=${REPO:-Onfire7/maitre-de-serveur}
mkdir -p "$WORK"

need() { command -v "$1" >/dev/null || { echo "vm: missing $1"; exit 1; }; }
need qemu-system-x86_64; need qemu-img; need gh
[ -r /dev/kvm ] && [ -w /dev/kvm ] || { echo "vm: /dev/kvm not accessible (add yourself to the kvm group)"; exit 1; }

# 1. Installer media from CI.
if [ -n "${RUN:-}" ] || [ ! -f "$WORK/maitre-installer.qcow2" ] || [ -n "${FRESH:-}" ]; then
  if [ -z "${RUN:-}" ]; then
    RUN=$(gh run list -R "$REPO" --branch main --workflow ci --status success --limit 1 --json databaseId -q '.[0].databaseId')
    [ -n "$RUN" ] || { echo "vm: no successful CI run on main yet"; exit 1; }
  fi
  if [ "$(cat "$WORK/run-id" 2>/dev/null)" != "$RUN" ] || [ ! -f "$WORK/maitre-installer.qcow2" ]; then
    echo "downloading installer media from CI run $RUN"
    rm -rf "$WORK/dl"; gh run download "$RUN" -R "$REPO" -n installer-media -D "$WORK/dl"
    mv "$WORK/dl/maitre-installer.qcow2" "$WORK/maitre-installer.qcow2"
    mv "$WORK/dl/image-ref.txt" "$WORK/image-ref.txt"
    # Firmware from the CI run that booted this media. Current edk2 releases
    # (202505, 202608) page-fault in GRUB's hand-off to the Fedora 42 kernel.
    [ -f "$WORK/dl/OVMF_CODE.fd" ] && mv "$WORK/dl/OVMF_CODE.fd" "$WORK/dl/OVMF_VARS.fd" "$WORK/"
    rm -rf "$WORK/dl"
    echo "$RUN" > "$WORK/run-id"
  fi
fi
echo "installer media: $WORK/maitre-installer.qcow2 (installs $(cat "$WORK/image-ref.txt"))"

# The installer pulls from GHCR anonymously; the package must be public.
IMG=$(cat "$WORK/image-ref.txt"); PKG=${IMG#ghcr.io/}; TAG=${PKG##*:}; PKG=${PKG%%:*}
TOKEN=$(curl -fs "https://ghcr.io/token?scope=repository:${PKG}:pull" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
if ! curl -fs -o /dev/null -H "Authorization: Bearer $TOKEN" \
     -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json' \
     "https://ghcr.io/v2/${PKG}/manifests/${TAG}"; then
  echo "vm: ghcr.io/${PKG} is not publicly pullable."
  echo "  Make the package public once: https://github.com/users/${PKG%%/*}/packages/container/${PKG#*/}/settings"
  exit 1
fi

# 2. The machine's disk. Blank on the first run: the firmware skips it and
#    boots the installer; after the install it boots the Node.
if [ -n "${FRESH:-}" ]; then rm -f "$WORK/target.qcow2" "$WORK/vars.fd"; fi
[ -f "$WORK/target.qcow2" ] || qemu-img create -f qcow2 "$WORK/target.qcow2" "$DISK_SIZE" >/dev/null

OVMF_CODE=${OVMF_CODE:-}; OVMF_VARS=${OVMF_VARS:-}
if [ -z "$OVMF_CODE" ] && [ -f "$WORK/OVMF_CODE.fd" ]; then OVMF_CODE=$WORK/OVMF_CODE.fd; OVMF_VARS=$WORK/OVMF_VARS.fd; fi
for f in /usr/share/OVMF/OVMF_CODE_4M.fd /usr/share/OVMF/OVMF_CODE.fd /usr/share/edk2/x64/OVMF_CODE.4m.fd /usr/share/edk2/ovmf/OVMF_CODE.fd; do
  [ -z "$OVMF_CODE" ] && [ -f "$f" ] && OVMF_CODE=$f
done
for f in /usr/share/OVMF/OVMF_VARS_4M.fd /usr/share/OVMF/OVMF_VARS.fd /usr/share/edk2/x64/OVMF_VARS.4m.fd /usr/share/edk2/ovmf/OVMF_VARS.fd; do
  [ -z "$OVMF_VARS" ] && [ -f "$f" ] && OVMF_VARS=$f
done
[ -n "$OVMF_CODE" ] && [ -n "$OVMF_VARS" ] || { echo "vm: OVMF firmware not found (install edk2-ovmf)"; exit 1; }
[ -f "$WORK/vars.fd" ] || cp "$OVMF_VARS" "$WORK/vars.fd"

cleanup() { kill "${QEMU:-}" 2>/dev/null || true; }
trap cleanup EXIT

echo "firmware: $OVMF_CODE"
echo "booting VM: display window = the machine's monitor, serial log = $WORK/serial.log"
qemu-system-x86_64 \
  -name maitre-server -m "${MEM:-3072}" -smp "${CPUS:-2}" -enable-kvm -cpu host -machine q35 \
  -display "${DISPLAY_BACKEND:-gtk}" -serial "file:$WORK/serial.log" \
  -drive "if=pflash,format=raw,readonly=on,file=$OVMF_CODE" \
  -drive "if=pflash,format=raw,file=$WORK/vars.fd" \
  -drive "file=$WORK/target.qcow2,format=qcow2,if=none,id=target" -device virtio-blk-pci,drive=target,bootindex=0 \
  -drive "file=$WORK/maitre-installer.qcow2,format=qcow2,if=none,id=installer,snapshot=on" -device virtio-blk-pci,drive=installer,bootindex=1 \
  -netdev "user,id=n0,hostfwd=tcp:127.0.0.1:${FWD_PORT}-:7443,hostfwd=tcp:127.0.0.1:${WEB_PORT}-:80" -device virtio-net-pci,netdev=n0 \
  &
QEMU=$!

cat <<MSG

  VM agent port:  127.0.0.1:${FWD_PORT}   (enter this in the Console when it cannot reach the VM's 10.0.2.x address, or in "Set up by address")
  VM web port:    http://127.0.0.1:${WEB_PORT}/  with Host: <service>.maitre.local
  Serial log:     $WORK/serial.log
  CLI instead of the Console:
    ./server/bin/maitre provision --identity $WORK/device --addr 127.0.0.1:${FWD_PORT} --fleet Dev --name vm --disk /dev/vda --save-node-pub $WORK/node.pub

Ctrl-C stops the VM. Start the Console with: make desktop
MSG

wait "$QEMU"
