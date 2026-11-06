// Package imagestore puts podman's image store somewhere other than the
// root partition (decision 319).
//
// The root partition is sized for the OS: about 2 GB installed, with a
// second copy for an update. The container image store was never sized for
// at all, and it is the larger and unbounded consumer — Avalon's sixteen
// Services are about twenty-five images including ollama with its CUDA
// libraries, immich's server and machine learning, photoprism, jellyfin
// and paperless with tika and gotenberg. On 2026-11-04 that filled a 32 GB
// root, and every Service that then failed to start re-pulled its image
// every five seconds.
//
// Where it goes, which the user settled: the OS disk's data partition when
// there is one, otherwise `.maitre/imagestore` on the largest Drive of the
// default Pool. It is re-downloadable data, so it is worth neither
// MergerFS nor SnapRAID parity, and `.maitre/` is already excluded from
// parity and hidden from folder views. Not a partition of its own,
// "especially when the OS is installed on a USB flash drive".
//
// Two settings move together and this package treats that as an
// invariant: storage.conf's graphroot and containers.conf's
// image_copy_tmp_dir. Blob staging is what hit ENOSPC, and leaving it on a
// root that decision 321 then shrinks would make the failure likelier, not
// rarer.
//
// Measured on the Node image's own podman (6.x, fedora-bootc:42) rather
// than assumed: a storage.conf.d drop-in is *ignored* (podman went on
// reporting /var/lib/containers/storage), so storage.conf is written
// whole — derived from the vendored copy, so that the image's own
// additionalimagestores (/usr/lib/containers/storage) and overlay
// mountopt survive, which a file written from nothing would silently drop.
// A containers.conf.d drop-in does work, and that is where
// image_copy_tmp_dir goes.
package imagestore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dir is the directory under a Drive's mount that holds the store. Inside
// .maitre/, which parity excludes ("exclude /.maitre/") and the Console's
// folder views hide, so it is never mistaken for the user's data.
const Dir = ".maitre/imagestore"

// Paths written, as package variables so a test need not be root.
var (
	VendoredConfig   = "/usr/share/containers/storage.conf"
	StorageConfig    = "/etc/containers/storage.conf"
	ContainersDropIn = "/etc/containers/containers.conf.d/50-maitre-image-store.conf"
	// DefaultRoot is where podman keeps the store when nothing says
	// otherwise: on the root partition, which is the problem.
	DefaultRoot = "/var/lib/containers/storage"
)

// Runner runs a command; the agent's own.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// Candidate is a Drive the store could live on.
type Candidate struct {
	// Mount is where the Drive is mounted, not a Pool path: the store must
	// never go through MergerFS.
	Mount string
	// SizeBytes of the filesystem.
	SizeBytes uint64
	// Internal marks the data partition the Installer made from the rest
	// of the system disk (label maitre-data).
	Internal bool
	// InDefaultPool marks a Drive of the Space new volumes default to.
	InDefaultPool bool
}

// Choose picks the store's home, or "" when this Node has nowhere to put
// it and the store stays where podman puts it. The data partition wins
// whatever its size: it is on the OS disk, so it is present whenever the
// Node is, it is an SSD on any machine whose OS disk is one, and using it
// leaves the Pool's Drives entirely to the user's data. Otherwise the
// largest Drive of the default Pool, which has the room.
func Choose(cands []Candidate) string {
	for _, c := range cands {
		if c.Internal && c.Mount != "" {
			return filepath.Join(c.Mount, Dir)
		}
	}
	pool := make([]Candidate, 0, len(cands))
	for _, c := range cands {
		if c.InDefaultPool && c.Mount != "" {
			pool = append(pool, c)
		}
	}
	if len(pool) == 0 {
		return ""
	}
	sort.Slice(pool, func(i, j int) bool {
		if pool[i].SizeBytes != pool[j].SizeBytes {
			return pool[i].SizeBytes > pool[j].SizeBytes
		}
		return pool[i].Mount < pool[j].Mount // stable across boots
	})
	return filepath.Join(pool[0].Mount, Dir)
}

// Configured is the graphroot the written configuration names, or "" when
// nothing has been written and podman uses its own default.
func Configured() string {
	b, err := os.ReadFile(StorageConfig)
	if err != nil {
		return ""
	}
	return graphrootOf(string(b))
}

func graphrootOf(body string) string {
	for _, line := range strings.Split(body, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(key) != "graphroot" {
			continue
		}
		return strings.Trim(strings.TrimSpace(val), `"`)
	}
	return ""
}

