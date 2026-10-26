# What the Console needs from the RPC and the Nodes

The running list of changes the Console (desktop/) needs on the agent side
(api/ and server/), kept as the UI is built against the demo Fleet
(decision 153). Each item says what the UI wants to do, what is missing,
and its state. When an item is built, say where; keep it here so the reason
survives.

## Open

- **Move a Service volume to another Space.** The Storage sheet on a
  Service lists its volumes and their Spaces but cannot change one. Needed:
  an RPC (say `ServiceService.MoveVolume{name, volume, to_space, to_node_id?}`)
  that stops the Service, moves the folder through the file manager's job
  machinery (copy, verify, remove), rewrites `ServiceSpec.volume_spaces`,
  and restarts; it should return a `FilesService` job id so the Moves panel
  shows progress. Until then the sheet says so and links to Files.
- **Gossip a Node's own Provisioning state.** While a new Node installs, the
  sidebar shows it as Provisioning from the Installer's progress only on
  the Console that started it. Other Consoles see nothing until the Node
  record arrives. Nice to have: the sponsor Node gossips "provisioning:
  name, percent" from the Installer status it can read.
- **Node status words beyond Healthy, Unreachable, Attention.** Updating
  (an image update in progress, from `UpdateStatus.state`) and Attention for
  a failed update or a rollback are read today with a direct
  `GetUpdateStatus` per Node. Add `UpdateStatus.state` and `last_rollback`
  to `FleetNodeStatus` so the anchor answers for every Node (decision 136).
- **Unreachable from the anchor's view only.** `FleetNodeStatus.reachable`
  comes from the sync layer's per-peer `online`, which flips false only when
  one of our own calls fails. A peer that stops gossiping but was never
  called stays `online` until the 20 s reconcile. Consider marking a peer
  offline when nothing was heard for 60 s (decision 162).

## Built

- **`GetFleetStatus` (ADR 0006).** Any Node answers for the Fleet: its own
  metrics and Services, every peer's reachability and last gossip. In
  `nodeservice.GetFleetStatus`.
- **`PushStatus` gossip.** Metrics every 5 s; Service statuses on change
  (checked every 2 s) or every 5 minutes; a metrics-only message keeps the
  statuses heard before. In `cmd/maitre/agent.go` and `peers.Manager`
  (`Gossip`, `Received`, `Last`).
- **`FilesService` scopes a spanning Space from any holder.** The Files tab
  lists a spanning Space once, under the Fleet, and reads it through the
  anchor when the anchor has it. No agent change was needed: the merged
  Pool is the same from any Node that has the Space.
