---
status: accepted
---

# A Node is not an administrator, and a container gets root powers only by a second, explicit signature

The agent stays root. It formats Drives, mounts Pools, writes system files, runs Podman and updates the OS, and a separate root helper would have to do whatever the agent asked it, so splitting them protects little. Instead the Fleet defends two boundaries: a container must not become root on its Node, and a compromised Node must not take over the other Nodes. (Decision 189.)

**Nodes lose admin powers.** The trust list gets a third role beside full and recovery-only keys: Node keys. A Node key may only do Node-to-Node work (`server/internal/gate`): liveness, metrics and update status, the Fleet database's exchange (`ListRecords`, `PushRecords`, `PushStatus`), `GetFleetStatus`, forwarding a Device-signed change to its owner (`PutRecord`), and a file move between two Nodes (`FilesService` Move, ListJobs, CancelJob), which must pull from the calling Node's own Pool between Spaces that span Nodes, and may cancel only its own moves. Everything else (updates and rollbacks, reboots, logs, Services, Drives, file manager operations, the Service tunnel, file contents) needs a Trusted Device.

**Every record is signed, and the right key must have signed it.** A record carries its signer's key, a signature over its kind, id, body, deletion and signing time (`server/internal/recsig`; the Console signs the same bytes in Rust), and that time. A Node checks each record it takes from a peer or a caller against `server/internal/authz`:
- Trusted Devices, Spaces, update policies, Tailscale settings, public settings and grants: a Trusted Device. The Recovery Phrase may sign a new full device. A Node may sign a device scoped to itself (decision 120's recovery code), or promote it to the whole Fleet with a voucher: the first Node's signed record of the same device, no more than a day older.
- Node records: a Trusted Device (introducing or removing a Node), or the Node itself once introduced and while not removed.
- Services: the Node that runs them, which writes them when a Trusted Device asks it directly; it may not take over another Node's live Service of the same name. Each Node also checks, before writing a unit, that the Service's record is its own.
- Drives: the Node that has them. Anything else: a Trusted Device or the owning Node.
- `PutRecord` takes only records a Trusted Device (or the Recovery Phrase) signed, so a change the Console sends through the anchor is authorized by the Console, never by the anchor.
- A replacement must be signed later than what it replaces, so an old signed body cannot be replayed over a newer one; a Node signs its own writes after the record they replace even if its clock went back.

**Services keep full Docker Compose compatibility; root powers need a second signature.** Anything a compose file or Catalog entry may say is allowed, so people can bring containers from anywhere. The receiving Node classifies the generated Quadlet itself. Protected options are:
- `privileged`;
- capabilities beyond Podman's default set;
- devices;
- the host's network, PID, IPC or user namespace;
- SELinux, seccomp or AppArmor turned off;
- sysctls;
- mounts of host paths outside the Service's Spaces and its own data folder, the Podman socket included.

Lines in any section other than `[Container]` (such as `[Service] ExecStartPre=`, which runs as root on the Node) and `[Container]` keys Maître does not know are protected too (`server/internal/services/protect.go`). Each protected option needs a grant (kind `grant`): the Console shows a warning that names the option and what it allows, and signs a grant for that option alone. A grant names the Service, its Node, the option, and the image's repository, not its tag, so updates keep it and a different image needs a new grant. A Service call that would install a definition with an option not granted fails with FailedPrecondition and a `GrantsNeeded` detail; the Console asks, signs the grants and retries. A revoked grant stops the Service and removes its unit. A Service added fraudulently, or a Service record altered after the fact, therefore gets no root powers without the second signature. Settings and folder names may not contain line breaks, which could otherwise add lines to the unit.

**Containers are confined by default.** Unprotected Services run with `NoNewPrivileges=true`, Podman's default capabilities and seccomp profile, SELinux (`container_t`, never disabled without a grant), no Podman socket, and the bridge network. They run as the image's own user, or as the compose file's `user:`, as Docker would; Maître does not force a user, because that breaks images.

**The file manager's changes run as the Space owner.** New folders, uploads, Delete, Zip and Unzip, and moves (writes as the destination Space's owner, removal as the source's, rsync with the owner's credentials) act with the Space owner's file system identity (`owner.As`: per-thread fsuid and fsgid, no supplementary groups), so a symbolic link a container plants in a shared Space cannot lead a root process outside it. Reading (listing, sizes, downloads) stays root; a Service's own volume moving between Spaces stays root to keep its container's owners; giving a Space's owner stays root and uses `Lchown`. A Space's owner id must be between 1000 and 65533. Consequence: the file manager cannot change files a container owns as another user, such as a Service's default volume folders, and says so.

**NFS squashes root to the Space owner.** Each Space's export uses `root_squash,anonuid=<owner>,anongid=<owner>`: containers that run as root, which most compose images do, still write to a peer's Drives, as the Space's owner. A Space without an owner squashes to `nobody`. Peers' Pools are mounted `nosuid,nodev,noexec`. A compromised Node can still read and write the Spaces it shares; that is what spanning a Space means.

**Maître Server images will be signed (roadmap, not built).** Nodes are to accept an OS image only when it is signed by the project's key, through Podman's signature policy, both from `UpdateImage` and from automatic rollouts. Deferred (user, 2026-10-27): signing the large dev images on every deploy costs time now and is as easy to add later. Until then, a Trusted Device can still update a Node to any image; Nodes cannot.

## Considered options

- **Unprivileged agent with a root helper**: the helper must chown, mount and update on the agent's word, so a compromised agent still gets root.
- **An allowlist of Quadlet keys, refusing the rest**: simplest to enforce, but turns away containers people want to run and breaks compose compatibility, for a low chance of abuse.
- **Running every Service as the Space owner (`User=`)**: images that expect to start as root break.
- **Rootless Podman or `userns=auto`**: each container's own uid range fights the shared ownership of Spaces; SELinux and dropped privileges give most of the benefit.
- **Plain `root_squash`**: root containers could not write to a peer's half of a spanning Space, so writes would fail depending on which Drive mergerfs picked.

## Consequences

- One compromised Node no longer means a compromised Fleet. Before this, a Node key could call every admin action on its peers, and a Service record's Quadlet ran as written on the Node it named.
- Amends ADR 0007: the core calls keep their shape, but authorization is not part of the frozen contract. A peer's `UpdateImage`, `Rollback` or `Reboot` is refused with PermissionDenied. Liveness (`GetNodeInfo`) is unchanged.
- Records from before 189 are not migrated: a Node keeps the unsigned records it holds and goes on using them, signs its own (Node, Service, Drive) as new versions at start, and accepts no unsigned record from anyone. Device-signed kinds (Spaces, policies, devices) spread again once a Trusted Device saves them. A Node provisioned into an existing Fleet receives the Fleet's signed records with its provisioning (`ProvisionRequest.records`).
- The Console signs every record it writes, asks for grants in a warning dialog, and lists a Service's grants with Revoke.
- Peers' Pools are exported with `root_squash,anonuid=<owner>,anongid=<owner>` and mounted `nosuid,nodev,noexec`.
- What 189 does not prevent: a container with no grants that escapes Podman and SELinux through a kernel bug is root on its Node. bootc's read-only `/usr` makes it harder for that to survive a reboot, and Node keys limit what it reaches beyond that Node.
