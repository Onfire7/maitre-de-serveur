package storage

import (
	"context"
	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

const lsblkFixture = `{"blockdevices":[
 {"name":"zram0","path":"/dev/zram0","model":null,"serial":null,"size":1,"rm":false,"rota":false,"type":"disk","tran":null,"fstype":"swap","uuid":null,"label":null,"mountpoints":["[SWAP]"]},
 {"name":"vda","path":"/dev/vda","model":null,"serial":null,"size":42949672960,"rm":false,"rota":false,"type":"disk","tran":"virtio","fstype":null,"uuid":null,"label":null,"mountpoints":[],
  "children":[{"name":"vda3","path":"/dev/vda3","model":null,"serial":null,"size":30000000000,"rm":false,"rota":false,"type":"part","tran":null,"fstype":"xfs","uuid":"aaaa","label":"root","mountpoints":["/sysroot"]},
              {"name":"vda4","path":"/dev/vda4","model":null,"serial":null,"size":500000000,"rm":false,"rota":false,"type":"part","tran":null,"fstype":"ext4","uuid":"cccc","label":"maitre-config","mountpoints":["/var/lib/maitre"]},
              {"name":"vda5","path":"/dev/vda5","model":null,"serial":null,"size":12000000000,"rm":false,"rota":false,"type":"part","tran":null,"fstype":"ext4","uuid":"eeee","label":"maitre-data","mountpoints":[]}]},
 {"name":"sdb","path":"/dev/sdb","model":"WD Red 4TB","serial":"WD-1","size":4000787030016,"rm":false,"rota":true,"type":"disk","tran":"sata","fstype":null,"uuid":null,"label":null,"mountpoints":[],
  "children":[{"name":"sdb1","path":"/dev/sdb1","model":null,"serial":null,"size":4000000000000,"rm":false,"rota":true,"type":"part","tran":null,"fstype":"ntfs","uuid":"BBBB-1","label":"Media","mountpoints":[]}]},
 {"name":"sdc","path":"/dev/sdc","model":"Kingston DataTraveler","serial":"KT-2","size":64000000000,"rm":true,"rota":false,"type":"disk","tran":"usb","fstype":null,"uuid":null,"label":null,"mountpoints":[]}
]}`

type fakeRunner struct {
	cmds      []string
	formatted bool
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.cmds = append(f.cmds, name+" "+strings.Join(args, " "))
	switch name {
	case "lsblk":
		if f.formatted {
			return strings.Replace(lsblkFixture, `"tran":"usb","fstype":null,"uuid":null,"label":null`, `"tran":"usb","fstype":"btrfs","uuid":"dddd","label":"maitre-stick"`, 1), nil
		}
		return lsblkFixture, nil
	case "mkfs.btrfs":
		f.formatted = true
	}
	return "", nil
}

func TestListAdoptFormatForget(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	defer st.Close()
	run := &fakeRunner{}
	m := New(st, run)
	m.Mounts = t.TempDir()

	drives, err := m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byDev := map[string]bool{}
	for _, d := range drives {
		byDev[d.GetDevice()] = d.GetSystem()
	}
	if len(drives) != 5 || !byDev["/dev/vda3"] || !byDev["/dev/vda4"] || byDev["/dev/vda5"] || byDev["/dev/sdb1"] || byDev["/dev/sdc"] {
		t.Fatalf("drives: %v", byDev)
	}
	var sdb1 = drives[0]
	if sdb1.GetDevice() != "/dev/sdb1" || sdb1.GetModel() != "WD Red 4TB" || strings.Join(sdb1.GetAutoTags(), ",") != "hdd,medium" {
		t.Fatalf("sdb1: %v", sdb1)
	}

	// Adopt keeps the NTFS filesystem and mounts it through ntfs3 by UUID.
	d, err := m.Adopt(ctx, "/dev/sdb1", "", []string{"media"})
	if err != nil {
		t.Fatal(err)
	}
	if !d.GetAdopted() || d.GetRecord().GetName() != "Media" || d.GetRecord().GetOrigin() != "adopted" {
		t.Fatalf("adopted: %v", d)
	}
	if !strings.Contains(strings.Join(run.cmds, "\n"), "mount -t ntfs3 -o noatime UUID=BBBB-1 ") {
		t.Errorf("mount call missing: %v", run.cmds)
	}
	if _, err := m.Adopt(ctx, "/dev/vda4", "", nil); err == nil {
		t.Error("system partition adopted")
	}
	if _, err := m.Adopt(ctx, "/dev/sdc", "", nil); err == nil {
		t.Error("blank disk adopted without a filesystem")
	}

	// Format needs the exact model repeated.
	if _, err := m.Format(ctx, "/dev/sdc", "stick", nil, "kingston"); err == nil || !strings.Contains(err.Error(), "repeat exactly: Kingston DataTraveler") {
		t.Fatalf("guard: %v", err)
	}
	d, err = m.Format(ctx, "/dev/sdc", "stick", []string{"scratch"}, "Kingston DataTraveler")
	if err != nil {
		t.Fatal(err)
	}
	if d.GetDriveId() != "dddd" || d.GetRecord().GetOrigin() != "formatted" || !strings.Contains(strings.Join(run.cmds, "\n"), "mkfs.btrfs -f -L maitre-stick /dev/sdc") {
		t.Fatalf("format: %v\n%v", d, run.cmds)
	}

	// Forget needs the Drive's name.
	if err := m.Forget(ctx, "dddd", "nope"); err == nil {
		t.Error("forget without the name")
	}
	if err := m.Forget(ctx, "dddd", "stick"); err != nil {
		t.Fatal(err)
	}
	drives, _ = m.List(ctx)
	for _, x := range drives {
		if x.GetDevice() == "/dev/sdc" && x.GetAdopted() {
			t.Error("still adopted after forget")
		}
	}
}

// The Installer's data partition is listed as usable storage, becomes a
// Drive by itself, and cannot be removed.
func TestInternalDrive(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	defer st.Close()
	m := New(st, &fakeRunner{})
	m.Mounts = t.TempDir()
	wrote, err := m.AdoptInternal(ctx)
	if err != nil || !wrote {
		t.Fatalf("adopt internal: wrote=%v err=%v", wrote, err)
	}
	if wrote, err := m.AdoptInternal(ctx); err != nil || wrote {
		t.Fatalf("second adopt: wrote=%v err=%v", wrote, err)
	}
	drives, _ := m.List(ctx)
	var data *maitrev1.Drive
	for _, d := range drives {
		if d.GetDevice() == "/dev/vda5" {
			data = d
		}
	}
	if data == nil || data.GetSystem() || !data.GetAdopted() || data.GetRecord().GetOrigin() != OriginInternal || data.GetRecord().GetName() == "" {
		t.Fatalf("data partition: %v", data)
	}
	if strings.Join(data.GetAutoTags(), ",") != "internal,ssd,small" {
		t.Fatalf("tags %v", data.GetAutoTags())
	}
	if err := m.Forget(ctx, "eeee", data.GetRecord().GetName()); err == nil {
		t.Fatal("built-in drive was forgotten")
	}
}

func TestMirroredDrive(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	defer st.Close()
	run := &mirrorRunner{}
	m := New(st, run)
	m.Mounts = t.TempDir()
	if _, err := m.MakeMirrored(ctx, []string{"/dev/sdb", "/dev/sdb"}, "Bluff", nil, "WD Red 4TB"); err == nil {
		t.Fatal("one disk twice")
	}
	if _, err := m.MakeMirrored(ctx, []string{"/dev/sde1", "/dev/sdc"}, "Bluff", nil, "Old disk"); err == nil || !strings.Contains(err.Error(), "partition") {
		t.Fatalf("a partition: %v", err)
	}
	d, err := m.MakeMirrored(ctx, []string{"/dev/sdb", "/dev/sdc"}, "Bluff", []string{"fast"}, "WD Red 4TB")
	if err != nil {
		t.Fatal(err)
	}
	rec := d.GetRecord()
	if rec.GetRole() != RoleMirrored || strings.Join(rec.GetDisks(), ",") != "WD-1,WD-2" || rec.GetDriveId() != "mmmm" {
		t.Fatalf("record %v", rec)
	}
	if !strings.Contains(strings.Join(run.cmds, "\n"), "mkfs.btrfs -f -L maitre-bluff -d raid1 -m raid1 /dev/sdb /dev/sdc") {
		t.Fatalf("commands %v", run.cmds)
	}
	// The two disks list as one Drive.
	drives, _ := m.List(ctx)
	n := 0
	for _, x := range drives {
		if x.GetDriveId() == "mmmm" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("mirrored Drive listed %d times", n)
	}
	// Health: a missing disk is named by its serial; errors are summed.
	mountedDirs = map[string]bool{filepath.Join(m.Mounts, "mmmm"): true}
	defer func() { mountedDirs = nil }()
	run.missing = true
	sts := m.MirrorStatuses(ctx)
	if len(sts) != 1 || sts[0].GetMissingDisk() != "WD-2" || sts[0].GetErrors() != 3 || sts[0].GetReplacing() != -1 {
		t.Fatalf("statuses %v", sts)
	}
	// Replace the missing disk: btrfs replace with the missing devid, and
	// the record learns the new serial.
	if _, err := m.ReplaceMirrorDisk(ctx, "mmmm", "/dev/sdd", "Seagate 8TB"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(run.cmds, "\n"), "btrfs replace start -f 2 /dev/sdd "+filepath.Join(m.Mounts, "mmmm")) {
		t.Fatalf("commands %v", run.cmds)
	}
	var after maitrev1.DriveRecord
	st.Get(ctx, KindDrive, "mmmm", &after)
	if strings.Join(after.GetDisks(), ",") != "WD-1,SG-1" {
		t.Fatalf("disks after replace %v", after.GetDisks())
	}
}

type mirrorRunner struct {
	cmds      []string
	formatted bool
	missing   bool
}

const mirrorLsblk = `{"blockdevices":[
 {"name":"sdb","path":"/dev/sdb","model":"WD Red 4TB","serial":"WD-1","size":4000787030016,"rm":false,"rota":true,"type":"disk","tran":"sata","fstype":FS,"uuid":ID,"label":LB,"mountpoints":[]},
 {"name":"sde","path":"/dev/sde","model":"Old disk","serial":"OLD-1","size":4000787030016,"rm":false,"rota":true,"type":"disk","tran":"sata","fstype":null,"uuid":null,"label":null,"mountpoints":[],
  "children":[{"name":"sde1","path":"/dev/sde1","model":null,"serial":null,"size":4000000000000,"rm":false,"rota":true,"type":"part","tran":null,"fstype":"ntfs","uuid":"BBBB-1","label":"Media","mountpoints":[]}]},
 {"name":"sdc","path":"/dev/sdc","model":"WD Red 4TB","serial":"WD-2","size":4000787030016,"rm":false,"rota":true,"type":"disk","tran":"sata","fstype":FS,"uuid":ID,"label":LB,"mountpoints":[]},
 {"name":"sdd","path":"/dev/sdd","model":"Seagate 8TB","serial":"SG-1","size":8001563222016,"rm":false,"rota":true,"type":"disk","tran":"sata","fstype":null,"uuid":null,"label":null,"mountpoints":[]}
]}`

func (f *mirrorRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.cmds = append(f.cmds, name+" "+strings.Join(args, " "))
	switch name {
	case "lsblk":
		if f.formatted {
			return strings.NewReplacer("FS", `"btrfs"`, "ID", `"mmmm"`, "LB", `"maitre-bluff"`).Replace(mirrorLsblk), nil
		}
		return strings.NewReplacer("FS", "null", "ID", "null", "LB", "null").Replace(mirrorLsblk), nil
	case "mkfs.btrfs":
		f.formatted = true
	case "btrfs":
		switch strings.Join(args[:2], " ") {
		case "filesystem show":
			if f.missing {
				return "Label: 'maitre-bluff'  uuid: mmmm\n\tTotal devices 2 FS bytes used 1.00GiB\n\tdevid    1 size 3.64TiB used 1.00GiB path /dev/sdb\n\tdevid    2 size 0 used 0 path /dev/sdc MISSING\n*** Some devices missing\n", nil
			}
			return "Label: 'maitre-bluff'  uuid: mmmm\n\tdevid    1 size 3.64TiB used 1.00GiB path /dev/sdb\n\tdevid    2 size 3.64TiB used 1.00GiB path /dev/sdc\n", nil
		case "device stats":
			return "[/dev/sdb].write_io_errs    0\n[/dev/sdb].read_io_errs     2\n[/dev/sdb].flush_io_errs    0\n[/dev/sdb].corruption_errs  1\n[/dev/sdb].generation_errs  0\n", nil
		case "replace status":
			return "Never started\n", nil
		}
	}
	return "", nil
}
