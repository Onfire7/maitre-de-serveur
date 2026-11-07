# To do

Work that is agreed but not yet done, grouped by the part of the system it touches. Ideas that still need planning go under **Ideas** until they are broken down into the sections above. Terms are as defined in `GLOSSARY.md`.

## Maître Server

The agent on every Node (`server/`).

## Image

The bootc image, its units and updates (`image/`).

## API

The Protobuf schema between the Console and Nodes (`api/`).

## Maître Desktop

The Tauri shell and the Console (`desktop/`).

- **Say how long this Node takes to update.** The Update confirmation says "A Node normally takes under a minute" (`app/NodeDetails.tsx`), which is wrong: a real update stages and reboots, and takes several minutes. "Stop for a minute or two" (`NodeDetails.tsx`, `app/NodesTab.tsx`) is the same guess. Each Node already keeps its `UpdateRun` history, with start and end times and an outcome. Use the average of its recent `OUTCOME_UPDATED` runs ("usually about 6 minutes"), and keep a plain fallback, without a number, for a Node with no finished update yet. To decide: the average or the median, how many runs to count, and whether a progress view should show the elapsed time against it.

## Catalog

The Catalog entries and their format (`catalog/`, Onfire7/maitre-catalog).

## CI and tooling

GitHub Actions, the QEMU checks, `make` targets and the `maitre` development subcommands.

## Ideas

### Service commands

A Catalog entry can declare commands, and each one appears in that Service's menu in the Console. Choosing one opens a terminal, instead of a Page, running that command in the right container of the Service. This is for setup and maintenance that an app only offers from its command line: making a pairing code, resetting an admin password, running a migration or a cleanup.

A sketch of the manifest field, beside `pages:`:

```yaml
commands:
  - name: pair                 # [a-z0-9-], unique within the entry
    label: Make a pairing code # what the menu calls it
    component: server          # which container; required when the entry has components
    run: [app, pair, --new]    # argv, not a shell string
```

To work out before this is broken down:

- **Interactive or not.** A real terminal (a PTY, keystrokes in, output streamed out, resize) or only streamed output? Some admin tools prompt for input, so a terminal is the likely answer. That needs a streaming RPC, which none of the Service RPCs are yet.
- **Which RPC.** It would be a new Service RPC that runs an entry's declared command by name. The Console never sends a command line, so this is not a general exec, and it is not `DebugExec`, which only debug builds have. Following the "Console vs older Nodes" rule, a Node without the RPC hides the menu items instead of failing.
- **Arguments.** Fixed argv only, or prompts filled from a small form (like `settings:`) before the terminal opens?
- **Where the terminal lives.** A Console view in the Service's area, next to its Pages, with a terminal component in the Desktop. What happens on close: the command is killed, or it keeps running and you can come back to it?
- **The container's state.** The command runs only while the Service is running. Show the item disabled, with the reason, when it is stopped?
- **Trust.** The commands come from the signed Catalog bundle, the same as the rest of the entry. A free-form shell into a container is a separate decision and not part of this. It weakens "a Node is not an administrator" (ADR 0008) and is a root channel for any container with a granted host mount.
- **Custom Services.** Can a user add their own commands in Setup, the way they can override other options?
