# What the Console needs from the RPC and the Nodes

The running list of changes the Console (desktop/) needs on the agent side
(api/ and server/), kept as the UI is built against the demo Fleet
(decision 153). Each item says what the UI wants to do, what is missing,
and its state. When an item is built, say where; keep it here so the reason
survives.

## Open

- **Gossip a Node's own Provisioning state.** While a new Node installs, the
  sidebar shows it as Provisioning from the Installer's progress only on
  the Console that started it. Other Consoles see nothing until the Node
  record arrives. Nice to have: the sponsor Node gossips "provisioning:
  name, percent" from the Installer status it can read.
## Built

- **Drive fill in the gossip.** `NodeMetrics.drives` (`DriveUsage`:
  drive id, total, used) lists every mounted Drive the Node owns, read with
  statfs on each sample, so the sidebar's Node flyout shows every Node's
  Drives from the anchor without calling each Node. In `metrics.Collector`
  (`Drives`) and `cmd/maitre/agent.go`.

- **Move a Service volume to another Space.** `ServiceService.MoveVolume
  {name, volume, to_space}` on the Service's Node: stops the Service, queues
  the folder move through the file manager (`files.Manager.MoveThen`, which
  calls back when the job ends), rewrites `volume_spaces` and the Quadlet
  unit on success, and starts the Service again if it was running; a failed
  or cancelled move leaves the spec alone and restarts it on the old place.
  Returns the job id. Same-Node only: the destination Space needs a Pool on
  the Service's Node. The Storage sheet offers it per volume.

- **Update state in the gossip.** `FleetNodeStatus` carries the Node's
  update state, message, progress, whether one is staged, and the last
  rollback, so the anchor says Updating or Attention for every Node and the
  Overview lists failed, rolled-back and staged updates. The Node reads
  `bootc status` at most every 30 s for this (5 s while updating).
- **Silent peers go offline.** `peers.Manager.Status` reports a peer online
  only when its last answered call or gossip is under `SilentFor` (60 s),
  so a Node that stopped talking is Unreachable without a failed call.

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
