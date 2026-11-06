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
// # How, and why not by configuration
//
// The store is moved by *mounting*, not by configuring: the Drive's
// directory is bind-mounted onto /var/lib/containers/storage, which stays
// podman's graphroot and the only path the store is ever reached by. The
// first design instead wrote the new path into /etc/containers/storage.conf
// and containers.conf.d, and every fault decision 319 collected on
// hardware came from that one choice:
//
//   - `bootc upgrade` stopped working. bootc keeps its own container
//     storage at /run/bootc/storage and reads the same global
//     /etc/containers/containers.conf.d: it overrides graphroot but
//     inherits [engine] static_dir, so pinning static_dir made bootc and
//     podman share one libpod database, each wrote its own graphroot into
//     it, and the other then refused — "database graph root ... does not
//     match our graph root". Boulder could not stage an update at all,
//     and not only while its store was off root. Unpinned, podman fails
//     the same way instead. There is no safe version of writing it.
//   - SELinux. A graphroot outside its usual place carries whatever
//     context /var/mnt/drives/... has, so every container died with
//     "RELRO protection failed" until an `semanage fcontext -e`
//     equivalence was added — from a package the image did not install,
//     written in a form semanage would accept (/var/mnt is equated with
//     /mnt, and it refuses a spec under an equivalency's left side).
//   - libpod's database could not travel with the store, because it
//     records the static dir it was made with, so a move dropped every
//     container and named volume record.
//
// A bind mount removes all of it by construction. Nothing records a path
// that can disagree; bootc sees exactly the configuration it expects; the
// files are labelled for the path they are reached by, which is the path
// the policy already knows; and libpod's recorded graphroot and static dir
// are unchanged by a move, so its database comes along.
//
// What is left to configure is one setting that no longer depends on where
// the store is: containers.conf's image_copy_tmp_dir, pointed at
// /var/lib/containers/storage/tmp. Blob staging is what hit ENOSPC, and
// under the mount that path is on the Drive without naming it.
//
// # Boot
//
// Two mount units, both wanted by local-fs.target, so the store is in
// place long before podman or a Service unit: one for the backing Drive
// itself, by filesystem UUID, because the agent mounts Drives from user
// space and does not run until well after the Quadlet units systemd
// starts on its own; and the bind mount on top of it. Service units carry
// RequiresMountsFor=/var/lib/containers/storage so that a Node whose
// store did not mount starts no Service at all, rather than starting them
// all against an empty store and pulling every image again.
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

// Paths written and read, as package variables so a test need not be root.
var (
	// DefaultRoot is podman's graphroot, and with this design it is also
	// the only path the store is ever reached by, wherever its bytes are.
	DefaultRoot = "/var/lib/containers/storage"
	// UnitDir is where the two mount units go.
	UnitDir = "/etc/systemd/system"
	// ContainersDropIn carries image_copy_tmp_dir, whose value no longer
	// depends on where the store is.
	ContainersDropIn = "/etc/containers/containers.conf.d/50-maitre-image-store.conf"
	// StorageConfig is only ever read and removed now. Decision 319's
	// first design wrote a graphroot into it; see migrate.
	StorageConfig = "/etc/containers/storage.conf"
	// MountInfo says what is mounted where.
	MountInfo = "/proc/self/mountinfo"
	// DevByUUID is where the backing Drive's UUID is checked before a
	// mount unit is written naming it.
	DevByUUID = "/dev/disk/by-uuid"
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

// Configured is the directory the bind mount's unit names, or "" when
// there is no such unit and the store's bytes are wherever podman put
// them. It is what has been *arranged*; Backing is what is true now.
func Configured() string {
	b, err := os.ReadFile(filepath.Join(UnitDir, BindUnit()))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "What="); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// Backing is where the store's bytes are: the Drive directory mounted
// onto DefaultRoot, the legacy graphroot of a Node that was moved by the
// configuration this design replaces, or DefaultRoot itself.
func Backing() string {
	if dir := Configured(); dir != "" && Mounted(DefaultRoot) {
		return dir
	}
	if dir := legacyGraphroot(); dir != "" && dir != DefaultRoot {
		return dir
	}
	return DefaultRoot
}

// legacyGraphroot is the graphroot decision 319's first design wrote into
// /etc/containers/storage.conf, or "". A Node in that state cannot stage
// an update, so finding one is a thing to act on, not to report.
func legacyGraphroot() string {
	b, err := os.ReadFile(StorageConfig)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(key) != "graphroot" {
			continue
		}
		return strings.Trim(strings.TrimSpace(val), `"`)
	}
	return ""
}