// Ensure makes dir the image store, idempotently: the directories, both
// settings, and the SELinux labelling a graphroot outside the default
// place needs. It returns whether anything changed, and it does not move a
// single byte — see Move for that, which is the part that cannot be
// silent.
func Ensure(ctx context.Context, run Runner, dir string) (bool, error) {
	if dir == "" {
		return false, nil
	}
	tmp := filepath.Join(dir, "tmp")
	for _, d := range []string{dir, tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return false, fmt.Errorf("image store %s: %w", d, err)
		}
	}
	changed := false
	// storage.conf: the vendored file with graphroot replaced, so the
	// image's additional image store and overlay options survive.
	base, err := os.ReadFile(VendoredConfig)
	if err != nil {
		// No vendored copy to build on: say the minimum, including the
		// driver, which podman would otherwise have to guess.
		base = []byte("[storage]\ndriver = \"overlay\"\nrunroot = \"/run/containers/storage\"\ngraphroot = \"" + DefaultRoot + "\"\n")
	}
	want := withGraphroot(string(base), dir)
	wrote, err := writeIfDifferent(StorageConfig, want)
	if err != nil {
		return false, err
	}
	changed = changed || wrote
	drop := "# Written by Maître (decision 319). The blob staging directory\n" +
		"# travels with the image store: staging on a root that the store has\n" +
		"# left is what ran out of space in the first place.\n" +
		"[engine]\nimage_copy_tmp_dir = \"" + tmp + "\"\n"
	wrote, err = writeIfDifferent(ContainersDropIn, drop)
	if err != nil {
		return false, err
	}
	changed = changed || wrote
	if changed && run != nil {
		if err := label(ctx, run, dir); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// withGraphroot is body with graphroot pointing at dir, whether or not it
// had one.
func withGraphroot(body, dir string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		key, _, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(key) == "graphroot" {
			lines[i] = "graphroot = \"" + dir + "\""
			return strings.Join(lines, "\n")
		}
	}
	for i, line := range lines {
		if strings.TrimSpace(line) == "[storage]" {
			rest := append([]string{"graphroot = \"" + dir + "\""}, lines[i+1:]...)
			return strings.Join(append(lines[:i+1], rest...), "\n")
		}
	}
	return body + "\n[storage]\ngraphroot = \"" + dir + "\"\n"
}

// label gives the new store the labelling podman's own place has, which
// SELinux needs before a container can read a layer from it. An
// equivalence rule covers everything under it, and relabelling follows.
// Both are logged rather than fatal: a Node with SELinux permissive works
// without them, and nobody can run the commands by hand to find out
// (a Node has no terminal).
func label(ctx context.Context, run Runner, dir string) error {
	// The equivalence makes the policy give dir the same per-path contexts
	// the default graphroot has, which `restorecon -R` then applies. An
	// equivalence that is already there is the one error worth ignoring.
	//
	// These used to be discarded, and that is how a store nothing could
	// run got built and the old one deleted on top of it: `semanage` lives
	// in policycoreutils-python-utils, which the image did not install, so
	// the rule was never added, restorecon applied whatever context
	// /var/mnt/drives/... carries, and every container on the moved store
	// died with "RELRO protection failed" as its loader was refused the
	// mapping. A labelling that did not happen has to be loud.
	if out, err := run.Run(ctx, "semanage", "fcontext", "-a", "-e", DefaultRoot, dir); err != nil && !strings.Contains(out, "already defined") {
		return fmt.Errorf("label %s like %s: %w (%s); semanage comes from policycoreutils-python-utils", dir, DefaultRoot, err, strings.TrimSpace(out))
	}
	if out, err := run.Run(ctx, "restorecon", "-R", dir); err != nil {
		return fmt.Errorf("apply the labels to %s: %w (%s)", dir, err, strings.TrimSpace(out))
	}
	return nil
}

// storeImageIDs is every image the store podman is configured for holds.
// Used either side of a move: an image that does not survive the copy is
// the signal to keep the old store rather than delete it.
func storeImageIDs(ctx context.Context, run Runner) (map[string]bool, error) {
	out, err := run.Run(ctx, "podman", "images", "--all", "--format", "{{.Id}}")
	if err != nil {
		return nil, fmt.Errorf("list the images in the store: %w", err)
	}
	ids := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if id := strings.TrimSpace(line); id != "" {
			ids[id] = true
		}
	}
	return ids, nil
}

