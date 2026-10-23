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
The act of approving a new Trusted Device or a new Node by confirming a short code shown on the machine's own screen.
_Avoid_: Enrollment, registration, login

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

## Storage

**Storage Location**:
A drive or pool on one Node where Service data can live. Addressable from any Node in the Fleet.
_Avoid_: Volume, mount, share, disk

**Adopt**:
Bringing an existing drive and its data into a Node as a Storage Location without reformatting it.
_Avoid_: Import, mount, attach

## Exposure

**Private**:
Exposure level of a Service reachable only on the local network.

**Remote**:
Exposure level of a Service reachable on the local network and over the Fleet's overlay network, never from the open internet. The default.
_Avoid_: VPN-only, tailnet-only

**Public**:
Exposure level of a Service reachable from the open internet, either through a relay (Funnel) or directly via the user's own domain.
_Avoid_: Exposed, published, external

**Member**:
A non-admin person (family, friend) granted access to specific Public Services. Not part of v1; reserved so the model stays ready for it.
_Avoid_: User, guest, viewer
