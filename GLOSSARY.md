# Maître De Serveur

An appliance Linux distribution and management app that lets non-technical hobbyists run a home lab of containerised apps on bare-metal machines, with as little software on the host as possible so updates never need hands-on intervention.

## Language

**Maître**:
Short name for the whole product: the distro, the agent on each machine, and the management app.
_Avoid_: MDS, the manager, the platform

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
