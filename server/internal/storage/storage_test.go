package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

const lsblkFixture = `{"blockdevices":[
 {"name":"zram0","path":"/dev/zram0","model":null,"serial":null,"size":1,"rm":false,"rota":false,"type":"disk","tran":null,"fstype":"swap","uuid":null,"label":null,"mountpoints":["[SWAP]"]},
 {"name":"vda","path":"/dev/vda","model":null,"serial":null,"size":42949672960,"rm":false,"rota":false,"type":"disk","tran":"virtio","fstype":null,"uuid":null,"label":null,"mountpoints":[],
  "children":[{"name":"vda3","path":"/dev/vda3","model":null,"serial":null,"size":30000000000,"rm":false,"rota":false,"type":"part","tran":null,"fstype":"xfs","uuid":"aaaa","label":"root","mountpoints":["/sysroot"]},
              {"name":"vda4","path":"/dev/vda4","model":null,"serial":null,"size":500000000,"rm":false,"rota":false,"type":"part","tran":null,"fstype":"ext4","uuid":"cccc","label":"maitre-config","mountpoints":["/var/lib/maitre"]}]},
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
	if len(drives) != 4 || !byDev["/dev/vda3"] || !byDev["/dev/vda4"] || byDev["/dev/sdb1"] || byDev["/dev/sdc"] {
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
