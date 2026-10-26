# What the Console UI needs to implement

Written for the Desktop UI sprint (2026-10-26). The agent side of each item
exists; this lists what a screen has to show and call. RPC names are in
`api/maitre/v1/*.proto`. Nothing here needs a new RPC unless marked.

## Storage Bins (decisions 121-128)

Records of kind `bin` (message `Bin`), read with `ListRecords{kind:"bin"}`,
written with `PutRecord` (the Node validates: id, name, placement, and a rule
when exclusive). Drives are `ListDrives` on each Node (live) and records of
kind `drive` (Fleet-wide view).

- **Bin list.** Name, rule (tags, Nodes, or "every Drive"), placement
  (local first / most free space / highest free share), spans Nodes,
  exclusive (lock), default for new volumes (exactly one; writing it on one
  Bin should clear it on the others: the UI does that with two PutRecords).
  Members: Drives by Node, from `storage.BinsOf` mirrored as `binsOf` in
  `desktop/src/bins.tsx`.
- **Capacity per Bin (125).** Used: sum of the Bin's folder sizes (needs the
  file manager's `Usage` RPC below). Free: the free space of the Bin's unique
  Drives (`Drive.free_bytes`), with a note that Bins on the same Drives share
  it.
- **Fleet map (126).** One box per Node with its letter; one box per Drive
  with a stripe per Bin it serves and a lock for exclusive membership; hover
  a Bin to light its members; typing a rule previews what it would take.
- **Empty states (123).** No Bins: Services cannot install; offer "Create your
  first Bin" with the built-in Drives selected (rule: every Drive, local
  first, default for new volumes). Bins but none default: say so on the
  Services page.
- **Removing a Bin (124).** Confirm; say the folders stay on the Drives and
  appear as "folder of a removed Bin"; offer to open the file manager there.
- **Drive card.** Its Bins; its folders from `Drive.folders` with kind
  bin / orphan / unsorted; an orphan or unsorted folder offers "open in the
  file manager" and never a delete.

## File manager (decision 122) — `FilesService`

Scope selectors: a Bin (the merged view of its Pool on a Node) or a Drive
(its root: pre-adoption files and orphan folders).

- `ListDir{scope, path}` → entries with name, dir flag, size (files),
  modified time, and locations (Node and Drive holding a copy; for a remote
  branch the Node only). Directory sizes come from `Usage{scope, path}` on
  demand (computed with `du`, cached a minute); show a spinner per row.
- Selecting a file or folder shows where it is (Node, Drive) and its size.
- **Move** (`Move`): destination is a Bin (same relative path by default,
  editable), optionally a specific Drive of that Bin on this or another
  Node. Returns a job id. `ListJobs` shows progress (bytes, files, current
  path, state: queued / running / done / failed with message); `CancelJob`.
  Moves copy then delete; a failed move leaves the source untouched.
- Unsorted files on an adopted Drive are the main use: pick a folder, choose
  a Bin, go.

## Node

- Rename (`RenameNode`): a Rename control on the Node page with a dice
  button drawing from the Node's letter (`randomNodeName` in
  `desktop/src/names.ts`); taken names are every Node and Drive name.
- Setup: the new Node's letter is `nextLetter` of the existing Nodes'
  letters; the name defaults to a draw from that letter.
- Recovery trust (120): `TrustedDevice.node_scope` on this device's record
  means "trusted by one Node only"; show the banner with the trusting Node
  and the 24-hour deadline, and a way to enter a second Node's code
  (`CodeScreen` with `promote`).

## Services

- Volumes pick a Bin at install (`ServiceSpec.volume_bins`), defaulting to
  the Bin marked default; the row shows "volume in Bin".
- Exposure dialog: private / remote / public via Tailscale / public at your
  own address, with the ACME email field (Fleet-wide `public_settings`),
  the UPnP step (`NodeInfo.port_mapping`, `ServiceStatus.forward_state`).
- Port forwarding dialog for Catalog entries with `port_forward`
  (`SetPortForwarding`); the row shows `public_ip:port` once forwarded.
