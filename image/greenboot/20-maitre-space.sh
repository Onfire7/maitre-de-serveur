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

avail=$(df -B1 --output=avail / | tail -1 | tr -d ' ')
size=$(df -B1 --output=size / | tail -1 | tr -d ' ')
store=$(podman info --format '{{.Store.GraphRoot}}' 2>/dev/null) || store=""
[ -n "$store" ] || store=/var/lib/containers/storage

# An OS image is about 2 GB installed and an update stages another copy;
# decision 324 reserves 110% of the image for exactly this.
need=$((3 * 1024 * 1024 * 1024))

printf 'maitre space: root has %s free of %s; the image store is %s\n' \
  "$(numfmt --to=iec "$avail")" "$(numfmt --to=iec "$size")" "$store"

if [ "$avail" -lt "$need" ]; then
  printf 'maitre space: less than %s free on root: an update cannot be staged, and a Service that restarts will pull its image again for as long as it fails\n' \
    "$(numfmt --to=iec "$need")" >&2
  exit 1
fi