// Mounted reports whether anything is mounted at path.
func Mounted(path string) bool {
	b, err := os.ReadFile(MountInfo)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) > 4 && unescapeMountInfo(f[4]) == path {
			return true
		}
	}
	return false
}

// unescapeMountInfo undoes the octal escaping the kernel uses for spaces,
// tabs, newlines and backslashes in mountinfo's paths.
func unescapeMountInfo(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			var n int
			if _, err := fmt.Sscanf(s[i+1:i+4], "%o", &n); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Ensure makes dir the image store, idempotently: the directories, the
// two mount units, and the mount itself. It returns whether anything
// changed, and it does not move a single byte — see Move for that, which
// is the part that cannot be silent.
//
// dir == DefaultRoot takes the arrangement away again, which is how a
// Node gets back to podman's own store.
func Ensure(ctx context.Context, run Runner, dir string) (bool, error) {
	if dir == "" {
		return false, nil
	}
	changed, err := writeIfDifferent(ContainersDropIn, dropIn())
	if err != nil {
		return false, err
	}
	if dir == DefaultRoot {
		c, err := unbind(ctx, run)
		return changed || c, err
	}
	tmp := filepath.Join(dir, "tmp")
	for _, d := range []string{dir, tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return changed, fmt.Errorf("image store %s: %w", d, err)
		}
	}
	wrote, err := writeUnits(ctx, run, dir)
	if err != nil {
		return changed, err
	}
	changed = changed || wrote
	if !Mounted(DefaultRoot) {
		if err := os.MkdirAll(DefaultRoot, 0o700); err != nil {
			return changed, err
		}
		if run != nil {
			if out, err := run.Run(ctx, "systemctl", "start", BindUnit()); err != nil {
				return changed, fmt.Errorf("mount the image store at %s: %w (%s)", DefaultRoot, err, strings.TrimSpace(out))
			}
		}
		changed = true
	}
	return changed, nil
}

func dropIn() string {
	return "# Written by Maître (decision 319). The image store lives on a Drive,\n" +
		"# bind-mounted onto " + DefaultRoot + " -- see internal/imagestore.\n" +
		"#\n" +
		"# Blob staging is what ran out of space in the first place, so it goes\n" +
		"# inside the store. Under the mount this path is on the Drive without\n" +
		"# naming it, which is the point: this file is global and bootc reads it\n" +
		"# too, and every value here that depended on where the store was is\n" +
		"# what broke `bootc upgrade`. static_dir especially is NOT set: bootc\n" +
		"# inherits it, and pinning it made bootc and podman share one libpod\n" +
		"# database that each then refused to open.\n" +
		"[engine]\nimage_copy_tmp_dir = \"" + filepath.Join(DefaultRoot, "tmp") + "\"\n"
}

// BindUnit is the name of the unit that mounts the store at DefaultRoot.
func BindUnit() string { return unitName(DefaultRoot) }

// driveUnit is the name of the unit that mounts the Drive the store is
// on. The agent mounts Drives itself, from user space, long after systemd
// has started the Quadlet units that need the store, so the one Drive the
// store lives on gets mounted by systemd instead. The agent's own Mount is
// idempotent and finds it already there.
func driveUnit(mount string) string { return unitName(mount) }

// unitName is systemd's name for a mount unit at path.
func unitName(path string) string {
	p := strings.Trim(path, "/")
	if p == "" {
		return "-.mount"
	}
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '/':
			b.WriteByte('-')
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
			b.WriteByte(c)
		case c == '.' && i > 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, `\x%02x`, c)
		}
	}
	b.WriteString(".mount")
	return b.String()
}

