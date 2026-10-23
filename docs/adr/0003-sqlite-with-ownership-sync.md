---
status: accepted
---

# Fleet state is SQLite on every Node with an ownership-based sync layer

The Fleet must be masterless: connecting to any Node must just work, and a one- or two-Node Fleet must be valid. The Fleet database (Node identities, Trusted Devices, Service definitions, Tiers, exposure rules, settings, event log) is kilobytes and changes a few times a day. Every record has exactly one owning Node. Every Node holds a full copy in SQLite, synced over the existing mTLS connections; reads are local, writes are forwarded to the owner or queued while it is offline, and the newest version wins for the rare Fleet-wide records. The sync layer is ours and deliberately small.

## Considered options

- **Single Primary Node**: simple but needs the user to notice and promote a replacement, which non-technical users will not do.
- **rqlite or dqlite (Raft)**: needs an odd-numbered quorum, so a two-Node Fleet cannot survive one Node being off.
- **cr-sqlite or a CRDT library**: general-purpose conflict resolution for data that is naturally partitioned by owner. More machinery than the data warrants.

## Consequences

Metrics and logs stay on the Node that produced them and are not part of the Fleet database. Edits to an offline Node's own records wait until it returns. Concurrent edits to a Fleet-wide record resolve newest-wins with a notice in the Console, no merge UI.
