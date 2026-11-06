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

// Measured on the Node image's own podman: a storage.conf.d drop-in is
// ignored, so storage.conf is written whole — and it must be built from
// the vendored copy, or the image's own additionalimagestores and overlay
// options are silently dropped.
func TestTheVendoredSettingsSurvive(t *testing.T) {
	dir := t.TempDir()
	VendoredConfig = filepath.Join(dir, "vendored.conf")
	StorageConfig = filepath.Join(dir, "storage.conf")
	ContainersDropIn = filepath.Join(dir, "containers.conf.d", "50-maitre.conf")
	vendored := "[storage]\ndriver = \"overlay\"\nrunroot = \"/run/containers/storage\"\ngraphroot = \"/var/lib/containers/storage\"\n\n[storage.options]\nadditionalimagestores = [\n\"/usr/lib/containers/storage\",\n]\n\n[storage.options.overlay]\nmountopt = \"nodev,metacopy=on\"\n"
	if err := os.WriteFile(VendoredConfig, []byte(vendored), 0o644); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dir, "drive", Dir)
	run := &runner{}
	changed, err := Ensure(context.Background(), run, store)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	b, err := os.ReadFile(StorageConfig)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		"graphroot = \"" + store + "\"",
		"driver = \"overlay\"",
		"\"/usr/lib/containers/storage\"",
		"mountopt = \"nodev,metacopy=on\"",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("storage.conf lost %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "graphroot = \"/var/lib/containers/storage\"") {
		t.Fatalf("the old graphroot is still there:\n%s", got)
	}
	// The blob staging directory travels with the store: staging on a root
	// the store has left is what ran out of space in the first place.
	b, err = os.ReadFile(ContainersDropIn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "image_copy_tmp_dir = \""+filepath.Join(store, "tmp")+"\"") {
		t.Fatalf("drop-in: %s", b)
	}
	if Configured() != store {
		t.Fatalf("Configured() = %q", Configured())
	}
	// A graphroot outside podman's own place needs SELinux labelling, and
	// nobody can run the commands by hand: a Node has no terminal.
	var labelled, relabelled bool
	for _, c := range run.cmds {
		labelled = labelled || strings.HasPrefix(c, "semanage fcontext -a -e "+DefaultRoot)
		relabelled = relabelled || strings.HasPrefix(c, "restorecon -R "+store)
	}
	if !labelled || !relabelled {
		t.Fatalf("labelling: %v", run.cmds)
	}
	// Idempotent: a second call changes nothing and relabels nothing.
	run.cmds = nil
	if changed, err := Ensure(context.Background(), run, store); err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if len(run.cmds) != 0 {
		t.Fatalf("ran commands with nothing to do: %v", run.cmds)
	}
}

// With no vendored copy to build on, the written file still names a driver,
// which podman would otherwise have to guess.
func TestWithoutAVendoredConfig(t *testing.T) {
	dir := t.TempDir()
	VendoredConfig = filepath.Join(dir, "absent.conf")
	StorageConfig = filepath.Join(dir, "storage.conf")
	ContainersDropIn = filepath.Join(dir, "containers.conf.d", "50-maitre.conf")
	store := filepath.Join(dir, "drive", Dir)
	if _, err := Ensure(context.Background(), &runner{}, store); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(StorageConfig)
	if !strings.Contains(string(b), "driver = \"overlay\"") || !strings.Contains(string(b), store) {
		t.Fatalf("%s", b)
	}
}

// moveRunner scripts the three commands Move leans on: what the store
// holds before and after, whether semanage is there, and rsync.
type moveRunner struct {
	cmds       []string
	before     string // `podman images` while the old store is configured
	after      string // and once the new one is
	switched   bool   // Ensure has pointed podman at the new store
	noSemanage bool   // the image has no policycoreutils-python-utils
}

