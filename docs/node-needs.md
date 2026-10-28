# What the Console needs from the RPC and the Nodes

The running list of changes the Console (desktop/) needs on the agent side
(api/ and server/), kept as the UI is built against the demo Fleet
(decision 153). Each item says what the UI wants to do, what is missing,
and its state. When an item is built, say where; keep it here so the reason
survives.

## Open

Nothing.

## Built

- **Every Console sees a Node being installed.** Built without gossip or a
  new RPC: the Console that starts an install already puts the new Node's
  record through a sponsor, and now writes the Installer's address into it
  (the Installer listens on the Node port, and its key becomes the
  node_id). Every Node that holds a record made within the last hour from
  a Node it never heard from asks that address's `GetInstallerStatus`
  every 5 s and reports it as `FleetNodeStatus.provisioning`, so the anchor
  answers for the install itself. The Console says Provisioning with the
  percent and step, or "install failed" as a notice. In
  `peers.Manager.WatchInstalls` and `nodeservice.GetFleetStatus`. The Node
  row of the Console that started it says Provisioning even when the
  anchor is too old to report it.
- **`GetFleetStatus` passes peers' update state on.** It copied only
  metrics and Services from a peer's gossip, so the anchor never said
  Updating or Attention for another Node; it now copies the update fields
  too.

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
- **Update availability (`UpdateStatus.available`, `FleetNodeStatus.update_available`).**
  Every Node asks its registry (`skopeo inspect`) for the newest digest of
  its booted reference at start and every 15 minutes, whatever the update
  policy, and reports it when it neither runs nor has staged it; it rides
  the gossip like the rest of the update state. The Console shows Update
  left of Restart on that Node's card and a line in its flyout. In
  `rollout.Runner.Check` and `rollout.Newer`. Older Nodes never set it, so
  the button stays hidden there; Update… in the details still works.
- **`FilesService.MakeDir`.** The file manager's New folder: one folder in a
  Space's Pool (the create policy picks the Drive) or on a Drive root; the
  parent must exist; names starting with a dot are refused, since ListDir
  hides them. In `files.Manager.MakeDir`. Older Nodes answer Unimplemented
  and the Console says the Node needs updating.
- **File contents, Zip and Unzip (decisions 177, 180, 183, 184).**
  `GET`/`PUT /maitre/files/content` on the mutual-TLS listener (ranges for
  previews and video; a folder downloads as a zip made on the fly; `PUT`
  never replaces), and `FilesService.Zip`/`Unzip` as jobs beside moves
  (`Job.kind`, `Job.summary`). In `files/http.go` and `files/archive.go`.
  Older Nodes have none of it: previews fail with a 404 and Zip/Unzip say
  the Node needs updating.
- **Service folders (`SetFolders`, `volume_folders`, `mounts`) and Space owners
  (`Space.owner_id`).** Decisions 170-176, 182. In `services.apply`,
  `services.SetFolders`, `internal/owner`, `files/owner.go`. Needs `acl` in
  the image.
- **`FilesService.Delete` (decision 186).** In `files/delete.go`. Older Nodes say they need updating.
- **`NodeService.GetNodeLogs` (decision 187).** In `nodeservice/logs.go`. Older Nodes: the sheet says the Node needs updating.
- **`ServiceService.SetSettings` (decision 193).** In `services.SetSettings`: replaces a Service's settings and, when given, its auto update; renders the unit before saving, so a setting the container definition needs cannot go missing; `systemctl try-restart`. Trusted Devices only (not in `gate.NodeCalls`). Older Nodes: the Console says changing settings needs a newer Maître Server.
- **`ServiceService.SetImage` (decision 195).** In `services.SetImage`: refuses anything but a plain image reference (no spaces or line breaks), pulls it and pins a tag to its digest, keeps the old image as `previous_image` for Rollback, renders before saving (another repository needs its grants, so it may ask for them), then `systemctl try-restart`. Trusted Devices only. Older Nodes: the Console says changing the image needs a newer Maître Server.
- **Space placements `least_free` and `least_free_percent` (decision 201).** `storage.ValidateSpace` accepts them; `pool.CreatePolicy` maps `least_free` to mergerfs `lfs`, and `least_free_percent` to `ff` over branches ordered by their share of free space, fullest first (`pool.ByFreeShare`, refreshed whenever the Pool is assembled). An older anchor refuses the Space record ("placement must be …"), and the Console says the Node needs updating; an older peer that receives it falls back to `mfs`.
