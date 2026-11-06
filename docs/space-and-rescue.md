# What still has to happen: pulls, the image store, a Node's space, and the way back in

Decisions 315-326, raised by the incident of 2026-11-04 and settled on 2026-11-05.
This is the work list. The reasoning behind each decision is in
`docs/v1-build-order.md` under "A Service that cannot start stops costing
bandwidth, and the container image store leaves the root partition"; this
document says only what is left to build, in what order, why that order, and
how each piece is proven.

## The baseline

All of it is built, as of 2026-11-05, except what the last section names.
The work landed in six commits, in the dependency order below: `bcfcb38`
(the owner filter, the start limit, the prune and the greenboot space
check), `93cbf91` (the guards before a pull), `f4c7922` (the gauges and the
per-Service series), `f056218` (the store off root and root at 6x the
image), `eba68de` (zram, greenboot falling into rescue, the rescue menu and
the staging reserve) and `ff56a63` (the three tiers and the Console's
amber image box).

The Debug Console followed, as `internal/debugconsole` and `maitre debug`:
six pages in Bubble Tea, each of them also a command, with `--json` for a
script and `--demo` to look at the whole thing without a Node.

What is **not** built, and is the honest remainder:

- **A fourth `Debug` role.** `authz.Role` is still `Stranger`, `Device`,
  `Recovery`, `Node`, so the Debug Console pairs as a Trusted Device and is
  trusted as fully as Maître Desktop is; the debug tier refuses a Node
  caller rather than demanding a role of its own. This is the one part of
  323 the app did not need, and the one that would narrow what a stolen
  Debug Console key can do.
- **A volume-less Service still skips both storage guards** (the open item
  below).
- Nothing has run on a real Node. The whole of it has been verified in
  tests, against the built image, and against a real registry where that
  was the question; none of it has been through a boot.

## Stage 1 — stop paying for pulls

Nothing here touches a partition, so all of it can be built and proven on the
dev box.

**1. A digest check that does not download to learn a number.**
`Update` (`services.go:994`) runs `podman pull -q <tag>` at line 1001 and only
then compares digests, so it downloads layers to discover whether it needed
them. `SetImage` (line 1175) does the same at 1216 and 1228. Replace the
leading pull with `podman manifest inspect <remote tag>`, which fetches the
index alone — verified against an image absent from the store, which stayed
absent.

The care this needs is the whole reason it is first and not trivial: a pinned
digest may name **either** an image index **or** one platform's manifest.
`podman image inspect --format {{.Digest}}`, which is what pins `spec.Image`,
gave the per-platform digest for `alpine:3.20` on podman 6.1.2, while of
Avalon's 44 pinned refs 23 resolve to an index and 3 to a plain manifest. A
check that assumes one kind mismatches every image of the other and pulls every
time — an optimisation that looks right and does nothing. So the comparison
accepts both: the index's own digest, and the digest of the entry matching the
Node's os and architecture. A single-manifest image carries no self-digest in
its body, so it falls back to today's pull-then-inspect.

**2. Appliability before a pull.** Even with a free digest check, `Update` and
`SetImage` should refuse before touching the network when `apply` would refuse
afterwards — a Service this Node does not own, or whose record it did not sign.
`mine` already covers the auto-update path; this covers a Console or CLI call
naming a Service directly.

**3. A free-space precheck.** Before any pull, compare
`storage.FreeBytes(graphroot)` against a floor and refuse with a message that
says so, rather than discovering it as `storing blob to file ...: no space left
on device` after thirty-five seconds of download. `storage.FreeBytes` already
exists (`storage/statfs_unix.go`). This is what turns a full store from an
unbounded bandwidth bill into one clear error, and it matters more after 321
shrinks root, not less.

**4. The automatic prune trigger (316).** The prune exists and is callable;
nothing runs it. Decision 316 is that free space crossing a threshold triggers
it, not a schedule, so a stopped Service keeps its image in the ordinary case.
The trigger belongs beside the precheck of item 3: the same measurement, one
deciding to refuse and the other to reclaim. Prune first, then pull, then refuse
only if it still does not fit.

