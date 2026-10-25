#!/usr/bin/env bash
# Developer VM without hardware or podman: boot Maître Server in QEMU.
# Run Maître Desktop separately with `make desktop`.
#
#   make vm                  # first run: boots the installer; later runs: boots the Node you installed
#   make vm FRESH=1          # wipe the virtual disk and start over from the installer
#   RUN=<ci run id> ...      # use a specific CI run's installer media instead of the latest on main
#   DISPLAY_BACKEND=none ... # headless: no VM window, drive it with the maitre CLI
#   make vm PXE=1 FRESH=1    # no installer media: boot from the network like a real machine
#                            # (run `make depot NETBOOT=1` first; see README)
#
# Ctrl-C stops the VM. The installer media comes from CI (the `installer-media`
# artifact of the image job), because building the image needs podman. The
# installer pulls the OS from ghcr.io, so the VM needs internet access.
#
# Networking: after `scripts/vm-net-setup.sh` (one-time, sudo) the VM sits on
# the host-only bridge maitre0 with its own 10.42.0.x address, the Console
# discovers it over mDNS and <service>.local resolves from the host. Without
# the bridge, QEMU's user-mode NAT is used: the Console cannot reach the
# 10.0.2.x address the VM advertises, so enter 127.0.0.1:${FWD_PORT} when it
# asks. NET=user forces NAT even when the bridge exists. The VM's display
# window is the machine's monitor: the pairing code shows there.
set -euo pipefail
cd "$(dirname "$0")/.."
WORK=${WORK:-$HOME/.maitre/qemu}
FWD_PORT=${FWD_PORT:-17443}
WEB_PORT=${WEB_PORT:-18080}
DISK_SIZE=${DISK_SIZE:-40G}
BRIDGE=${BRIDGE:-maitre0}
NET=${NET:-auto}
DEPOT=${DEPOT:-$HOME/.maitre/depot}
PXE=${PXE:-}
REPO=${REPO:-Onfire7/maitre-de-serveur}
mkdir -p "$WORK"

need() { command -v "$1" >/dev/null || { echo "vm: missing $1"; exit 1; }; }
need qemu-system-x86_64; need qemu-img; need gh
[ -r /dev/kvm ] && [ -w /dev/kvm ] || { echo "vm: /dev/kvm not accessible (add yourself to the kvm group)"; exit 1; }

# 1. Installer media from CI. PXE mode boots from the Depot instead and only
#    needs the firmware, so it skips the download once that is present.
if [ -n "$PXE" ] && [ -f "$WORK/OVMF_CODE.fd" ]; then
  :
elif [ -n "${RUN:-}" ] || [ ! -f "$WORK/maitre-installer.qcow2" ] || [ -n "${FRESH:-}" ]; then
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
[ -n "$PXE" ] || echo "installer media: $WORK/maitre-installer.qcow2 (installs $(cat "$WORK/image-ref.txt"))"

# The installer pulls from GHCR anonymously; the package must be public.
[ -n "$PXE" ] || {
IMG=$(cat "$WORK/image-ref.txt"); PKG=${IMG#ghcr.io/}; TAG=${PKG##*:}; PKG=${PKG%%:*}
TOKEN=$(curl -fs "https://ghcr.io/token?scope=repository:${PKG}:pull" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
if ! curl -fs -o /dev/null -H "Authorization: Bearer $TOKEN" \
     -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json' \
     "https://ghcr.io/v2/${PKG}/manifests/${TAG}"; then
  echo "vm: ghcr.io/${PKG} is not publicly pullable."
  echo "  Make the package public once: https://github.com/users/${PKG%%/*}/packages/container/${PKG#*/}/settings"
  exit 1
fi
}

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

if [ "$NET" = auto ]; then
  if ip link show "$BRIDGE" >/dev/null 2>&1 && grep -qx "allow $BRIDGE" /etc/qemu/bridge.conf 2>/dev/null; then NET=bridge; else NET=user; fi
fi
# PXE: the NIC is the second boot device instead of the installer media. On
# the bridge the Depot's proxyDHCP answers the VM like any other machine; on
# NAT, QEMU's own TFTP hands GRUB out of the Depot's boot directory and GRUB
# fetches the rest over HTTP from the Depot on the host's LAN address.
NICBOOT=; INSTALLER_DRIVE=(-drive "file=$WORK/maitre-installer.qcow2,format=qcow2,if=none,id=installer,snapshot=on" -device virtio-blk-pci,drive=installer,bootindex=1)
SLIRP_PXE=
if [ -n "$PXE" ]; then
  [ -f "$DEPOT/boot/grubx64.efi" ] || { echo "vm: no netboot files in $DEPOT/boot (run: make publish NETBOOT=1)"; exit 1; }
  NICBOOT=",bootindex=1"; INSTALLER_DRIVE=()
  SLIRP_PXE=",tftp=$DEPOT/boot,bootfile=/grubx64.efi"
fi
if [ "$NET" = bridge ]; then
  # Fixed MAC so the VM keeps its DHCP lease across runs.
  NETDEV=(-netdev "bridge,id=n0,br=$BRIDGE" -device "virtio-net-pci,netdev=n0,mac=52:54:00:4d:41:49${NICBOOT}")
  echo "network: bridge $BRIDGE (the VM gets its own address; the Console discovers it)"
  [ -n "$PXE" ] && echo "netboot: the VM asks the LAN; run 'make depot NETBOOT=1' so the Depot answers"
else
  NETDEV=(-netdev "user,id=n0,hostfwd=tcp:127.0.0.1:${FWD_PORT}-:7443,hostfwd=tcp:127.0.0.1:${WEB_PORT}-:80${SLIRP_PXE}" -device "virtio-net-pci,netdev=n0${NICBOOT}")
  echo "network: QEMU NAT with ports forwarded (run scripts/vm-net-setup.sh once for a bridged VM)"
  [ -n "$PXE" ] && echo "netboot: GRUB from $DEPOT/boot via QEMU's TFTP; run 'make depot' so GRUB can fetch the kernel and root"
fi

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
  "${INSTALLER_DRIVE[@]}" \
  "${NETDEV[@]}" \
  &
QEMU=$!

if [ "$NET" = bridge ]; then
  cat <<MSG

  The VM is on bridge $BRIDGE and shows its address on its screen. The Console
  finds it by itself; Services open as http://<name>.local/ from this machine.
  Serial log: $WORK/serial.log
  CLI instead of the Console (replace ADDR with the address on the VM's screen):
    ./server/bin/maitre provision --identity $WORK/device --addr ADDR:7443 --fleet Dev --name vm --disk /dev/vda --save-node-pub $WORK/node.pub

Ctrl-C stops the VM. Start the Console with: make desktop
MSG
else
  cat <<MSG

  VM agent port:  127.0.0.1:${FWD_PORT}   (enter this in the Console when it cannot reach the VM's 10.0.2.x address, or in "Set up by address")
  VM web port:    http://127.0.0.1:${WEB_PORT}/  with Host: <service>.local
  Serial log:     $WORK/serial.log
  CLI instead of the Console:
    ./server/bin/maitre provision --identity $WORK/device --addr 127.0.0.1:${FWD_PORT} --fleet Dev --name vm --disk /dev/vda --save-node-pub $WORK/node.pub

Ctrl-C stops the VM. Start the Console with: make desktop
MSG
fi

wait "$QEMU"
