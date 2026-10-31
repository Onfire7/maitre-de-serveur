package installer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
)

const lsblkSample = `{"blockdevices":[
 {"name":"sda","path":"/dev/sda","model":"SanDisk Ultra","size":32000000000,"rm":true,"type":"disk","tran":"usb","mountpoints":[null],"fstype":null,"label":null,"partlabel":null,
  "children":[{"name":"sda1","path":"/dev/sda1","model":null,"size":1500000000,"rm":true,"type":"part","tran":null,"mountpoints":["/run/media/iso"],"fstype":"vfat","label":"MAITRELIVE","partlabel":null}]},
 {"name":"nvme0n1","path":"/dev/nvme0n1","model":"WD Blue SN580","size":1000204886016,"rm":false,"type":"disk","tran":"nvme","mountpoints":[null],"fstype":null,"label":null,"partlabel":null,
  "children":[
   {"name":"nvme0n1p1","path":"/dev/nvme0n1p1","model":null,"size":104857600,"rm":false,"type":"part","tran":null,"mountpoints":[null],"fstype":"vfat","label":null,"partlabel":"EFI system partition"},
   {"name":"nvme0n1p2","path":"/dev/nvme0n1p2","model":null,"size":16777216,"rm":false,"type":"part","tran":null,"mountpoints":[null],"fstype":null,"label":null,"partlabel":"Microsoft reserved partition"},
   {"name":"nvme0n1p3","path":"/dev/nvme0n1p3","model":null,"size":999000000000,"rm":false,"type":"part","tran":null,"mountpoints":[null],"fstype":"LVM2_member","label":null,"partlabel":null,
    "children":[{"name":"vg-home","path":"/dev/mapper/vg-home","model":null,"size":900000000000,"rm":false,"type":"lvm","tran":null,"mountpoints":[null],"fstype":"xfs","label":"home","partlabel":null}]}]},
 {"name":"sdb","path":"/dev/sdb","model":null,"size":4000000000000,"rm":false,"type":"disk","tran":"sata","mountpoints":[null],"fstype":"btrfs","label":"pool","partlabel":null},
 {"name":"sdc","path":"/dev/sdc","model":"Blank","size":2000000000000,"rm":false,"type":"disk","tran":"sata","mountpoints":[null],"fstype":null,"label":null,"partlabel":null}
]}`

func TestParseDisksPartitions(t *testing.T) {
	disks, err := parseDisks([]byte(lsblkSample))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, d := range disks {
		var parts []string
		for _, p := range d.GetPartitions() {
			parts = append(parts, fmt.Sprintf("%s %s %q %v", p.GetDevice(), p.GetFstype(), p.GetLabel(), p.GetBrowsable()))
		}
		got[d.GetPath()] = strings.Join(parts, "; ")
	}
	want := map[string]string{
		// The installer media is listed but never opened.
		"/dev/sda": `/dev/sda1 vfat "MAITRELIVE" false`,
		// GPT names stand in for missing labels; LVM volumes are listed
		// after the partition holding them.
		"/dev/nvme0n1": `/dev/nvme0n1p1 vfat "EFI system partition" true; /dev/nvme0n1p2  "Microsoft reserved partition" false; /dev/nvme0n1p3 LVM2_member "" false; /dev/mapper/vg-home xfs "home" true`,
		// A filesystem on the whole disk.
		"/dev/sdb": `/dev/sdb btrfs "pool" true`,
		"/dev/sdc": ``,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s:\n got  %s\n want %s", k, got[k], v)
		}
	}
}

func TestListDirStaysInside(t *testing.T) {
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "Photos", "2025"), 0o755))
	must(os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello"), 0o644))
	must(os.WriteFile(filepath.Join(root, "B.txt"), nil, 0o644))
	must(os.Symlink("/etc", filepath.Join(root, "etc-link")))

	r, err := listDir(root, "/")
	must(err)
	var names []string
	for _, e := range r.GetEntries() {
		names = append(names, e.GetName())
	}
	if got := strings.Join(names, ","); got != "Photos,B.txt,etc-link,notes.txt" {
		t.Errorf("order: %s", got)
	}
	for _, e := range r.GetEntries() {
		if e.GetName() == "notes.txt" && e.GetSizeBytes() != 5 {
			t.Errorf("size %d", e.GetSizeBytes())
		}
		if e.GetName() == "etc-link" && (!e.GetLink() || e.GetDir()) {
			t.Errorf("link not marked: %+v", e)
		}
	}
	if r.GetTotalBytes() == 0 {
		t.Error("no filesystem size")
	}
	// ".." cannot climb out, and a link out of the partition is refused.
	r, err = listDir(root, "../../..")
	must(err)
	if len(r.GetEntries()) != 4 {
		t.Errorf("../../.. left the root: %v", r.GetEntries())
	}
	if _, err := listDir(root, "etc-link"); err == nil {
		t.Error("followed a link out of the partition")
	}
	r, err = listDir(root, "Photos")
	must(err)
	if len(r.GetEntries()) != 1 || r.GetEntries()[0].GetName() != "2025" {
		t.Errorf("Photos: %v", r.GetEntries())
	}
}