// driveOf is the mount point of the Drive dir sits on, and its filesystem
// UUID, which is the directory name the agent mounts every Drive under.
func driveOf(dir string) (mount, uuid string, err error) {
	rest, ok := strings.CutSuffix(filepath.Clean(dir), string(filepath.Separator)+Dir)
	if !ok {
		return "", "", fmt.Errorf("%s is not a <drive>/%s directory, so there is no Drive to mount for it", dir, Dir)
	}
	return rest, filepath.Base(rest), nil
}

// fstypeAt is the filesystem type mounted at path, from mountinfo.
func fstypeAt(path string) string {
	b, err := os.ReadFile(MountInfo)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || unescapeMountInfo(f[4]) != path {
			continue
		}
		for i := 5; i < len(f); i++ {
			if f[i] == "-" && i+1 < len(f) {
				return f[i+1]
			}
		}
	}
	return ""
}

// writeUnits writes the Drive's mount unit and the bind mount on top of
// it, and enables both. Nothing is started here.
func writeUnits(ctx context.Context, run Runner, dir string) (bool, error) {
	mount, uuid, err := driveOf(dir)
	if err != nil {
		return false, err
	}
	// A unit naming a UUID no device has would fail at every boot, and
	// with RequiresMountsFor on the Service units that means a Node that
	// runs nothing. Better to refuse now, while the store is still where
	// it works.
	if _, err := os.Stat(filepath.Join(DevByUUID, uuid)); err != nil {
		return false, fmt.Errorf("no filesystem with UUID %s (%s): the image store's Drive must be named by UUID, since its mount unit runs before anything that could look it up", uuid, filepath.Join(DevByUUID, uuid))
	}
	fstype := fstypeAt(mount)
	if fstype == "ntfs" {
		fstype = "ntfs3" // as storage.Manager.Mount does
	}
	drive := "# Written by Maître (decision 319): the Drive the image store lives on.\n" +
		"# Mounted here rather than by the agent, which runs from user space and\n" +
		"# starts well after the Service units that need the store.\n" +
		"[Unit]\n" +
		"Description=Maître image store Drive\n" +
		"Documentation=https://github.com/Onfire7/maitre-de-serveur\n" +
		"[Mount]\n" +
		"What=/dev/disk/by-uuid/" + uuid + "\n" +
		"Where=" + mount + "\n" +
		typeLine(fstype) +
		"Options=noatime\n" +
		"[Install]\nWantedBy=local-fs.target\n"
	bind := "# Written by Maître (decision 319): the image store, off the root\n" +
		"# partition. The bytes are on a Drive and the path is podman's own\n" +
		"# graphroot, so nothing -- not podman, not bootc, not the SELinux\n" +
		"# policy -- has to be told the store moved.\n" +
		"[Unit]\n" +
		"Description=Maître image store\n" +
		"Documentation=https://github.com/Onfire7/maitre-de-serveur\n" +
		"Requires=" + driveUnit(mount) + "\n" +
		"After=" + driveUnit(mount) + "\n" +
		"[Mount]\n" +
		"What=" + dir + "\n" +
		"Where=" + DefaultRoot + "\n" +
		"Type=none\n" +
		"Options=bind\n" +
		"[Install]\nWantedBy=local-fs.target\n"
	changed := false
	for _, u := range []struct{ name, body string }{
		{driveUnit(mount), drive},
		{BindUnit(), bind},
	} {
		wrote, err := writeIfDifferent(filepath.Join(UnitDir, u.name), u.body)
		if err != nil {
			return changed, err
		}
		changed = changed || wrote
	}
	if !changed || run == nil {
		return changed, nil
	}
	if out, err := run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return changed, fmt.Errorf("daemon-reload: %w (%s)", err, strings.TrimSpace(out))
	}
	if out, err := run.Run(ctx, "systemctl", "enable", driveUnit(mount), BindUnit()); err != nil {
		return changed, fmt.Errorf("enable the image store's mount units: %w (%s)", err, strings.TrimSpace(out))
	}
	return changed, nil
}

