# Maître De Serveur

An appliance Linux distribution and management app that lets non-technical hobbyists run a home lab of containerised apps on bare-metal machines, with as little software on the host as possible so updates never need hands-on intervention.

## Language

**Maître De Serveur**:
The project as a whole: Maître Server, Maître Desktop, and the Catalog. Informally "Maître".
_Avoid_: MDS, the platform

**Maître Server**:
The appliance operating system installed on every Node, including the agent that manages it.
_Avoid_: The OS, the distro, the agent (when meaning the whole OS)

**Maître Desktop**:
The native desktop application that administers a Fleet. Informally "Maître".
_Avoid_: The app, the client, the web UI

**Console**:
The administration interface inside Maître Desktop.
_Avoid_: Dashboard, admin panel, control panel

**Trusted Device**:
An installation of Maître Desktop whose key has been approved to administer a Fleet.
_Avoid_: User, account, login, client

**Pairing**:
The act of approving a new Trusted Device or a new Node by confirming that the short code on the machine's own screen matches the one in the Console (ADR 0005).
_Avoid_: Enrollment, registration, login, pairing PIN

**Recovery Phrase**:
A one-time secret shown when a Fleet is created that can approve a new Trusted Device when all others are lost.
_Avoid_: Backup code, master password

**Node**:
A single bare-metal machine running the Maître distro and enrolled in a Fleet.
_Avoid_: Server, host, machine, box

**Fleet**:
The set of Nodes administered together from one management app, typically everything in one home.
_Avoid_: Cluster, site, group

**Service**:
A containerised app the user has chosen to run on a Node, as the user sees it (name, settings, status).
_Avoid_: Container, app, workload, pod

**Provisioning**:
The guided process that turns a freshly installed machine into a Node of an existing Fleet.
_Avoid_: Joining, bootstrapping, onboarding

**Catalog**:
The curated list of Service templates the user can install from, maintained in a public git repository.
_Avoid_: App store, marketplace, registry

**Installer**:
The bootable media that turns a bare machine into a Node, driven from the management app with the machine's own screen used only to confirm pairing.
_Avoid_: ISO, live USB, setup disk

**Config Partition**:
The small partition the Installer creates next to the OS, holding the Node's key and its copy of the Fleet database; it is what survives a reinstall and what gets backed up to Drives.
_Avoid_: State partition, /var

## Storage

**Drive**:
A physical disk on one Node, adopted or formatted, carrying automatic tags (ssd, hdd, usb, size, Node) and user tags.
_Avoid_: Disk, device, volume

**Storage Space**:
A Fleet-wide, named set of Drives that always hold the same kind of data, chosen by a rule (tags, Nodes, or every Drive). Services point at Spaces, never at Drives. Informally "Space". Replaces the retired term Tier (decision 128).
_Avoid_: Tier, storage class, storage location, share, bin

**Pool**:
What a Node builds for a Space out of the Drives available to it. Always a MergerFS mount, even over a single Drive.
_Avoid_: Mount, array, volume group

**Placement**:
A Space's rule for which of its Drives receives new data: local first, most free space, or highest free share.
_Avoid_: Mode, topology, replication type

**Spanning**:
A Space option that merges the Space's Pools on every Node into one view, with the parts on an unreachable Node unavailable until it returns.
_Avoid_: Replication, sync

**Exclusive**:
A Space option meaning a Drive that matches its rule serves this Space alone.
_Avoid_: Dedicated, locked

**Default Space**:
The one Space new Service volumes go to unless the user picks another. A Fleet with no Default Space cannot install Services.
_Avoid_: Primary, main storage

**Parity Drive**:
A Drive on a Node dedicated to parity data protecting every other Drive on that Node. Excluded from all Spaces.
_Avoid_: Parity disk, backup drive

**Adopt**:
Bringing an existing Drive and its data into a Node without reformatting it.
_Avoid_: Import, mount, attach

**Unsorted**:
Files found at the root of an adopted Drive that belong to no Space; the File Manager moves them into one.
_Avoid_: Loose files, legacy data

**Orphan Folder**:
A folder on a Drive left by a Space that no longer exists. Never deleted by a Node.
_Avoid_: Stale folder, leftover

## Exposure

**Private**:
Exposure level of a Service reachable only on the local network.

**Remote**:
Exposure level of a Service reachable on the local network and over the Fleet's overlay network, never from the open internet. The default.
_Avoid_: VPN-only, tailnet-only

**Public**:
Exposure level of a Service reachable from the open internet, either through a relay (Funnel) or directly via the user's own domain.
_Avoid_: Exposed, published, external

**Infrastructure Service**:
A Service the local network itself depends on (DHCP, DNS). Its Node keeps a reserved address and starts it before anything that waits for the network.
_Avoid_: System service, core service

**Member**:
A non-admin person (family, friend) granted access to specific Public Services. Not part of v1; reserved so the model stays ready for it.
_Avoid_: User, guest, viewer

## Console

**Fleet page**:
The area of the Console for administering the Fleet itself (Nodes, Storage, Devices, Updates, remote access) rather than using its Services.
_Avoid_: Settings, admin, config

**Service status**:
One of four words the Console uses for a Service: **Online** (running and healthy), **Starting** (starting, or being updated), **Stopped** (stopped by the user), **Attention** (failed, running but unhealthy, or restart-looping).
_Avoid_: Up, down, healthy (for Services), crashed, degraded

**Node status**:
One of five words the Console uses for a Node: **Healthy** (reachable and reporting no problem), **Unreachable** (no address answers), **Provisioning** (being installed or restored), **Updating** (applying a Maître Server update), **Attention** (reachable but reporting a problem, such as a rollback or a Pool missing a Drive).
_Avoid_: Online/offline (for Nodes), up, down, degraded
