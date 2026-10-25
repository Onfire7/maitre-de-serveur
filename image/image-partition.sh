#!/usr/bin/env bash
# Build the maitre-image partition (decision 76): an ext4 filesystem image
# holding the OCI layout of LOCAL_IMAGE at image/ (tag "media") and
# image-ref.txt naming REF, the registry reference it was built from. Needs
# no root: podman writes the layout, mke2fs -d packs it.
#
#   image/image-partition.sh LOCAL_IMAGE REF OUT.img
set -euo pipefail
IMAGE=${1:?local image}; REF=${2:?image ref}; OUT=${3:?output file}
WORK=$(mktemp -d); trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/root/image"
podman push -q "$IMAGE" "oci:$WORK/root/image:media"
echo "$REF" > "$WORK/root/image-ref.txt"
# 5% headroom over the layout size, at least 64 MB.
SIZE_KB=$(du -sk "$WORK/root" | cut -f1); SIZE_KB=$(( SIZE_KB + SIZE_KB / 20 + 65536 ))
rm -f "$OUT"; truncate -s "${SIZE_KB}K" "$OUT"
mke2fs -q -t ext4 -L maitre-image -d "$WORK/root" -E root_owner=0:0 "$OUT"
echo "$OUT"