func TestListDirCapsEntries(t *testing.T) {
	root := t.TempDir()
	for i := range maxEntries + 5 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%04d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := listDir(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.GetEntries()) != maxEntries || r.GetMore() != 5 {
		t.Errorf("%d entries, %d more", len(r.GetEntries()), r.GetMore())
	}
}

func TestBrowserMountsOnceAndUnmounts(t *testing.T) {
	var mounts, unmounts []string
	b := &Browser{Root: t.TempDir(), Idle: time.Hour,
		Mount: func(_ context.Context, device, fstype, dir string) error {
			mounts = append(mounts, device+" "+fstype)
			return os.WriteFile(filepath.Join(dir, "hello"), nil, 0o644)
		},
		Unmount: func(dir string) error { unmounts = append(unmounts, filepath.Base(dir)); return nil },
	}
	p := &maitrev1.Partition{Device: "/dev/sdb1", Fstype: "ext4", Browsable: true}
	for range 2 {
		r, err := b.List(context.Background(), p, "/")
		if err != nil {
			t.Fatal(err)
		}
		if len(r.GetEntries()) != 1 {
			t.Fatalf("entries %v", r.GetEntries())
		}
	}
	if len(mounts) != 1 || mounts[0] != "/dev/sdb1 ext4" {
		t.Errorf("mounts %v", mounts)
	}
	if _, err := b.List(context.Background(), &maitrev1.Partition{Device: "/dev/sdb2", Fstype: "crypto_LUKS"}, "/"); err == nil {
		t.Error("opened a partition marked not browsable")
	}
	b.Close()
	if len(unmounts) != 1 || unmounts[0] != "sdb1" {
		t.Errorf("unmounts %v", unmounts)
	}

	// Left alone, a mount goes away by itself.
	b.Idle = 10 * time.Millisecond
	done := make(chan struct{})
	b.Unmount = func(string) error { close(done); return nil }
	if _, err := b.List(context.Background(), p, "/"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("idle mount never unmounted")
	}
}

type countingFolders struct {
	FakeFolders
	closed int
}

func (c *countingFolders) Close() { c.closed++ }

func TestFolderRefusals(t *testing.T) {
	id, _ := identity.Generate()
	folders := &countingFolders{}
	in := New(id, Options{Runner: &FakeRunner{Dir: t.TempDir()}, Disks: FakeDisks, Folders: folders})
	ctx := context.Background()
	if r, err := in.Folder(ctx, "/dev/fake-hdd1", "Films"); err != nil || len(r.GetEntries()) != 2 {
		t.Fatalf("Films: %v %v", r, err)
	}
	if _, err := in.Folder(ctx, "/dev/fake-usb1", "/"); err == nil || !strings.Contains(err.Error(), "installer media") {
		t.Errorf("installer media: %v", err)
	}
	if _, err := in.Folder(ctx, "/etc", "/"); err == nil || !strings.Contains(err.Error(), "no partition") {
		t.Errorf("arbitrary path: %v", err)
	}
	in.mu.Lock()
	in.prov = &maitrev1.Provisioning{Phase: maitrev1.Provisioning_PHASE_INSTALLING}
	in.mu.Unlock()
	if _, err := in.Folder(ctx, "/dev/fake-hdd1", "/"); err == nil || !strings.Contains(err.Error(), "installing") {
		t.Errorf("during install: %v", err)
	}
}

// The backup scan mounts partitions; none may be mounted while bootc wipes
// the disk (decision 268).
func TestBackupScanWaitsOutTheInstall(t *testing.T) {
	id, _ := identity.Generate()
	scans := 0
	in := New(id, Options{Runner: &FakeRunner{Dir: t.TempDir()}, Disks: FakeDisks, Backups: func(context.Context) []*maitrev1.Backup {
		scans++
		return []*maitrev1.Backup{{NodeId: "n1"}}
	}})
	ctx := context.Background()
	if b := in.backups(ctx); len(b) != 1 || scans != 1 {
		t.Fatalf("first scan: %v %d", b, scans)
	}
	in.mu.Lock()
	in.backupScanned = time.Time{}
	in.prov = &maitrev1.Provisioning{Phase: maitrev1.Provisioning_PHASE_INSTALLING}
	in.mu.Unlock()
	if b := in.backups(ctx); len(b) != 1 || scans != 1 {
		t.Errorf("scanned during the install: %v %d", b, scans)
	}
}
