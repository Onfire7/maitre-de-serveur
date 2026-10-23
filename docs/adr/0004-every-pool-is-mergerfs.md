---
status: accepted
---

# Every Pool is a MergerFS mount, even over a single Drive

Services point at Tiers, and a Node materialises a Tier as a Pool. Every Pool is a MergerFS mount from v1, including a Pool over exactly one Drive. This makes the path a Service sees stable forever: adding Drives, enabling mirrored copies, or spanning to other Nodes over NFS changes the Pool's branches, never the mount point. Storage protection (mirrored copies per Tier, SnapRAID parity per Node over dedicated Parity Drives) ships after v1 without any Service migration.

## Considered options

- **Mount Drives directly in v1, add MergerFS later**: would force every Service's mount point to change when pooling arrived.
- **btrfs or ZFS pooling**: ties Drives together at the filesystem level, defeats Adopt (bringing existing drives with their data unformatted), and makes removing a Drive a rebuild rather than a copy.
- **Cross-Node spanning via a distributed filesystem**: heavy daemons and failure modes that are hard for a hobbyist to diagnose. MergerFS over soft NFS mounts makes a remote Node's files simply absent until it returns.

## Consequences

NFS mounts between Nodes must be soft with timeouts, or a Node going offline hangs processes on every other Node. Parity Drives are excluded from all Tiers.
