---
status: accepted
---

# A permanent core of the Node API decides whether a Node is online; every other call only decides whether it is healthy

Five `NodeService` calls form the core: `GetNodeInfo`, `GetUpdateStatus`, `UpdateImage`, `Rollback` and `Reboot`, with their request and response messages. They are frozen to a minimum standard: a field is never removed or renumbered, never changes meaning, and no Node ever refuses a core request an older caller could send. New optional fields may be added. Every Node keeps answering the core, so any Node, however old, can be seen, updated, rolled back and restarted, by a Console or a peer of any later version.

A peer is online when it answered `GetNodeInfo` (each Node asks every peer every 15 s) or sent its status within the last 60 s. Any answer counts, even an error, because an error the Node sent back proves it is there; only a call that reached nobody (dial, TLS, timeout) counts against it. No other call changes whether a peer is online. When a peer answers some other call with an error, that is a problem with the peer, worded for the Console ("runs Maître Server 95c3d8a, which cannot answer PushStatus; update it", "refused PushRecords: ..."), reported in `PeerStatus.problems` and `FleetNodeStatus.problems` with the peer's version, and shown as Attention. A problem clears when the same call next succeeds or the peer's version changes. The Console offers Update and Rollback on a Node page whenever its own direct `GetUpdateStatus` call to that Node answers, whatever the anchor reports. (Decisions 165 to 167.)

## Why

Before this, any failed peer call marked the peer offline. During a rolling update the Fleet runs two versions, so a call the new version added (gossip, here) failed against every old Node with "not implemented", and each side showed the other as Unreachable. The Console hides Update for an Unreachable Node, so a version mismatch could take away the one action that fixes it. The same happened when an old Node sent something a newer one rejects.

## Considered options

- **Treat only "not implemented" as alive**: fixes the case that happened, but a newer Node rejecting an old Node's records would still make it look offline.
- **Version negotiation (each Node advertises the calls it supports)**: lets callers skip what a peer lacks, but needs its own frozen call anyway, and failures other than missing calls still need a place to go.
- **Online from any answered call, offline from any failed one**: the rule this replaces.

## Consequences

- Changing a core call's messages in a way an older caller would notice is a breaking change to the Fleet and is not allowed; anything new goes in a new call, which may fail against old Nodes without harm.
- A Node that only answers the core is shown as Attention with the reason and stays updatable, which is what an old Node in a mixed Fleet needs.
- Liveness costs one small call per peer every 15 s on top of gossip.
- ADR 0006 still holds: the Node's word comes from the anchor and the Console does not probe Nodes to second-guess it. The Update and Rollback controls were already direct calls to the owning Node (decision 160); they now depend only on that call answering.
- Only Nodes running this version judge their peers this way; a Node from before it still marks a peer offline when a call fails, until it is updated.
