package imagestore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type runner struct{ cmds []string }

func (r *runner) Run(_ context.Context, name string, args ...string) (string, error) {
	r.cmds = append(r.cmds, name+" "+strings.Join(args, " "))
	return "", nil
}

func (r *runner) ran(prefix string) bool {
	for _, c := range r.cmds {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// sandbox points every path this package writes at a temporary directory,
// and gives it a mountinfo and a /dev/disk/by-uuid to read. A Drive whose
// UUID has no device is refused, so a test that wants one says so.
type sandbox struct {
	dir   string
	root  string // DefaultRoot, inside dir
	drive string // the Drive's mount point
	store string // the store on the Drive
	uuid  string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	dir := t.TempDir()
	s := &sandbox{
		dir:  dir,
		root: filepath.Join(dir, "var", "lib", "containers", "storage"),
		uuid: "0f9c5a62-1c3a-4a2e-9c56-9b3a1f0e7e21",
	}
	s.drive = filepath.Join(dir, "var", "mnt", "drives", s.uuid)
	s.store = filepath.Join(s.drive, Dir)

	old := []string{DefaultRoot, UnitDir, ContainersDropIn, StorageConfig, MountInfo, DevByUUID}
	t.Cleanup(func() {
		DefaultRoot, UnitDir, ContainersDropIn, StorageConfig, MountInfo, DevByUUID =
			old[0], old[1], old[2], old[3], old[4], old[5]
	})
	DefaultRoot = s.root
	UnitDir = filepath.Join(dir, "etc", "systemd", "system")
	ContainersDropIn = filepath.Join(dir, "etc", "containers", "containers.conf.d", "50-maitre.conf")
	StorageConfig = filepath.Join(dir, "etc", "containers", "storage.conf")
	MountInfo = filepath.Join(dir, "mountinfo")
	DevByUUID = filepath.Join(dir, "dev", "disk", "by-uuid")
	for _, d := range []string{UnitDir, DevByUUID, s.root, filepath.Dir(StorageConfig)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s.setMounts()
	return s
}

// withDevice makes the Drive's UUID resolvable, as it is on a Node whose
// Drive is actually there.
func (s *sandbox) withDevice(t *testing.T) *sandbox {
	t.Helper()
	if err := os.WriteFile(filepath.Join(DevByUUID, s.uuid), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return s
}

// setMounts writes a mountinfo naming the Drive's mount point plus any
// extra mount points given, in the kernel's own format.
func (s *sandbox) setMounts(extra ...string) {
	lines := []string{
		"25 1 254:1 / / rw,relatime shared:1 - xfs /dev/vda4 rw",
		"41 25 8:5 / " + s.drive + " rw,noatime shared:2 - ext4 /dev/sda5 rw",
	}
	for _, m := range extra {
		lines = append(lines, "77 25 8:5 /"+Dir+" "+m+" rw,noatime shared:3 - ext4 /dev/sda5 rw")
	}
	if err := os.WriteFile(MountInfo, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		panic(err)
	}
}

// The user settled where it goes: the OS disk's data partition when there
// is one, otherwise the largest Drive of the default Pool.
func TestWhereTheStoreGoes(t *testing.T) {
	data := Candidate{Mount: "/var/mnt/drives/data", SizeBytes: 26 << 30, Internal: true}
	small := Candidate{Mount: "/var/mnt/drives/a", SizeBytes: 1 << 40, InDefaultPool: true}
	big := Candidate{Mount: "/var/mnt/drives/b", SizeBytes: 8 << 40, InDefaultPool: true}
	elsewhere := Candidate{Mount: "/var/mnt/drives/c", SizeBytes: 9 << 40}

	// The data partition wins even against far larger Pool Drives: it is on
	// the OS disk, so it is there whenever the Node is, and it leaves the
	// Pool entirely to the user's data.
	if got := Choose([]Candidate{small, big, data}); got != "/var/mnt/drives/data/"+Dir {
		t.Fatalf("got %q", got)
	}
	// Without one, the largest Drive of the default Pool.
	if got := Choose([]Candidate{small, big, elsewhere}); got != "/var/mnt/drives/b/"+Dir {
		t.Fatalf("got %q", got)
	}
	// A Drive in no Pool of ours is not a candidate at all.
	if got := Choose([]Candidate{elsewhere}); got != "" {
		t.Fatalf("got %q", got)
	}
	// And a Node with no Drive has nowhere: the store stays where podman
	// puts it, and such a Node can run no Service anyway.
	if got := Choose(nil); got != "" {
		t.Fatalf("got %q", got)
	}
}

// The store moves by mounting, not by configuring. Everything decision 319
// collected on hardware came from writing the new path into storage.conf
// and containers.conf: `bootc upgrade` stopped working, SELinux refused
// every container's loader, and libpod's database could not travel. So
// nothing may name the store's real location but the mount units.
func TestEnsureMountsTheStoreAndConfiguresNothing(t *testing.T) {
	s := newSandbox(t).withDevice(t)
	run := &runner{}

	changed, err := Ensure(context.Background(), run, s.store)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}

	// No graphroot anywhere: podman's own is the only one, which is why
	// bootc still recognises the Node's configuration as its own.
	if _, err := os.Stat(StorageConfig); err == nil {
		b, _ := os.ReadFile(StorageConfig)
		t.Fatalf("storage.conf was written, which is what broke bootc:\n%s", b)
	}
	drop, err := os.ReadFile(ContainersDropIn)
	if err != nil {
		t.Fatal(err)
	}
	var settings []string
	for _, line := range strings.Split(string(drop), "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "#") {
			settings = append(settings, l)
		}
	}
	for _, l := range settings {
		// static_dir is the one bootc inherits, and graphroot the one it
		// overrides: either, written here, is a path that can disagree.
		for _, forbidden := range []string{"static_dir", "graphroot", "runroot"} {
			if strings.HasPrefix(l, forbidden) {
				t.Errorf("the drop-in sets %s: %q", forbidden, l)
			}
		}
		// Nor may anything in it name the Drive: the value has to mean the
		// same thing whether or not the store has moved.
		if strings.Contains(l, s.drive) {
			t.Errorf("the drop-in names the Drive, so it would have to be rewritten by every move: %q", l)
		}
	}
	if want := "image_copy_tmp_dir = \"" + filepath.Join(s.root, "tmp") + "\""; !strings.Contains(string(drop), want) {
		t.Errorf("the drop-in does not stage blobs inside the store (%s):\n%s", want, drop)
	}

	// The bind mount, and the Drive under it: the agent mounts Drives from
	// user space long after systemd starts the Service units that need the
	// store, so the store's own Drive is mounted by systemd, by UUID.
	bind, err := os.ReadFile(filepath.Join(UnitDir, BindUnit()))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"What=" + s.store,
		"Where=" + s.root,
		"Options=bind",
		"Requires=" + unitName(s.drive),
		"WantedBy=local-fs.target",
	} {
		if !strings.Contains(string(bind), want) {
			t.Errorf("the bind mount unit lacks %q:\n%s", want, bind)
		}
	}
	drive, err := os.ReadFile(filepath.Join(UnitDir, unitName(s.drive)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"What=/dev/disk/by-uuid/" + s.uuid,
		"Where=" + s.drive,
		"Type=ext4", // from mountinfo, not assumed
		"WantedBy=local-fs.target",
	} {
		if !strings.Contains(string(drive), want) {
			t.Errorf("the Drive's mount unit lacks %q:\n%s", want, drive)
		}
	}
	if !run.ran("systemctl daemon-reload") || !run.ran("systemctl enable "+unitName(s.drive)) || !run.ran("systemctl start "+BindUnit()) {
		t.Errorf("the units were written but not enabled and started: %v", run.cmds)
	}
	// No SELinux work at all. The store is reached by the path the policy
	// already knows, so there is nothing to equate and nothing to relabel.
	for _, c := range run.cmds {
		if strings.HasPrefix(c, "semanage") {
			t.Errorf("semanage is no longer needed, and the image does not ship it: %q", c)
		}
	}
	if Configured() != s.store {
		t.Errorf("Configured() = %q, want %q", Configured(), s.store)
	}

	// Idempotent, and once the mount is up it is not started again.
	s.setMounts(s.root)
	run.cmds = nil
	if changed, err := Ensure(context.Background(), run, s.store); err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if len(run.cmds) != 0 {
		t.Fatalf("ran commands with nothing to do: %v", run.cmds)
	}
	if Backing() != s.store {
		t.Errorf("Backing() = %q, want %q", Backing(), s.store)
	}
}

// A mount unit naming a UUID no device has would fail at every boot, and
// with RequiresMountsFor on the Service units that means a Node that runs
// nothing at all. Refuse while the store is still where it works.
func TestEnsureRefusesADriveItCannotNameByUUID(t *testing.T) {
	s := newSandbox(t) // no device for the UUID
	_, err := Ensure(context.Background(), &runner{}, s.store)
	if err == nil {
		t.Fatal("a mount unit was written for a UUID no device has")
	}
	if !strings.Contains(err.Error(), s.uuid) {
		t.Errorf("the error does not name the UUID: %v", err)
	}
	if _, err := os.Stat(filepath.Join(UnitDir, BindUnit())); err == nil {
		t.Error("the bind mount unit was written anyway")
	}
}

// systemd's name for a mount unit is the mount point, and getting it
// wrong writes a file systemd never reads.
func TestUnitNamesAreSystemdsOwn(t *testing.T) {
	for in, want := range map[string]string{
		"/var/lib/containers/storage":            "var-lib-containers-storage.mount",
		"/var/mnt/drives/abc-def":                `var-mnt-drives-abc\x2ddef.mount`,
		"/var/mnt/drives/0f9c5a62/.maitre/store": `var-mnt-drives-0f9c5a62-.maitre-store.mount`,
		"/":                                      "-.mount",
	} {
		if got := unitName(in); got != want {
			t.Errorf("unitName(%q) = %q, want %q", in, got, want)
		}
	}
}

// moveRunner answers the three commands a move leans on: what the store
// holds before and after, rsync, and du. The store it reports changes when
// the bind mount is started, which is the moment podman starts reading the
// new bytes.
type moveRunner struct {
	cmds     []string
	before   string
	after    string
	switched bool
	s        *sandbox
}

func (r *moveRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	r.cmds = append(r.cmds, name+" "+strings.Join(args, " "))
	switch {
	case name == "podman":
		if r.switched {
			return r.after, nil
		}
		return r.before, nil
	case name == "du":
		return "1024\t/x\n", nil
	case name == "systemctl" && len(args) > 1 && args[0] == "start":
		// The mount is up from here on, and podman reads the new store.
		r.s.setMounts(r.s.root)
		r.switched = true
	case name == "systemctl" && len(args) > 1 && args[0] == "stop":
		r.s.setMounts()
		r.switched = false
	}
	return "", nil
}

func (r *moveRunner) ran(prefix string) bool {
	for _, c := range r.cmds {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// The move copies, mounts, checks, and only then deletes. What it is
// protecting against is what `cp -a` once did: two images became one with
// its tag reduced to <none>, and the old store was removed on top of
// that, which on a Node with a 23 GiB store would have left every Service
// unable to start and nothing to recover from.
func TestMoveKeepsTheOldStoreWhenAnImageDoesNotSurvive(t *testing.T) {
	s := newSandbox(t).withDevice(t)
	if err := os.WriteFile(filepath.Join(s.root, "layer"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &moveRunner{before: "aaa\nbbb\n", after: "aaa\n", s: s}

	_, err := Move(context.Background(), r, s.root, s.store)
	if err == nil {
		t.Fatal("the move reported success although an image did not survive the copy")
	}
	if !strings.Contains(err.Error(), "bbb") {
		t.Errorf("the error does not name the lost image: %v", err)
	}
	// The store that works is back in place, with its contents, and
	// nothing is mounted over it.
	if _, err := os.Stat(filepath.Join(s.root, "layer")); err != nil {
		t.Errorf("the old store was not put back: %v", err)
	}
	if _, err := os.Stat(s.root + ".moved-aside"); err == nil {
		t.Error("the old store was left renamed aside")
	}
	if Configured() != "" {
		t.Errorf("the mount units were left behind: Configured() = %q", Configured())
	}
	if !r.ran("systemctl stop " + BindUnit()) {
		t.Errorf("the mount was not undone: %v", r.cmds)
	}
}

// libpod's database travels with the store now, which it could not when
// the store moved by configuration: under a bind mount the graphroot and
// static dir it records are the same paths afterwards as before, so a
// move keeps this Node's containers and named volumes instead of
// dropping them.
func TestMoveBringsLibpodAlong(t *testing.T) {
	s := newSandbox(t).withDevice(t)
	if err := os.MkdirAll(filepath.Join(s.root, "libpod"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := &moveRunner{before: "aaa\n", after: "aaa\n", s: s}

	if _, err := Move(context.Background(), r, s.root, s.store); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if !r.ran("rsync -aHAX --delete " + s.root + "/ " + s.store + "/") {
		t.Errorf("the copy is not the one expected: %v", r.cmds)
	}
	for _, c := range r.cmds {
		if strings.Contains(c, "libpod") {
			t.Errorf("libpod is still being excluded or cleared: %q", c)
		}
	}
	// And the root partition is given back: the old store is gone, the
	// mount is up, and the units will bring it back at the next boot.
	if _, err := os.Stat(s.root + ".moved-aside"); err == nil {
		t.Error("the old store was not removed after the copy was checked")
	}
	if Backing() != s.store {
		t.Errorf("Backing() = %q, want %q", Backing(), s.store)
	}
}

// Moving back is the way out for a Node whose Drive is going away. The
// mount hides the directory it is on top of, so the copy has to land
// beside the mount point and be swapped in once it is unmounted.
func TestMoveBackToRoot(t *testing.T) {
	s := newSandbox(t).withDevice(t)
	if err := os.MkdirAll(s.store, 0o700); err != nil {
		t.Fatal(err)
	}
	r := &moveRunner{before: "aaa\n", after: "aaa\n", s: s}
	if _, err := Ensure(context.Background(), r, s.store); err != nil {
		t.Fatal(err)
	}
	r.cmds = nil

	if _, err := Move(context.Background(), r, s.store, s.root); err != nil {
		t.Fatalf("Move back: %v", err)
	}
	// Read through the path everything uses, written beside it, swapped in.
	if !r.ran("rsync -aHAX --delete " + s.root + "/ " + s.root + ".incoming/") {
		t.Errorf("the copy did not read the store through its mounted path: %v", r.cmds)
	}
	if Configured() != "" {
		t.Errorf("the mount units are still there: Configured() = %q", Configured())
	}
	if Backing() != s.root {
		t.Errorf("Backing() = %q, want %q", Backing(), s.root)
	}
	for _, left := range []string{s.root + ".incoming", s.root + ".replaced", s.store} {
		if _, err := os.Stat(left); err == nil {
			t.Errorf("%s was left behind", left)
		}
	}
	if _, err := os.Stat(s.root); err != nil {
		t.Errorf("there is no store at %s afterwards: %v", s.root, err)
	}
}

// A Node that decision 319's first design moved by configuration cannot
// stage an update at all, and the repair costs nothing: the same
// directory is simply mounted at podman's own graphroot, and the
// configuration that named it goes. Not one byte is copied.
func TestMigrateAStoreMovedByConfiguration(t *testing.T) {
	s := newSandbox(t).withDevice(t)
	if err := os.MkdirAll(filepath.Join(s.store, "libpod"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.store, "layer"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StorageConfig, []byte("[storage]\ndriver = \"overlay\"\ngraphroot = \""+s.store+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Before the repair the Node's store is named by configuration, which
	// is the state that stops `bootc upgrade`.
	if Backing() != s.store {
		t.Fatalf("Backing() = %q, want the legacy graphroot %q", Backing(), s.store)
	}
	r := &moveRunner{before: "aaa\n", after: "aaa\n", s: s}

	did, err := Migrate(context.Background(), r)
	if err != nil || !did {
		t.Fatalf("did=%v err=%v", did, err)
	}
	if _, err := os.Stat(StorageConfig); err == nil {
		t.Error("the configuration that broke bootc is still there")
	}
	if Configured() != s.store {
		t.Errorf("Configured() = %q, want %q", Configured(), s.store)
	}
	// The images stayed where they were; only the path changed.
	if _, err := os.Stat(filepath.Join(s.store, "layer")); err != nil {
		t.Errorf("the store's contents did not survive: %v", err)
	}
	if !r.ran("systemctl start " + BindUnit()) {
		t.Errorf("the store was not mounted: %v", r.cmds)
	}
	// libpod's database cannot survive the change of path: it records the
	// graphroot it was made with, and podman refuses to open one whose
	// recorded root is not the configured one.
	if _, err := os.Stat(filepath.Join(s.store, "libpod")); err == nil {
		t.Error("libpod's database was kept, and podman will refuse to open it at the new path")
	}
	// Nothing to do on a second pass, or on a Node that was never moved.
	if did, err := Migrate(context.Background(), r); err != nil || did {
		t.Fatalf("did=%v err=%v on a Node already repaired", did, err)
	}
}

// Nowhere to move to is an error, not a silent no-op: it is the answer
// for a Node with no Drive, and the Console says so.
func TestMoveWithNowhereToGo(t *testing.T) {
	if _, err := Move(context.Background(), &runner{}, DefaultRoot, ""); err == nil {
		t.Fatal("the move was allowed with no destination")
	}
	if n, err := Move(context.Background(), &runner{}, DefaultRoot, DefaultRoot); n != 0 || err != nil {
		t.Fatalf("moving the store where it already is: %d %v", n, err)
	}
}