func typeLine(fstype string) string {
	if fstype == "" {
		// mount(8) probes. Only reached when the Drive is not mounted
		// while the unit is written, which Choose does not do.
		return ""
	}
	return "Type=" + fstype + "\n"
}

// unbind takes the mount units away, which is how the store goes back to
// being an ordinary directory on the root partition. The mount is stopped
// first, so what the caller then sees at DefaultRoot is the real
// directory and not the Drive's.
func unbind(ctx context.Context, run Runner) (bool, error) {
	changed := false
	if run != nil && Mounted(DefaultRoot) {
		if out, err := run.Run(ctx, "systemctl", "stop", BindUnit()); err != nil {
			return false, fmt.Errorf("unmount the image store from %s: %w (%s)", DefaultRoot, err, strings.TrimSpace(out))
		}
		changed = true
	}
	units := []string{BindUnit()}
	if dir := Configured(); dir != "" {
		if mount, _, err := driveOf(dir); err == nil {
			units = append(units, driveUnit(mount))
		}
	}
	for _, u := range units {
		path := filepath.Join(UnitDir, u)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if run != nil {
			if out, err := run.Run(ctx, "systemctl", "disable", u); err != nil {
				return changed, fmt.Errorf("disable %s: %w (%s)", u, err, strings.TrimSpace(out))
			}
		}
		if err := os.Remove(path); err != nil {
			return changed, err
		}
		changed = true
	}
	if changed && run != nil {
		if out, err := run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
			return changed, fmt.Errorf("daemon-reload: %w (%s)", err, strings.TrimSpace(out))
		}
	}
	return changed, nil
}

