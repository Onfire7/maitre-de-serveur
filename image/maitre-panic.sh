#!/bin/sh
# Put whatever the last panic left in the ramoops buffer into the journal.
#
# A Node has no terminal (decision 72), so a panic that only ever reaches
# the monitor reaches nobody. The kernel keeps its last words in the named
# memory region reserved by image/kargs.d/10-maitre.toml, and they survive
# the warm reboot that panic=30 performs. Writing them to stdout under this
# unit makes them an ordinary journal source, which the Console can already
# ask for over GetNodeLogs -- no new RPC, and nothing to read off a screen.
#
# Runs before systemd-pstore.service, which archives these files to
# /var/lib/systemd/pstore and empties the buffer. That ordering is what
# makes each record reach the journal exactly once: if it ever fails, the
# records are still archived to disk and nothing is lost for good.
set -u

found=0
for f in /sys/fs/pstore/*; do
	[ -f "$f" ] || continue
	found=1
	echo "=== $f ==="
	cat "$f" 2>&1
done

if [ "$found" = 0 ]; then
	echo "no saved panic: the previous boot ended cleanly, or the buffer did not survive"
fi
