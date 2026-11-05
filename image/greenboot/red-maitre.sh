#!/bin/bash
# greenboot red script: a boot failed its checks. Record it for the Console
# (UpdateStatus.last_rollback); greenboot itself arranges the rollback.
#
# Decision 325: past the point where the rollback has had its turn, ask the
# next boot for rescue. Until now a Node could only be put into rescue by a
# running agent, from its own screen — so the one case that most needs it,
# an agent that will not come up, could not ask. greenboot runs this at
# exactly the right moment and the request is a file on the config
# partition, which both deployments share, so it survives the rollback and
# the rolled-back image honours it.
#
# Not on the first red boot: an unrelated failure would take a Node off the
# network (rescue runs with networking off, deliberately, so that nothing
# can drive it) and need someone standing at the machine. greenboot counts
# in the bootloader environment, so rather than depend on its internals
# this keeps its own tally beside last-rollback. recoverymode clears the
# request as it reads it, so a crash loop cannot hold a Node in rescue.
set -u

state=/var/lib/maitre
date -u +"%Y-%m-%dT%H:%M:%SZ boot failed health checks; previous image restored" > "$state/last-rollback"

# How many red boots in a row. greenboot gives three tries before it rolls
# back, so the fourth is the one where rolling back did not help.
reds=0
[ -r "$state/red-boots" ] && reds=$(cat "$state/red-boots" 2>/dev/null || echo 0)
case $reds in ''|*[!0-9]*) reds=0 ;; esac
reds=$((reds + 1))
echo "$reds" > "$state/red-boots"

if [ "$reds" -ge 4 ]; then
  date -u +"%Y-%m-%dT%H:%M:%SZ" > "$state/recovery-requested"
  echo "maitre: $reds failed boots and a rollback that did not help; the next boot enters rescue" >&2
fi
