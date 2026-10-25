#!/usr/bin/env bash
# Build installer media (a qcow2 disk image) from a Maître Server image.
# Decision 68: the media is the same bootc image with kernel arguments that
# select installer mode and name the image the installer will pull.
#
#   image/installer-media.sh LOCAL_IMAGE INSTALL_IMAGE OUT_DIR [extra kargs...]
#
# LOCAL_IMAGE   image ref in podman storage to turn into media
# INSTALL_IMAGE image ref the installer pulls onto the target disk (a registry
#               reachable from the booted machine, e.g. ghcr.io/onfire7/maitre-server:main)
# Requires rootful podman (bootc-image-builder). Prints the qcow2 path.
set -euo pipefail
IMAGE=${1:?local image}; INSTALL=${2:?install image ref}; OUT=${3:?output dir}; shift 3
mkdir -p "$OUT"
if ! sudo podman image exists "$IMAGE"; then
  echo "copying $IMAGE into rootful podman storage" >&2
  podman save "$IMAGE" | sudo podman load >&2
fi
for i in $(seq 1 10); do
  sudo podman pull quay.io/centos-bootc/bootc-image-builder:latest >/dev/null 2>&1 && break
  [ "$i" -lt 10 ] || { echo "installer-media: could not pull bootc-image-builder" >&2; exit 1; }
  echo "pull failed; retrying in 30s" >&2; sleep 30
done
cat > "$OUT/config.toml" <<TOML
[customizations.kernel]
append = "maitre.installer maitre.image=${INSTALL} $*"
TOML
sudo podman run --rm --privileged --security-opt label=type:unconfined_t \
  -v "$OUT:/output" -v "$OUT/config.toml:/config.toml:ro" \
  -v /var/lib/containers/storage:/var/lib/containers/storage \
  quay.io/centos-bootc/bootc-image-builder:latest \
  --type qcow2 --rootfs xfs --config /config.toml --local "$IMAGE" >&2
sudo chown -R "$(id -u):$(id -g)" "$OUT"
Q=$(find "$OUT" -name '*.qcow2' | head -1)
[ -n "$Q" ] || { echo "installer-media: no qcow2 produced" >&2; exit 1; }
# Decision 76: append a partition carrying the image itself, so the install
# needs no download when the registry has nothing newer (or is unreachable).
PART=$OUT/maitre-image.img
"$(dirname "$0")/image-partition.sh" "$IMAGE" "$INSTALL" "$PART" >/dev/null
RAW=$OUT/media.raw
qemu-img convert -O raw "$Q" "$RAW"
PSIZE=$(stat -c %s "$PART")
START=$(( ( $(stat -c %s "$RAW") + 1048575 ) / 1048576 * 1048576 ))
truncate -s $(( START + PSIZE + 1048576 * 2 )) "$RAW"
# GPT: move the backup header to the new end (util-linux 2.36+), then append the partition.
sfdisk --relocate gpt-bak-std "$RAW" >/dev/null
echo "start=$(( START / 512 )), size=$(( PSIZE / 512 )), type=0FC63DAF-8483-4772-8E79-3D69D8477DE4, name=maitre-image" | sfdisk --append --no-reread --no-tell-kernel "$RAW" >/dev/null
dd if="$PART" of="$RAW" bs=1M seek=$(( START / 1048576 )) conv=notrunc status=none
qemu-img convert -O qcow2 "$RAW" "$Q"
rm -f "$RAW" "$PART"
echo "$Q"