func writeIfDifferent(path, body string) (bool, error) {
	if cur, err := os.ReadFile(path); err == nil && string(cur) == body {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// Move takes an image store that is already somewhere — in practice the
// root partition, where every Node before decision 319 has one — and puts
// it where it belongs, then frees what it left behind.
//
// This is deliberately not something Ensure does on its own at boot. The
// copy is local but it is not small (Avalon's store is tens of gigabytes),
// removing the old store while a container still has layers mounted from
// it would break that container, and freeing a Node's root is not a
// decision to take silently. So it is an action someone asks for, with the
// Services stopped by the caller, and it reports what it moved.
//
// Nothing is removed until the copy has succeeded, and the configuration
// is switched only then: an interrupted Move leaves a Node exactly as it
// was, minus some space in the new location that the next attempt reuses.
func Move(ctx context.Context, run Runner, from, to string) (uint64, error) {
	if to == "" {
		return 0, fmt.Errorf("nowhere to move the image store to: this Node has no Drive for it")
	}
	if from == to {
		return 0, nil
	}
	if _, err := os.Stat(from); err != nil {
		// Nothing there to move: configuring the new place is all it takes.
		_, err := Ensure(ctx, run, to)
		return 0, err
	}
	if err := os.MkdirAll(to, 0o700); err != nil {
		return 0, err
	}
	// What the store holds now, to compare with afterwards. Taken before
	// anything is touched, while podman is still configured for `from`.
	was, err := storeImageIDs(ctx, run)
	if err != nil {
		return 0, err
	}
	// The source's own labels are made valid first. `bootc install` leaves
	// files carrying `invalid_bootcinstall_testlabel_t`, a context the
	// policy deliberately does not define so that setting it can be shown
	// to fail, and a copy that preserves xattrs faithfully tries to carry
	// it across: every file then fails with SELINUX_ERR op=setxattr and
	// the copy reports failure. restorecon puts the source back on
	// contexts the policy knows, which are the ones worth copying.
	if out, err := run.Run(ctx, "restorecon", "-R", from); err != nil {
		return 0, fmt.Errorf("relabel %s before copying it: %w (%s)", from, err, strings.TrimSpace(out))
	}
	// -aHAX, not -a: the graphroot's layers carry xattrs, including ones
	// in the trusted namespace, and its metadata is a database. `cp -a`
	// lost images outright — two became one with its tag reduced to
	// <none>, and a round trip lost both — so the copy has to preserve
	// hard links, ACLs and every xattr. rsync is in the image for the
	// Pool's own copies.
	if out, err := run.Run(ctx, "rsync", "-aHAX", "--delete", filepath.Join(from)+"/", to+"/"); err != nil {
		return 0, fmt.Errorf("copy the image store to %s: %w (%s)", to, err, strings.TrimSpace(out))
	}
	if _, err := Ensure(ctx, run, to); err != nil {
		// Labelling or configuration failed, so `to` is not a store
		// anything can run from. Put podman back on the old one, which is
		// untouched, and keep it.
		if _, rerr := Ensure(ctx, run, from); rerr != nil {
			return 0, fmt.Errorf("%w; and %s could not be configured again: %v", err, from, rerr)
		}
		return 0, err
	}
	moved := dirBytes(ctx, run, to)
	// Never delete the old store on the strength of a copy that was not
	// checked. podman now reads `to`, so this asks the new store what it
	// holds; anything the old one had and this one does not means the
	// copy is not a replacement, whatever the exit codes said.
	now, err := storeImageIDs(ctx, run)
	if err == nil {
		var lost []string
		for id := range was {
			if !now[id] {
				lost = append(lost, id)
			}
		}
		if len(lost) > 0 {
			sort.Strings(lost)
			err = fmt.Errorf("the copy at %s is missing %d of the %d images %s holds (%s), so it is not a replacement",
				to, len(lost), len(was), from, strings.Join(lost, " "))
		}
	}
	if err != nil {
		if _, rerr := Ensure(ctx, run, from); rerr != nil {
			return moved, fmt.Errorf("%w; and %s could not be configured again: %v", err, from, rerr)
		}
		return moved, fmt.Errorf("%w; %s is kept and still in use", err, from)
	}
	if err := os.RemoveAll(from); err != nil {
		// The new store is live and correct; the old copy is only waste.
		return moved, fmt.Errorf("the store now lives at %s, but the old one at %s could not be removed: %w", to, from, err)
	}
	return moved, nil
}

// dirBytes is du of a directory, which is wanted once and never on a
// schedule: walking an image store of tens of thousands of layers is slow
// enough that the greenboot check reads statfs instead.
func dirBytes(ctx context.Context, run Runner, dir string) uint64 {
	out, err := run.Run(ctx, "du", "-sb", dir)
	if err != nil {
		return 0
	}
	var n uint64
	if _, err := fmt.Sscanf(strings.TrimSpace(out), "%d", &n); err != nil {
		return 0
	}
	return n
}
