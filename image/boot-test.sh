#!/usr/bin/env bash
# Build a qcow2 from the bootc image with bootc-image-builder, boot it in
# QEMU, and wait for the agent to log "listening" on the serial console.
# Requires: podman (rootful, for bootc-image-builder), qemu-system-x86_64.
set -euo pipefail
IMAGE=${1:?image ref}
OUT=${OUT:-$(mktemp -d)}
TIMEOUT=${TIMEOUT:-300}

# bootc-image-builder reads from root's image store. If the image was built
# rootless (as in CI), copy it over first.
if ! sudo podman image exists "$IMAGE"; then
  echo "copying $IMAGE into rootful podman storage"
  podman save "$IMAGE" | sudo podman load
fi

sudo podman run --rm --privileged --security-opt label=type:unconfined_t \
  -v "$OUT:/output" -v /var/lib/containers/storage:/var/lib/containers/storage \
  quay.io/centos-bootc/bootc-image-builder:latest \
  --type qcow2 --rootfs xfs --local "$IMAGE"

DISK=$(find "$OUT" -name '*.qcow2' | head -1)
echo "booting $DISK"
timeout "$TIMEOUT" qemu-system-x86_64 \
  -m 2048 -smp 2 -nographic -enable-kvm -cpu host \
  -drive "file=$DISK,format=qcow2,if=virtio" \
  -netdev user,id=n0 -device virtio-net-pci,netdev=n0 \
  2>&1 | tee "$OUT/serial.log" | grep -m1 -E 'maitre.*listening' \
  && { echo "boot-test: agent came up"; exit 0; }
echo "boot-test: agent did not come up within ${TIMEOUT}s"; tail -50 "$OUT/serial.log"; exit 1
