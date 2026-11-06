#!/bin/bash
# greenboot wanted check (decision 324): say how much room the root
# filesystem has left, and warn when an update could no longer be staged.
#
# This is a wanted check and not a required one on purpose. greenboot
# answers a failed required check by rolling the image back, and a rollback
# does not free a single byte: a full root would cost three reboots and fix
# nothing. The required check beside this one passes a full disk happily,
# since podman answers `podman info` right up to the moment it has to write
# — which is how both Nodes of the Fleet wedged on 2026-11-04.
#
# Only statfs is read, never the store itself: `du` over an image store of
# tens of thousands of layers would be slow on every boot.
set -u

# Not / : on a booted bootc Node that is the deployment's composefs image,
# a few megabytes and full by construction, so this check would fail on
# every boot and say a Node with 30 GB free could not stage an update.
# Measured on Boulder: df / answered 8.0M, 100% used, while the partition
# underneath had 32G. The partition is /sysroot, where ostree keeps its
# repo and /var. Which to read is asked rather than assumed — the larger of
# the two — so this is also right on a system with no /sysroot at all.
fs_field() { df -B1 --output="$1" "$2" 2>/dev/null | tail -1 | tr -d ' '; }
root=/
sysroot_size=$(fs_field size /sysroot)
root_size=$(fs_field size /)
if [ -n "$sysroot_size" ] && [ -n "$root_size" ] && [ "$sysroot_size" -gt "$root_size" ]; then
  root=/sysroot
fi

avail=$(fs_field avail "$root")
size=$(fs_field size "$root")
store=$(podman info --format '{{.Store.GraphRoot}}' 2>/dev/null) || store=""
[ -n "$store" ] || store=/var/lib/containers/storage

# An OS image is about 2 GB installed and an update stages another copy;
# decision 324 reserves 110% of the image for exactly this.
need=$((3 * 1024 * 1024 * 1024))

printf 'maitre space: %s has %s free of %s; the image store is %s\n' \
  "$root" "$(numfmt --to=iec "$avail")" "$(numfmt --to=iec "$size")" "$store"

if [ "$avail" -lt "$need" ]; then
  printf 'maitre space: less than %s free on the OS filesystem: an update cannot be staged, and a Service that restarts will pull its image again for as long as it fails\n' \
    "$(numfmt --to=iec "$need")" >&2
  exit 1
fi
