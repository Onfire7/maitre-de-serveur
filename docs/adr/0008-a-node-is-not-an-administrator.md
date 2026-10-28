---
status: accepted
---

# A Node is not an administrator, and a container gets root powers only by a second, explicit signature

The agent stays root. It formats Drives, mounts Pools, writes system files, runs Podman and updates the OS, and a separate root helper would have to do whatever the agent asked it, so splitting them protects little. Instead the Fleet defends two boundaries: a container must not become root on its Node, and a compromised Node must not take over the other Nodes. (Decision 189.)

**Nodes lose admin powers.** The trust list gets a third role beside full and recovery-only keys: Node keys. A Node key may only do Node-to-Node work: replicate records, gossip status, answer liveness, mount shared Pools over NFS, and finish a file move a person started. Everything that changes the Fleet (updates and rollbacks, Services, Devices, Spaces, removing Nodes, update policies) needs a Trusted Device.

**Devices sign the records that grant power.** Device, Service, Space and update policy records, and Fleet membership, carry the signature of a Trusted Device over their content. A Node checks that signature before acting on one, so peers relay these records but cannot forge them; an unsigned or wrongly signed one is ignored and reported as a problem. A Node signs only facts about itself, as its NFS certificate already is (decision 188). A change the Console sends through the anchor is signed by the Console, so the anchor's own key never authorizes it.

**Services keep full Docker Compose compatibility; root powers need a second signature.** Anything a compose file or Catalog entry may say is allowed, so people can bring containers from anywhere. The receiving Node classifies the generated Quadlet itself. Protected options are:
- `privileged`;
- capabilities beyond Podman's default set;
- devices;
- the host's network, PID, IPC or user namespace;
- SELinux, seccomp or AppArmor turned off;
- sysctls;
- mounts of host paths outside the Service's Spaces and its own data folder, the Podman socket included.

Each protected option needs a grant: the Console shows a warning that names the option and what it allows, and signs a grant for that option alone. A grant names the Service, its Node, the option, and the image's repository, not its tag, so updates keep it and a different image needs a new grant. A Node runs a Service with a protected option only with a valid grant for it, and otherwise refuses to start it and says which grant is missing. A Service added fraudulently, or a Service record altered after the fact, therefore gets no root powers without the second signature.

**Containers are confined by default.** Unprotected Services run with `NoNewPrivileges=true`, Podman's default capabilities and seccomp profile, SELinux (`container_t`, never disabled without a grant), no Podman socket, and the bridge network. They run as the image's own user, or as the compose file's `user:`, as Docker would; Maître does not force a user, because that breaks images.

**The agent's file work runs as the Space owner.** Moves, deletes, Zip, Unzip, uploads and new folders run in a child process with the Space owner's uid and gid, rsync included, so a symlink a container plants in a shared Space cannot lead a root process outside it. Setting a Space's owner stays root and becomes a repair; it already uses `Lchown`.

**NFS squashes root to the Space owner.** Each Space's export uses `root_squash,anonuid=<owner>,anongid=<owner>`: containers that run as root, which most compose images do, still write to a peer's Drives, as the Space's owner. A Space without an owner squashes to `nobody`. Peers' Pools are mounted `nosuid,nodev,noexec`. A compromised Node can still read and write the Spaces it shares; that is what spanning a Space means.

**Maître Server images are signed.** Nodes accept an OS image only when it is signed by the project's key, through Podman's signature policy, both from `UpdateImage` and from automatic rollouts. The dev Depot signs with a dev key that only dev Nodes trust.

## Considered options

- **Unprivileged agent with a root helper**: the helper must chown, mount and update on the agent's word, so a compromised agent still gets root.
- **An allowlist of Quadlet keys, refusing the rest**: simplest to enforce, but turns away containers people want to run and breaks compose compatibility, for a low chance of abuse.
- **Running every Service as the Space owner (`User=`)**: images that expect to start as root break.
- **Rootless Podman or `userns=auto`**: each container's own uid range fights the shared ownership of Spaces; SELinux and dropped privileges give most of the benefit.
- **Plain `root_squash`**: root containers could not write to a peer's half of a spanning Space, so writes would fail depending on which Drive mergerfs picked.

## Consequences

- One compromised Node no longer means a compromised Fleet. Before this, a Node key could call every admin action on its peers, and a Service record's Quadlet ran as written on the Node it named.
- Amends ADR 0007: the core calls keep their shape, but authorization is not part of the frozen contract. A peer's `UpdateImage`, `Rollback` or `Reboot` is refused with PermissionDenied, and `UpdateImage` refuses an unsigned image. Liveness (`GetNodeInfo`) is unchanged.
- Fleets from before 189 are not migrated: unsigned Service, Space and Device records are ignored after the update and have to be remade. The only such Fleet is the developer's.
- The Console needs grant dialogs and a list of a Service's granted options, which can be revoked.
- `make publish` signs images; existing dev Nodes get the dev key by one last unsigned update.
- What 189 does not prevent: a container with no grants that escapes Podman and SELinux through a kernel bug is root on its Node. bootc's read-only `/usr` makes it harder for that to survive a reboot, and Node keys limit what it reaches beyond that Node.
