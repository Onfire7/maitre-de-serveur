---
status: accepted
---

# Service status and metrics travel by gossip; the Console reads the Fleet through one anchor Node

Every Node keeps, in memory only, the status of its own Services (status word, message, restart count, healthy flag, port-forward state, image digests) and a sample of its own metrics. It sends the status to every peer whenever it changes (coalesced to one send per Service per two seconds) and the metrics sample every five seconds, over the existing peer connection. Each Node remembers the last thing it heard from every peer, with the time it heard it. A new `GetFleetStatus` RPC returns that whole picture from any one Node: every Node's last metrics and Service statuses with their age, and the answering Node's view of which peers it can reach. Nothing of this is ever written to SQLite, so it never enters the record sync or the config-partition backup.

The Console picks one reachable Node as its anchor (the last one used, then the first Healthy Node by letter), reads all Fleet-wide state from it (synced records plus `GetFleetStatus`) on one short poll, and moves to the next Node within a poll interval when the anchor stops answering. Writes keep the existing forward-to-owner path through the anchor. Calls that are per-machine by nature (the Service web tunnel, logs, installer progress, file manager, Drive operations, a Node's own update and reboot) go straight to the owning Node. A Node is Unreachable when the anchor has not heard from it for 60 seconds; the anchor itself is judged by the Console. (Design questions 152 and 156-163.)

## Considered options

- **Console polls every Node and merges**: what the Console does today, per panel. Every screen then needs N connections, offline Nodes make every list wait on a timeout, and a Fleet-wide sidebar means the Console re-implements peer discovery. Rejected by the user: whatever the Console needs from every Node all the time should be broadcast or synced so one Node can serve it.
- **Status as a synced record** (`service_status` kind, written by the running Node): fits the record model and would survive a restart, but every record write is pushed to every peer and, five seconds later, rewrites the config-partition backup on every Drive. A restart-looping Service would churn disks across the Fleet, and excluding one kind from backups and restores adds a special case to the sync layer. Rejected by the user: status is in-memory state that is synced, not something committed to disk.
- **Metrics as records**: the same churn at a sample every few seconds, permanently. Never seriously considered.
- **Every Node reachable through every other (full relay)**: more general than an anchor, but on a home LAN the Console can reach the Nodes it needs directly, and relaying large or streaming calls (the web tunnel, rsync progress) would double the traffic for nothing.

## Consequences

- The peer layer gains a small ephemeral message type beside record pushes, and a per-peer "last heard" table that `GetFleetStatus` serialises. A Node that restarts starts with no statuses for its peers and fills them within one metrics interval.
- A freshly reachable Node whose Services have not reported yet shows them with an unknown status for up to two seconds; a Node unheard for 30 seconds has its gauges dimmed, for 60 seconds is Unreachable and its Services take the Stopped look.
- The Console has one Fleet-wide query and a handful of page-scoped direct queries instead of a timer per panel. The demo Fleet implements `GetFleetStatus` so the UI can be built before the agent work lands.
- Two opinions about reachability would flap, so the Console does not second-guess the anchor; a split LAN shows whatever the anchor sees.