func (r *moveRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	r.cmds = append(r.cmds, name+" "+strings.Join(args, " "))
	switch {
	case name == "semanage" && r.noSemanage:
		return "", errString("executable file not found in $PATH")
	case name == "podman" && len(args) > 0 && args[0] == "images":
		if r.switched {
			return r.after, nil
		}
		return r.before, nil
	case name == "du":
		return "1024\t/x\n", nil
	case name == "restorecon":
		// Ensure has rewritten storage.conf by the time it labels, so
		// podman reads the new store from here on.
		r.switched = true
	}
	return "", nil
}

type errString string

func (e errString) Error() string { return string(e) }

func (r *moveRunner) ran(prefix string) bool {
	for _, c := range r.cmds {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// The old store is never deleted on the strength of an unchecked copy.
// `cp -a` silently lost images — two became one with its tag reduced to
// <none> — and the old store was removed on top of that, which on a Node
// with a 23 GiB store would have left every Service unable to start and
// nothing to recover from.
func TestMoveKeepsTheOldStoreWhenAnImageDoesNotSurvive(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	from, to := filepath.Join(dir, "old"), filepath.Join(dir, "new")
	if err := os.MkdirAll(from, 0o700); err != nil {
		t.Fatal(err)
	}
	// Two images before, one after: the copy is not a replacement.
	r := &moveRunner{before: "aaa\nbbb\n", after: "aaa\n"}
	t.Cleanup(func() { r.switched = false })
	// Ensure switches podman over; the fake follows it.
	oldStorage, oldDropIn, oldVendored := StorageConfig, ContainersDropIn, VendoredConfig
	StorageConfig = filepath.Join(dir, "storage.conf")
	ContainersDropIn = filepath.Join(dir, "containers.conf")
	VendoredConfig = filepath.Join(dir, "vendored.conf")
	defer func() { StorageConfig, ContainersDropIn, VendoredConfig = oldStorage, oldDropIn, oldVendored }()
	if err := os.WriteFile(VendoredConfig, []byte("[storage]\ndriver = \"overlay\"\ngraphroot = \""+DefaultRoot+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := move(ctx, r, from, to)
	if err == nil {
		t.Fatal("Move reported success although an image did not survive the copy")
	}
	if !strings.Contains(err.Error(), "bbb") {
		t.Errorf("the error does not name the lost image: %v", err)
	}
	if _, statErr := os.Stat(from); statErr != nil {
		t.Errorf("the old store was removed even though the copy was short: %v", statErr)
	}
}

// A labelling that did not happen has to be loud: semanage lives in
// policycoreutils-python-utils, and while it was missing the store was
// relabelled with whatever context its new home carried, so every
// container on it died with "RELRO protection failed".
func TestLabellingFailsLoudlyWithoutSemanage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	oldStorage, oldDropIn, oldVendored := StorageConfig, ContainersDropIn, VendoredConfig
	StorageConfig = filepath.Join(dir, "storage.conf")
	ContainersDropIn = filepath.Join(dir, "containers.conf")
	VendoredConfig = filepath.Join(dir, "vendored.conf")
	defer func() { StorageConfig, ContainersDropIn, VendoredConfig = oldStorage, oldDropIn, oldVendored }()
	if err := os.WriteFile(VendoredConfig, []byte("[storage]\ndriver = \"overlay\"\ngraphroot = \""+DefaultRoot+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &moveRunner{noSemanage: true}

	_, err := Ensure(ctx, r, filepath.Join(dir, "store"))
	if err == nil {
		t.Fatal("Ensure reported success although the store could not be labelled")
	}
	if !strings.Contains(err.Error(), "policycoreutils-python-utils") {
		t.Errorf("the error does not say where semanage comes from: %v", err)
	}
}

// The policy equates /var/mnt with /mnt, and semanage refuses a file spec
// under the left side of an equivalency it already has. Drives live under
// /var/mnt, so the rule has to be written the other way round or no store
// off the root partition can be labelled at all.
func TestFcontextSpecFollowsThePolicysEquivalencies(t *testing.T) {
	for in, want := range map[string]string{
		"/var/mnt/drives/abc/.maitre/imagestore": "/mnt/drives/abc/.maitre/imagestore",
		"/var/mnt":                               "/mnt",
		"/var/home/someone/store":                "/home/someone/store",
		"/var/roothome/store":                    "/root/store",
		"/var/lib/containers/storage":            "/var/lib/containers/storage",
		"/var/mnttypo/store":                     "/var/mnttypo/store",
	} {
		if got := fcontextSpec(in); got != want {
			t.Errorf("fcontextSpec(%q) = %q, want %q", in, got, want)
		}
	}
}

// libpod's database must not travel with the store: it records the static
// dir it was made with and podman will not open it anywhere else.
func TestMoveLeavesLibpodBehind(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	from, to := filepath.Join(dir, "old"), filepath.Join(dir, "new")
	if err := os.MkdirAll(filepath.Join(from, "libpod"), 0o700); err != nil {
		t.Fatal(err)
	}
	oldStorage, oldDropIn, oldVendored := StorageConfig, ContainersDropIn, VendoredConfig
	StorageConfig = filepath.Join(dir, "storage.conf")
	ContainersDropIn = filepath.Join(dir, "containers.conf")
	VendoredConfig = filepath.Join(dir, "vendored.conf")
	defer func() { StorageConfig, ContainersDropIn, VendoredConfig = oldStorage, oldDropIn, oldVendored }()
	if err := os.WriteFile(VendoredConfig, []byte("[storage]\ngraphroot = \""+DefaultRoot+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &moveRunner{before: "aaa\n", after: "aaa\n"}

	if _, err := move(ctx, r, from, to); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if !r.ran("rsync -aHAX --delete --exclude /libpod/") {
		t.Errorf("libpod was not excluded from the copy; ran %v", r.cmds)
	}
}

// The drop-in says nothing about libpod's static dir, in either
// direction. Pinning it is what made a moved store openable by podman,
// but this file is global and bootc reads it too: pinned, bootc and
// podman shared one libpod database, each wrote its own graphroot into it
// and the other then refused to open it, which stopped `bootc upgrade` on
// Boulder even with the store in its default place.
func TestTheDropInLeavesLibpodsStaticDirAlone(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	oldStorage, oldDropIn, oldVendored := StorageConfig, ContainersDropIn, VendoredConfig
	StorageConfig = filepath.Join(dir, "storage.conf")
	ContainersDropIn = filepath.Join(dir, "containers.conf")
	VendoredConfig = filepath.Join(dir, "vendored.conf")
	defer func() { StorageConfig, ContainersDropIn, VendoredConfig = oldStorage, oldDropIn, oldVendored }()
	if err := os.WriteFile(VendoredConfig, []byte("[storage]\ngraphroot = \""+DefaultRoot+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dir, "mnt", "drives", "abc", ".maitre", "imagestore")

	if _, err := Ensure(ctx, &runner{}, store); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(ContainersDropIn)
	if err != nil {
		t.Fatal(err)
	}
	// The settings only: the comment explains static_dir at length.
	var settings []string
	for _, line := range strings.Split(string(b), "\n") {
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
			settings = append(settings, t)
		}
	}
	for _, line := range settings {
		if strings.HasPrefix(line, "static_dir") {
			t.Errorf("the drop-in sets static_dir, which bootc inherits: %q", line)
		}
	}
	// The setting it does carry is the one the drop-in is for.
	if want := "image_copy_tmp_dir = \"" + filepath.Join(store, "tmp") + "\""; !strings.Contains(string(b), want) {
		t.Errorf("the drop-in does not set %s:\n%s", want, b)
	}
}

// Moving the store off root is refused, because bootc reads the same
// containers.conf and will not open a libpod database whose graph root is
// not its own: Boulder could not stage an update until the store came
// back. Moving it back is the way out and stays allowed.
func TestMoveOffRootIsRefusedWhileItBreaksUpdates(t *testing.T) {
	ctx := context.Background()
	_, err := Move(ctx, &moveRunner{}, DefaultRoot, "/var/mnt/drives/abc/.maitre/imagestore")
	if err == nil {
		t.Fatal("the move was allowed")
	}
	for _, want := range []string{"stop this Node updating", DefaultRoot} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}
