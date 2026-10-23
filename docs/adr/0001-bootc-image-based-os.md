---
status: accepted
---

# Maître Server is a bootc image-based OS

Maître Server must update itself without ever needing user intervention, on hobbyist bare metal. We build it as a bootc image (Fedora base): the whole root filesystem ships as one signed container image, boots atomically, and rolls back automatically if the post-boot health check fails. The distro itself is a Containerfile, so Podman and Quadlet are native and there is no package tree to maintain.

## Considered options

- **Flatcar**: mature A/B updates, but Docker-centric and hard to customise.
- **Buildroot or Alpine from scratch**: smallest footprint, but we would own the kernel, packaging, and the update mechanism, which is the exact thing we want to never debug on a user's machine.
- **A plain Debian or Alpine install plus our software**: package-level updates can leave a machine half-updated, which is the failure mode the whole product exists to avoid.

## Consequences

The host is immutable. Anything that is not in the image is a container or lives on the config or data partitions. Health is defined as "every Service that ran before the update runs after it."