// Migrate repairs a Node that decision 319's first design moved by
// configuration. Such a Node cannot stage an update at all, so this is
// not optional and it does not wait to be asked: it bind-mounts the store
// it already has onto podman's own graphroot and deletes the
// configuration that named it. Not one byte is copied — only the path the
// same directory is reached by changes.
//
// libpod's database does not survive that change of path, because it
// records the graphroot it was made with, so it goes. What it holds is
// containers, pods and named volumes; Maître keeps none of its own state
// there (a Service is a Quadlet unit recreated on start, its volumes bind
// mounts from a Space), and podman builds a fresh one.
func Migrate(ctx context.Context, run Runner) (bool, error) {
	dir := legacyGraphroot()
	if dir == "" || dir == DefaultRoot {
		return false, nil
	}
	if _, err := os.Stat(dir); err != nil {
		// The configuration names a store that is not there, which is
		// worse than either state: remove it and let podman use its own.
		if err := os.Remove(StorageConfig); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := os.RemoveAll(filepath.Join(dir, "libpod")); err != nil {
		return false, err
	}
	if _, err := Ensure(ctx, run, dir); err != nil {
		return false, fmt.Errorf("mount the image store at %s instead of configuring it: %w", DefaultRoot, err)
	}
	if err := os.Remove(StorageConfig); err != nil {
		return true, fmt.Errorf("the store is mounted at %s, but %s still names it directly: %w", DefaultRoot, StorageConfig, err)
	}
	return true, nil
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

// Move takes the image store's bytes from where they are to where they
// belong, and frees what they left behind.
//
// This is deliberately not something Ensure does on its own at boot. The
// copy is local but it is not small (Avalon's store is tens of
// gigabytes), removing the old store while a container still has layers
// mounted from it would break that container, and freeing a Node's root
// is not a decision to take silently. So it is an action someone asks
// for, with the Services stopped by the caller, and it reports what it
// moved.
//
// Nothing is removed until the copy has been checked against what the
// store held before, image by image. An interrupted Move leaves the Node
// able to run every Service it could before: the store it was using is
// only ever renamed aside, never deleted, until the new one has answered
// `podman images` correctly through the path everything uses.
func Move(ctx context.Context, run Runner, from, to string) (uint64, error) {
	if to == "" {
		return 0, fmt.Errorf("nowhere to move the image store to: this Node has no Drive for it")
	}
	if from == to {
		return 0, nil
	}
	if to == DefaultRoot {
		return moveToRoot(ctx, run, from)
	}
	return moveToDrive(ctx, run, from, to)
}

// moveToDrive copies the store onto a Drive and mounts it over the place
// it came from. The order is what makes it safe: copy, rename the old
// store aside, mount, check, and only then delete.
func moveToDrive(ctx context.Context, run Runner, from, to string) (uint64, error) {
	if _, err := os.Stat(from); err != nil {
		// Nothing there to move: arranging the new place is all it takes.
		_, err := Ensure(ctx, run, to)
		return 0, err
	}
	// What the store holds now, while it is still the one podman reads.
	was, err := storeImageIDs(ctx, run)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(to, 0o700); err != nil {
		return 0, err
	}
	if err := copyStore(ctx, run, from, to); err != nil {
		return 0, err
	}
	// The old store is renamed, not removed: the bind mount would hide it
	// anyway, and a rename on one filesystem is instant and reversible.
	// Everything after this point puts it back on any failure.
	aside := from + ".moved-aside"
	if err := os.RemoveAll(aside); err != nil {
		return 0, err
	}
	if err := os.Rename(from, aside); err != nil {
		return 0, fmt.Errorf("move %s aside: %w", from, err)
	}
	moved := dirBytes(ctx, run, to)
	undo := func(cause error) (uint64, error) {
		if _, err := unbind(ctx, run); err != nil {
			return moved, fmt.Errorf("%w; and the mount could not be undone either: %v", cause, err)
		}
		_ = os.Remove(from)
		if err := os.Rename(aside, from); err != nil {
			return moved, fmt.Errorf("%w; and %s could not be put back from %s: %v", cause, from, aside, err)
		}
		return moved, fmt.Errorf("%w; the store at %s is kept and still in use", cause, from)
	}
	if _, err := Ensure(ctx, run, to); err != nil {
		return undo(err)
	}
	if err := check(ctx, run, was, from, to); err != nil {
		return undo(err)
	}
	if err := os.RemoveAll(aside); err != nil {
		// The store is live and correct at the new place; what is left
		// behind is only waste, and it is on the partition this was meant
		// to free.
		return moved, fmt.Errorf("the store now lives on %s, but the copy it replaced could not be removed from %s: %w", to, aside, err)
	}
	return moved, nil
}

// moveToRoot brings the store back to the root partition, which is the
// way out for a Node whose Drive is going away. The staging copy lands
// next to the mount point rather than under it, because the mount hides
// the directory it is on top of: only once it is unmounted can the real
// directory be replaced.
func moveToRoot(ctx context.Context, run Runner, from string) (uint64, error) {
	if !Mounted(DefaultRoot) && legacyGraphroot() == "" {
		// Nothing is standing between podman and its own store.
		_, err := unbind(ctx, run)
		return 0, err
	}
	was, err := storeImageIDs(ctx, run)
	if err != nil {
		return 0, err
	}
	incoming := DefaultRoot + ".incoming"
	if err := os.RemoveAll(incoming); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(incoming, 0o700); err != nil {
		return 0, err
	}
	// Read through the path everything uses, so this works whether the
	// store is bind-mounted or named by a legacy graphroot.
	src := DefaultRoot
	if !Mounted(DefaultRoot) {
		src = from
	}
	if err := copyStore(ctx, run, src, incoming); err != nil {
		return 0, err
	}
	moved := dirBytes(ctx, run, incoming)
	if _, err := unbind(ctx, run); err != nil {
		return moved, err
	}
	// Whatever is at DefaultRoot now is the directory the mount hid: on a
	// Node that moved off root it is the empty mount point, and on one
	// that never did it is a store nobody has used since.
	aside := DefaultRoot + ".replaced"
	if err := os.RemoveAll(aside); err != nil {
		return moved, err
	}
	if err := os.Rename(DefaultRoot, aside); err != nil {
		return moved, fmt.Errorf("move %s aside: %w", DefaultRoot, err)
	}
	if err := os.Rename(incoming, DefaultRoot); err != nil {
		_ = os.Rename(aside, DefaultRoot)
		return moved, fmt.Errorf("put the store back at %s: %w", DefaultRoot, err)
	}
	// The legacy configuration, if this was a Node moved by it, says the
	// store is still on the Drive. It is not, from here on.
	if legacyGraphroot() != "" {
		if err := os.Remove(StorageConfig); err != nil {
			return moved, fmt.Errorf("the store is back at %s, but %s still names %s: %w", DefaultRoot, StorageConfig, from, err)
		}
	}
	if _, err := Ensure(ctx, run, DefaultRoot); err != nil {
		return moved, err
	}
	if err := check(ctx, run, was, from, DefaultRoot); err != nil {
		// Put the Drive's store back in service: it is untouched, and it
		// is the one that is known to work.
		_ = os.RemoveAll(DefaultRoot)
		_ = os.Rename(aside, DefaultRoot)
		if _, rerr := Ensure(ctx, run, from); rerr != nil {
			return moved, fmt.Errorf("%w; and %s could not be mounted again: %v", err, from, rerr)
		}
		return moved, fmt.Errorf("%w; the store on %s is kept and still in use", err, from)
	}
	if err := os.RemoveAll(aside); err != nil {
		return moved, fmt.Errorf("the store is back at %s, but the empty mount point at %s could not be removed: %w", DefaultRoot, aside, err)
	}
	if err := os.RemoveAll(from); err != nil {
		return moved, fmt.Errorf("the store is back at %s, but the old one at %s could not be removed: %w", DefaultRoot, from, err)
	}
	return moved, nil
}

// copyStore copies a store's bytes. -aHAX, not -a: the graphroot's layers
// carry xattrs, including ones in the trusted namespace, and its metadata
// is a database. `cp -a` lost images outright — two became one with its
// tag reduced to <none>, and a round trip lost both — so the copy has to
// preserve hard links, ACLs and every xattr. rsync is in the image for
// the Pool's own copies.
//
// libpod's database comes along, which it could not when the store moved
// by configuration: it records the graphroot and static dir it was made
// with, and under a bind mount both are the same path afterwards as
// before. So a move keeps this Node's containers and named volumes
// instead of dropping them.
func copyStore(ctx context.Context, run Runner, from, to string) error {
	// The source's own labels are made valid first. `bootc install` leaves
	// files carrying `invalid_bootcinstall_testlabel_t`, a context the
	// policy deliberately does not define so that setting it can be shown
	// to fail, and a copy that preserves xattrs faithfully tries to carry
	// it across: every file then fails with SELINUX_ERR op=setxattr and
	// the copy reports failure. restorecon puts the source back on
	// contexts the policy knows, which are the ones worth copying.
	if out, err := run.Run(ctx, "restorecon", "-R", from); err != nil {
		return fmt.Errorf("relabel %s before copying it: %w (%s)", from, err, strings.TrimSpace(out))
	}
	if out, err := run.Run(ctx, "rsync", "-aHAX", "--delete", from+"/", to+"/"); err != nil {
		return fmt.Errorf("copy the image store to %s: %w (%s)", to, err, strings.TrimSpace(out))
	}
	return nil
}

// check asks the store that is now mounted what it holds. Never delete
// the old one on the strength of a copy that was not checked: whatever
// the exit codes said, an image the old store had and this one does not
// means the copy is not a replacement.
func check(ctx context.Context, run Runner, was map[string]bool, from, to string) error {
	now, err := storeImageIDs(ctx, run)
	if err != nil {
		return err
	}
	var lost []string
	for id := range was {
		if !now[id] {
			lost = append(lost, id)
		}
	}
	if len(lost) == 0 {
		return nil
	}
	sort.Strings(lost)
	return fmt.Errorf("the copy at %s is missing %d of the %d images %s holds (%s), so it is not a replacement",
		to, len(lost), len(was), from, strings.Join(lost, " "))
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