## Stage 2 — make it visible

The Console could not have shown any of this, which is why it ran for twenty
hours.

**5. A root filesystem gauge.** `NodeMetrics` has gauges for CPU, memory, Pool,
network, temperature, load and each Pool Drive, and nothing for the filesystem
that actually filled. Fields 12 and 13 are free; the series name follows the
existing convention as `node/system_disk`, documented in
`GetMetricsHistoryRequest`'s comment with the others. `storage.FreeBytes` is the
whole implementation. The Console's Node screen then has somewhere to show it,
and the `-history` default set should include it.

Note what this does *not* need: the gauge is useful on its own and reads the
root filesystem whether or not the image store has moved. Build it before 319,
not after, so the move can be watched.

**6. Per-Service network and pull bytes (317).** The per-Service series `cpu`,
`memory`, `disk_read` and `disk_write` already exist (`history/sample.go`
reading each unit's cgroup through `metrics.Unit`) and named the culprits
plainly. Two gaps: there is no per-Service network at all, and a `podman pull`
runs in the host netns, so a netns-based `service/<name>/net_rx` would have read
zero during exactly this failure. So pull bytes are counted in the Manager,
where they are attributable, and network comes from the container's netns. A
Service on a Tunnel shares the Tunnel's namespace, so its traffic is attributed
to the Tunnel and shown on each member marked as shared, rather than split
dishonestly or hidden.

## Stage 3 — move the store off root (319) and resize root (321)

This is the only stage that must be verified on a real Node rather than in
tests, for two reasons: SELinux labelling of a non-default graphroot, and the
boot ordering of a store that lives on a Pool Drive.

**7. Where the store goes.** The OS disk's `maitre-data` partition when there is
one; otherwise `.maitre/imagestore` on the largest Drive of the default Pool.
Re-downloadable data, so it is worth neither MergerFS nor SnapRAID parity, and
`.maitre/` is already excluded by `parity.go` and already hidden by
`storage.Folders`. An SSD wins outright over proximity to a Service's files:
volumes render as `Volume=<host>:<path>:z` bind mounts straight from the Pool and
never pass through the graphroot, and overlayfs's lower and upper layers are both
inside the graphroot by construction.

**8. Two settings move together.** `storage.conf`'s `graphroot` and
`containers.conf`'s `image_copy_tmp_dir`. Treat it as an invariant: blob staging
is what hit ENOSPC, and leaving it on a root that 321 then shrinks would make
the failure likelier, not rarer. Open: whether the podman in `fedora-bootc:42`
honours a `storage.conf.d` drop-in or wants the file written whole;
`containers.conf.d` is certain.

**9. Resolved lazily, after the Drive check.** The store is resolved inside
`apply`, after the Space and volume checks and before the unit is written, so a
Node with no Drive cannot reach it. Install already refuses there, and Install
itself never pulls — the unit does. The agent ensures the same store at startup,
idempotently, before Services start.

**10. Root at 6x the OS image (321).** `RootSize` is still the flat `"32G"`
(`installer/runner.go:68`), which on a 64 GB disk takes half of it. The media
path already has the measurement to hand, since `podman pull` of the image runs
before `rootSize` (line 419); the netboot path calls `rootSize` before bootc
fetches anything and needs `skopeo inspect` or a documented fallback.
`MinRoot`'s reasoning (line 70) should be rewritten at the same time, since it
budgets for the OS and never for the store.

The Installer's `data_partition` flag changes meaning here: from "create it" to
"may `maitre-data` join a Pool". The partition's existence is what the image
store wants; its eligibility for a Pool is the user's choice, and they are
different questions.

## Stage 4 — survive a spike (322)

**11. zram.** Nothing in `image/` or the Installer configures swap today, so a
memory spike is terminal and the kernel picks a Service to kill. zram costs no
disk at all, so it behaves the same on a USB install, needs no partition and no
filesystem, and compresses at roughly 2-3x. `zram-generator` is not in the
image's `dnf` list and needs adding. Size it modestly: swap trades a fast
failure for thrashing, and a server often wants the fast one. This also softens
the `/tmp` question, since tmpfs pages are swappable.

Stands alone — no dependency on any other item here.

## Stage 5 — the way back in

**12. The staging reserve (324).** bootc stages into ostree's repo under
`/sysroot`, and root *is* sysroot, so the reserve has to be a file inside root,
allocated at install and deleted when an update starts. xfs has no tunable
reserve of ext4's kind. 110% of the image is generous rather than tight: only a
large version jump approaches it.

**13. Rescue, which costs nothing (324, 325).** The rescue environment is the
same image booted with a rescue flag; there is no separate boot image. It is
half built already, because decision 101's recovery mode is exactly this shape:
`recoverymode.RequestFile` in the state directory asks the next boot to enter
recovery, `screen.go`'s `RebootRecovery` writes it and reboots, and the agent
reads it at start (`agent.go:151`) and runs `runRecoveryScreen` with networking
off.

A Node also has to be able to *reach* rescue when its agent cannot start, which
today it cannot: only a running agent writes the flag. greenboot is the answer
and the hook already runs at the right moment — `image/greenboot/red-maitre.sh`,
installed as `/etc/greenboot/red.d/10-maitre.sh`, is what greenboot runs on a
red boot, and it already writes `/var/lib/maitre/last-rollback`. That directory
is the config partition, shared by both deployments, so a request written there
survives the rollback greenboot then arranges, and the rolled-back image honours
it. `recoverymode.Requested` clears the request as it reads it, so a crash loop
cannot hold a Node in rescue.

Two things to get right. Rescue belongs *after* a rollback has had its turn and
failed, not on the first red boot, or an unrelated red boot takes a Node off the
network and needs someone standing at it; greenboot counts in the bootloader
environment, so rather than depend on its internals the red script can keep its
own tally beside `last-rollback`.

The second is that a rescue boot does not need to stop the Services, which is
worth writing down because the opposite looks true at first. Every Service unit
is `WantedBy=multi-user.target default.target`, so systemd starts them in
parallel with the agent whatever the agent then does — but rescue runs with
`nmcli networking off`, and a pull with no route and no DNS fails at connect. It
transfers nothing, so it stages no blob into `/var/tmp` and adds no layer to the
store. A crash-looping Service in a rescue boot costs CPU and a few journal
lines, and `StartLimitBurst=5` caps even that. So no kernel argument, no rescue
target and no boot-time mechanism: the file is enough, exactly as decision 324
says, and rescue stays entirely inside the agent with `image/` untouched.

What is left is a race, not a leak. The agent is `After=network-online.target`
and the Services are its siblings on `multi-user.target`, and the agent opens the
store, loads the trust list and runs `ResignOwn` before it reaches the flag
check, so for a second or so the interfaces are up and a Service can be pulling.
The fix is ordering, not machinery: read the flag and turn networking off at the
top of the agent's startup, before `store.Open`, since the flag needs only the
state directory path. The screen itself still needs the Node and Fleet names, so
it draws after the store is open, as it does now — the networking call is the
only part that moves. Beyond that, "stop all Services" can be one of the rescue
menu's actions, an ordinary `systemctl` call for whoever wants the machine quiet
while they work.

**14. What rescue can do.** Today it shows a one-time pairing code and waits for
Enter — `runRecoveryScreen` is a `printf` screen, not a menu, because decision
101 needed nothing more. Expanding it is its own piece of work: rescue becomes a
menu, with Services held down, offering at least

- report space: root free and total, the image store's path and size;
- prune images, which is the repair this incident needed;
- roll back to the previous image;
- re-run the health checks and show what they say;
- view a unit's journal;
- a shell, as the escape hatch (326);
- reboot normally, and the existing one-time code for pairing a Desktop.

The curated actions matter more than the shell: each one that exists is a repair
nobody has to improvise. `tui.Source` is the shape to reuse — it already serves
status, the metrics charts, the containers and any unit's journal, with Reboot
and RebootRecovery behind a confirm — with a reduced implementation that does not
assume a running agent, since the case that reaches rescue is the one where the
agent did not start.

**15. Three tiers of diagnostics (326), split by what can reach them.**

- *The regular server*, in every shipped image: basic diagnostics that are not a
  security surface — filesystem usage, the image list, a unit's journal, prune —
  for a Trusted Device, which can already install Services and open their pages,
  so none of it is an escalation.
- *The debug build*, behind a Go build tag: the dangerous remote half, meaning
  diagnostics over the wire and command execution, for a paired Debug Console.
  Absent from a shipped binary rather than guarded inside one.
- *Rescue mode*, with networking off: a shell, on the Node's own screen, reached
  only by someone standing at the machine.

What this preserves is worth stating precisely, because it is stronger than the
rule it replaces: "a Node has no terminal" becomes a property of the *network*
rather than of the image. Nothing reachable over a network can run a command on
a Node unless the image was built for it, and a shell exists only where physical
presence is already assumed. It is also honest — physical access to an
unencrypted disk is root anyway, so the rescue shell takes nothing that was not
already there. It would become the hole to think about if disk encryption or
measured boot ever arrive.

**16. The Debug Console (323).** Built. A separate app in the sense that
matters — its own pages, its own trust, nothing of it compiled into what a Node
serves — living in the `maitre` binary that is already the client CLI, so it
needs no identity, trust list or dialling of its own invention.

It is one surface in two shapes, and that is the design rather than a
convenience. Every page is also a command: `maitre debug space` is the Space
page, `maitre debug read` is the Files page, and `--json` prints the response as
protobuf JSON. Both shapes go through one `Source` interface, so a page a script
cannot reach cannot be written, and the fakes behind that interface are what the
tests drive. The reason is the incident itself: what ends one is usually the
same question asked of four Nodes at once, and a console that can only be
watched cannot be asked that way.

Decision 326's split is shown rather than hidden. Overview, Space, Services and
Logs are the basic tier and answer from every shipped image. Files and Run are
the debug tier, and against a shipped Node they say so before anything is typed,
read from `NodeInfo.debug_build` — the Console does not make a Node refuse in
order to discover what it is. The newer RPCs stay optional: `Unimplemented`
reads as "this Node is older than the image list", never as a broken page.

The loud advertising is the Desktop's, deliberately: the whole image box turns
amber on the Update and Node screens through `ServerImage` in `NodeDetails.tsx`,
and the Console's header carries the same amber `DEBUG BUILD` pill, because the
risk 323 names is a debug image running in production unnoticed and it should
look the same wherever a Node is described.

What it does not do is reach a Node already running a shipped image, since
getting it there is an update. It buys future diagnosis, not the recovery of a
wedged Node, which is 324 and 325.

To use it: `make build`, then `./server/bin/maitre debug --demo` to read every
page without a Node, or `--addr HOST:7443` against a real one, which needs a
Trusted Device identity (`maitre keygen`) that the Node trusts and the Node's
own `identity.pub` in the caller's trust directory. The basic tier works against
any image. The debug tier needs one built for it, which is `make image-debug`:
it sets `GOTAGS=maitredebug` and names the image `:<version>-debug`, because
323's risk is a debug image running in production unnoticed, and a version that
says so is the cheapest guard against it.

## Still open

- A Service with no volumes skips both storage guards, since the default-Space
  check runs only `for _, v := range spec.GetVolumes()`, so a Tunnel could still
  install on a Drive-less Node.
- Whether `storage.conf` honours a `storage.conf.d` drop-in on this podman:
  **answered, inside the built image — it does not.** A drop-in is ignored and
  `containers.conf.d` is not, which is why `storage.conf` is written whole from
  the vendored copy; writing it fresh would silently drop the image's own
  `additionalimagestores` and overlay `mountopt`.
- The new `PruneImages` RPC must stay optional in the Console: an older Node
  answers `Unimplemented`, and the Console falls back rather than requiring it.
  Done in the Debug Console, which names the Node old rather than showing an
  error, and tested both ways. Maître Desktop does not call either RPC yet, so
  there is nothing there to fall back from — which is itself the remaining
  work, since the Desktop is where an older Node is likely to be looked at.

## How each of these is proven

Five rungs, cheapest first. Nothing goes to a Node carrying real Services until
the rung below it has passed.

0. **`maitre debug --demo`.** The Console against a made-up Node in the state
   of 2026-11-04 — a root with a gigabyte left, an image store still on it, a
   Service at 1174 restarts, images nothing names. It proves nothing about a
   Node, and it is the only way to read every page of the Console at once
   without one, which is what the Node's own `maitre tui --demo` is for.
1. **Unit tests on the dev box.** Items 1-6 are all `fakeRunner` work: scripted
   `podman manifest inspect` output for an index and for a plain manifest, a
   free-space floor, a prune trigger, a gauge. No hardware.
2. **Two Nodes on loopback.** `scripts/m3-check.sh` stands up two Nodes of
   one Fleet with `--fake-install`, no root and no hardware, and `make
   m1-check`/`m0-check` cover a single Node and the mTLS refusals. What
   they cannot easily do is seed a Service: `maitre record put` takes a raw
   protobuf body, not JSON, and `service install` wants podman, so decision
   318's end-to-end case is covered by the unit tests instead — they seed a
   foreign spec through the real store and assert both that `List` and
   `AutoUpdate` pass it over and that no registry is contacted for it.
   One trap worth keeping in mind for any future attempt: `mine` filters on
   the spec's own `node_id`, which only `Install` sets, so a seeded body
   must carry it or the Service vanishes from both Nodes and the test
   passes for the wrong reason.
3. **A VM.** `make image` then `make image-boot` installs from the image in
   QEMU and reboots into a Node. This is where greenboot's new check runs for
   real, where 319's SELinux labelling and mount ordering are either right or
   obviously wrong, and where 321's partitioning can be read off a fresh
   install. `make vm` boots CI's installer media for the same purpose without a
   local image build.
4. **The LAN test Nodes**, booted from the dev box's Depot, with
   `make image publish TAG=dev` and `maitre update`. Real hardware, no real
   Services.

Verified so far, for `bcfcb38`: `go build`, `go vet`, the full Go test suite,
`make gen-check`, desktop `tsc --noEmit`, and the greenboot check inside the
built image — both its pass path (reporting `/var/lib/containers/storage`, the
Node's real graphroot) and its warn path, at exit 1.

## The live-Node sequence

Both real Nodes are wedged in the same way, so the order is chosen to learn the
riskiest fact first at the lowest cost.

**Stage, do not reboot.** `maitre update` takes `-no-reboot`, which stages and
stops. That answers the one genuinely unknown question — can a Node with a full
root stage a ~2 GB update at all? — with no downtime and no reboot on either
Node. If staging fails, nothing has been lost and we know before touching
anything. Pair it with `-wait` to see the outcome.

**Boulder before Avalon.** Boulder owns zero Services, so a reboot costs
nothing, and it exhibits the auto-update sweep directly. It is also the better
prune test: owning no Services, its keep-set is empty, so every image in its
store is waste from the sweep, and `maitre service prune -drop-rollback` should
empty it and report exactly how much the bug cost. Expect `service list` to come
back empty afterwards where it used to show sixteen phantom stopped Services.

**Then Avalon**, where three things are worth knowing in advance:

- Its Services are enabled, so the reboot starts them again whether or not they
  were stopped by hand. The ones whose images are missing will pull, and until
  `Migrate` rewrites their units early in the agent's start, they do so under
  the *old* unit files with no start limit. The worst case is minutes of pulling
  instead of twenty hours, but it is not zero.
- `render.go` changed, so every Service's unit is stale and `Migrate` rewrites
  all sixteen and restarts those that ran. `apply` does not pull, so the rewrite
  itself downloads nothing.
- Prune as soon as the agent answers, before anything else. Keep rollback
  targets on the first run; the measured duplicates share their layers entirely,
  so dropping them frees almost nothing and costs the ability to roll back.

A Node that predates `bcfcb38` answers `PruneImages` with `Unimplemented`, which
is the clearest possible statement of why the update has to come first.
