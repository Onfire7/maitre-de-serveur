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
