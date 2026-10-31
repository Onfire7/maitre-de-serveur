package parity

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/drain"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

const lsblk = `{"blockdevices":[
 {"name":"vda","path":"/dev/vda","model":null,"serial":null,"size":42949672960,"rm":false,"rota":false,"type":"disk","tran":"virtio","fstype":null,"uuid":null,"label":null,"mountpoints":[],
  "children":[{"name":"vda3","path":"/dev/vda3","model":null,"serial":null,"size":30000000000,"rm":false,"rota":false,"type":"part","tran":null,"fstype":"xfs","uuid":"aaaa","label":"root","mountpoints":["/sysroot"]},
              {"name":"vda5","path":"/dev/vda5","model":null,"serial":null,"size":12000000000,"rm":false,"rota":false,"type":"part","tran":null,"fstype":"ext4","uuid":"eeee","label":"maitre-data","mountpoints":[]}]},
 {"name":"sdb","path":"/dev/sdb","model":"WD Red 4TB","serial":"WD-1","size":4000787030016,"rm":false,"rota":true,"type":"disk","tran":"sata","fstype":"btrfs","uuid":"b1","label":"maitre-atlas","mountpoints":[]},
 {"name":"sdc","path":"/dev/sdc","model":"WD Red 4TB","serial":"WD-2","size":4000787030016,"rm":false,"rota":true,"type":"disk","tran":"sata","fstype":"btrfs","uuid":"c1","label":"maitre-aspen","mountpoints":[]},
 {"name":"sdd","path":"/dev/sdd","model":"Seagate 8TB","serial":"SG-1","size":8001563222016,"rm":false,"rota":true,"type":"disk","tran":"sata","fstype":FSTYPE,"uuid":UUID,"label":LABEL,"mountpoints":[]},
 {"name":"sde","path":"/dev/sde","model":"Toshiba 2TB","serial":"TO-1","size":2000000000000,"rm":false,"rota":true,"type":"disk","tran":"sata","fstype":null,"uuid":null,"label":null,"mountpoints":[],
  "children":[{"name":"sde1","path":"/dev/sde1","model":null,"serial":null,"size":1000000000000,"rm":false,"rota":true,"type":"part","tran":null,"fstype":"ext4","uuid":"e1","label":null,"mountpoints":[]},
              {"name":"sde2","path":"/dev/sde2","model":null,"serial":null,"size":1000000000000,"rm":false,"rota":true,"type":"part","tran":null,"fstype":"ext4","uuid":"e2","label":null,"mountpoints":[]}]}
]}`

type fakeRunner struct {
	cmds      []string
	formatted bool
	diff      string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.cmds = append(f.cmds, name+" "+strings.Join(args, " "))
	switch name {
	case "lsblk":
		out := lsblk
		if f.formatted {
			out = strings.NewReplacer("FSTYPE", `"btrfs"`, "UUID", `"d1"`, "LABEL", `"maitre-parity-arbor"`).Replace(out)
		} else {
			out = strings.NewReplacer("FSTYPE", "null", "UUID", "null", "LABEL", "null").Replace(out)
		}
		return out, nil
	case "mkfs.btrfs":
		f.formatted = true
	case "snapraid":
		if len(args) >= 3 && args[2] == "diff" {
			return f.diff, nil
		}
	}
	return "", nil
}

func setup(t *testing.T) (*Manager, *store.Store, *fakeRunner, map[string]bool) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "a")
	t.Cleanup(func() { st.Close() })
	for _, b := range []*maitrev1.Space{
		{SpaceId: "media", Name: "Media", Placement: storage.PlacementMostFree, RequireTags: []string{"hdd"}, ParityLevel: 1},
		{SpaceId: "docs", Name: "Documents", Placement: storage.PlacementMostFree, ParityLevel: 2},
		{SpaceId: "apps", Name: "Apps", Placement: storage.PlacementMostFree, DefaultForNew: true},
		{SpaceId: "vault", Name: "Vault", Placement: storage.PlacementMostFree, Mirrored: true},
	} {
		st.Put(ctx, storage.KindSpace, b.SpaceId, b, true)
	}
	for _, d := range []*maitrev1.DriveRecord{
		{DriveId: "b1", NodeId: "a", Name: "Atlas", AutoTags: []string{"hdd", "medium"}, Tags: []string{"cold"}, AddedUnixMs: 1},
		{DriveId: "c1", NodeId: "a", Name: "Aspen", AutoTags: []string{"hdd", "medium"}, AddedUnixMs: 2},
		{DriveId: "eeee", NodeId: "a", Name: "Apollo", AutoTags: []string{"internal", "ssd", "small"}, Origin: storage.OriginInternal, AddedUnixMs: 0},
		{DriveId: "x1", NodeId: "b", Name: "Birch", AutoTags: []string{"hdd"}},
	} {
		st.Put(ctx, storage.KindDrive, d.DriveId, d, false)
	}
	run := &fakeRunner{diff: "   100 equal\n     3 added\n     1 removed\n     0 updated\n     0 moved\n     0 copied\n     0 restored\n"}
	stor := storage.New(st, run)
	stor.Mounts = t.TempDir()
	dm := drain.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), "a")
	dm.Mounts = stor.Mounts
	m := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), "a", stor, dm)
	m.Mounts = stor.Mounts
	m.ConfigDir = t.TempDir()
	m.StateFile = filepath.Join(t.TempDir(), "parity.json")
	m.Run = run
	m.Nice = false
	mounted := map[string]bool{"b1": true, "c1": true, "eeee": true}
	m.Mounted = func(dir string) bool { return mounted[filepath.Base(dir)] }
	dm.Mounted = m.Mounted
	dm.Free = func(string) uint64 { return 1 << 40 }
	m.Used = func(dir string) uint64 { return map[string]uint64{"b1": 3 << 40, "c1": 1 << 40}[filepath.Base(dir)] }
	m.Now = func() time.Time { return time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC) }
	for _, d := range []string{"b1", "c1", "eeee"} {
		os.MkdirAll(filepath.Join(m.Mounts, d), 0o755)
	}
	return m, st, run, mounted
}

func addParity(t *testing.T, st *store.Store, ids ...string) {
	for i, id := range ids {
		st.Put(context.Background(), storage.KindDrive, id, &maitrev1.DriveRecord{DriveId: id, NodeId: "a", Name: "P" + id, Role: storage.RoleParity, AddedUnixMs: int64(100 + i)}, false)
	}
}

func TestArraysAndConfig(t *testing.T) {
	ctx := context.Background()
	m, st, _, mounted := setup(t)
	if arrays, _ := m.Arrays(ctx); len(arrays) != 0 {
		t.Fatalf("arrays without a Parity Drive: %v", arrays)
	}
	addParity(t, st, "p1")
	mounted["p1"] = true
	arrays, err := m.WriteConfigs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Level 1 holds Media (hdd Drives b1, c1); level 2 holds Documents, on
	// every plain Drive, but the Node has one Parity Drive, so it runs at
	// level 1. Vault is mirrored and Apps is off: neither appears.
	if len(arrays) != 2 || arrays[0].Level != 1 || arrays[1].Level != 2 || arrays[1].LevelGot != 1 {
		t.Fatalf("arrays %+v", arrays)
	}
	ids := func(a Array) string {
		var s []string
		for _, d := range a.Drives {
			s = append(s, d.GetDriveId())
		}
		return strings.Join(s, ",")
	}
	if ids(arrays[0]) != "b1,c1" || ids(arrays[1]) != "b1,c1,eeee" {
		t.Fatalf("drives %s / %s", ids(arrays[0]), ids(arrays[1]))
	}
	cfg, _ := os.ReadFile(m.configPath(1))
	for _, want := range []string{"parity " + filepath.Join(m.Mounts, "p1", ".maitre/snapraid/level1.parity"), "content " + filepath.Join(m.Mounts, "b1", ".maitre/snapraid/level1.content"), "data b1 " + filepath.Join(m.Mounts, "b1") + "/", "include /media/", "exclude /.maitre/"} {
		if !strings.Contains(string(cfg), want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(string(cfg), "include /docs/") || strings.Contains(string(cfg), "2-parity") {
		t.Errorf("level 1 config has level 2 things:\n%s", cfg)
	}
	addParity(t, st, "p1", "p2")
	mounted["p2"] = true
	arrays, _ = m.WriteConfigs(ctx)
	cfg, _ = os.ReadFile(m.configPath(2))
	if arrays[1].LevelGot != 2 || !strings.Contains(string(cfg), "2-parity "+filepath.Join(m.Mounts, "p2", ".maitre/snapraid/level2.2-parity")) {
		t.Errorf("level 2 with two Parity Drives:\n%s", cfg)
	}
	if _, err := os.Stat(filepath.Join(m.Mounts, "b1", ".maitre", "snapraid")); err != nil {
		t.Error("content folder not made on a data Drive")
	}
	st.Delete(ctx, storage.KindDrive, "p2", false)
	m.WriteConfigs(ctx)
	if _, err := os.Stat(m.configPath(2)); err == nil {
		cfg, _ = os.ReadFile(m.configPath(2))
		if strings.Contains(string(cfg), "2-parity") {
			t.Error("stale 2-parity")
		}
	}
	status := m.Status(ctx)
	if len(status.Arrays) != 2 || strings.Join(status.ParityDriveIds, ",") != "p1" || status.NextSyncUnixMs != time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("status %v", status)
	}
}

func TestCheck(t *testing.T) {
	ctx := context.Background()
	m, st, _, _ := setup(t)
	// The blank 8 TB disk: fine, protecting Media and Documents.
	res, err := m.Check(ctx, "", "/dev/sdd", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Blockers) != 0 || strings.Join(res.Protects, ",") != "docs,media" || res.LargestProtectedBytes != 3<<40 {
		t.Fatalf("check %v", res)
	}
	// Atlas holds 3 TB of protected data: a Drive smaller than that cannot
	// be the Parity Drive (227); the partitions share a disk (239).
	st.Put(ctx, storage.KindDrive, "e1", &maitrev1.DriveRecord{DriveId: "e1", NodeId: "a", Name: "Elm", AutoTags: []string{"hdd"}}, false)
	st.Put(ctx, storage.KindDrive, "e2", &maitrev1.DriveRecord{DriveId: "e2", NodeId: "a", Name: "Elder", AutoTags: []string{"hdd"}}, false)
	res, _ = m.Check(ctx, "e1", "", "")
	if len(res.Blockers) != 2 || !strings.Contains(res.Blockers[0], "shares its disk with Elder") || !strings.Contains(res.Blockers[1], "at least as large") {
		t.Fatalf("blockers %v", res.Blockers)
	}
	// Two Parity Drives already (228).
	addParity(t, st, "p1", "p2")
	res, _ = m.Check(ctx, "", "/dev/sdd", "")
	if len(res.Blockers) != 1 || !strings.Contains(res.Blockers[0], "two Parity Drives already") {
		t.Fatalf("blockers %v", res.Blockers)
	}
	// A Drive with files needs a Drain plan; its loose files need a Space (246).
	st.Delete(ctx, storage.KindDrive, "p1", false)
	st.Delete(ctx, storage.KindDrive, "p2", false)
	os.MkdirAll(filepath.Join(m.Mounts, "c1", "Downloads"), 0o755)
	os.WriteFile(filepath.Join(m.Mounts, "c1", "Downloads", "x.iso"), []byte("xx"), 0o644)
	res, _ = m.Check(ctx, "c1", "", "")
	if len(res.Blockers) != 1 || !strings.Contains(res.Blockers[0], "Pick a Space") {
		t.Fatalf("blockers %v", res.Blockers)
	}
	res, _ = m.Check(ctx, "c1", "", "apps")
	if len(res.Blockers) != 0 || res.Plan == nil || len(res.Plan.Moves) != 1 {
		t.Fatalf("check with a Space for loose files: %v", res)
	}
	// The last data Drive (240): with Atlas and Aspen gone, Apollo is alone.
	st.Delete(ctx, storage.KindDrive, "b1", false)
	st.Delete(ctx, storage.KindDrive, "c1", false)
	st.Delete(ctx, storage.KindDrive, "e1", false)
	st.Delete(ctx, storage.KindDrive, "e2", false)
	res, _ = m.Check(ctx, "", "/dev/sdd", "")
	if len(res.Blockers) != 0 {
		t.Fatalf("Apollo is another Drive: %v", res.Blockers)
	}
	st.Delete(ctx, storage.KindDrive, "eeee", false)
	res, _ = m.Check(ctx, "", "/dev/sdd", "")
	if len(res.Blockers) != 1 || !strings.Contains(res.Blockers[0], "no other Drive") {
		t.Fatalf("blockers %v", res.Blockers)
	}
}

func TestMakeParityFromBlankDisk(t *testing.T) {
	ctx := context.Background()
	m, st, run, mounted := setup(t)
	mounted["d1"] = true
	if _, err := m.SetParity(ctx, &maitrev1.SetParityDriveRequest{Device: "/dev/sdd", Name: "Arbor", Parity: true, Confirm: "wrong"}); err == nil || !strings.Contains(err.Error(), "Seagate 8TB") {
		t.Fatalf("wrong confirm: %v", err)
	}
	res, err := m.SetParity(ctx, &maitrev1.SetParityDriveRequest{Device: "/dev/sdd", Name: "Arbor", Parity: true, Confirm: "Seagate 8TB"})
	if err != nil {
		t.Fatal(err)
	}
	rec := res.GetDrive().GetRecord()
	if rec.GetRole() != storage.RoleParity || rec.GetName() != "Arbor" || rec.GetDriveId() != "d1" {
		t.Fatalf("record %v", rec)
	}
	all := strings.Join(run.cmds, "\n")
	if !strings.Contains(all, "mkfs.btrfs -f -L maitre-parity-arbor /dev/sdd") || !strings.Contains(all, "chattr +C "+filepath.Join(m.Mounts, "d1", ".maitre/snapraid")) {
		t.Fatalf("commands:\n%s", all)
	}
	// The first sync ran in the background with the new array.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(run.cmds, "\n"), "snapraid -c "+m.configPath(1)+" sync") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(strings.Join(run.cmds, "\n"), " sync") {
		t.Fatal("no sync after the Parity Drive was made")
	}
	for time.Now().Before(deadline) && m.Status(ctx).GetJob() != "" {
		time.Sleep(10 * time.Millisecond)
	}
	if st := m.Status(ctx); st.GetLastSyncUnixMs() == 0 || st.GetJob() != "" {
		t.Fatalf("status after sync %v", st)
	}
	// The Parity Drive matches no Space.
	spaces, _ := storage.Spaces(ctx, st)
	if got := storage.SpacesOf(rec, spaces); len(got) != 0 {
		t.Fatalf("Parity Drive in Spaces %v", got)
	}
	// Off again: role data, parity files gone, Spaces matched again (245).
	os.MkdirAll(filepath.Join(m.Mounts, "d1", ".maitre", "snapraid"), 0o755)
	os.WriteFile(filepath.Join(m.Mounts, "d1", ".maitre", "snapraid", "level1.parity"), []byte("p"), 0o644)
	if _, err := m.SetParity(ctx, &maitrev1.SetParityDriveRequest{DriveId: "d1", Parity: false, Confirm: "Arbor"}); err != nil {
		t.Fatal(err)
	}
	var after maitrev1.DriveRecord
	st.Get(ctx, storage.KindDrive, "d1", &after)
	if storage.RoleOf(&after) != storage.RoleData {
		t.Fatalf("role %q", after.GetRole())
	}
	if _, err := os.Stat(filepath.Join(m.Mounts, "d1", ".maitre", "snapraid")); err == nil {
		t.Fatal("parity files kept")
	}
	if got := storage.SpacesOf(&after, spaces); strings.Join(got, ",") != "apps,docs,media" {
		t.Fatalf("Spaces after: %v", got)
	}
}

func TestSyncGuards(t *testing.T) {
	ctx := context.Background()
	m, st, run, mounted := setup(t)
	addParity(t, st, "p1")
	mounted["p1"] = true
	os.MkdirAll(filepath.Join(m.Mounts, "p1"), 0o755)
	write := func(p string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}
	write(filepath.Join(m.Mounts, "b1", "media", "a.mkv"))
	write(filepath.Join(m.Mounts, "c1", "media", "b.mkv"))
	write(filepath.Join(m.Mounts, "b1", "docs", "d.txt"))
	// First sync: nothing synced before, so no guard can hold it.
	if err := m.Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	// Decision 231: a missing Drive holds the sync.
	mounted["c1"] = false
	err := m.Sync(ctx, false)
	if err == nil || !strings.Contains(err.Error(), "Aspen is not connected") || m.Status(ctx).GetHeld() == "" || strings.Join(m.Status(ctx).MissingDriveIds, ",") != "c1" {
		t.Fatalf("missing Drive: %v, %v", err, m.Status(ctx))
	}
	mounted["c1"] = true
	// A Drive that came back empty.
	write(filepath.Join(m.Mounts, "c1", ".maitre", "snapraid", "level1.content"))
	os.RemoveAll(filepath.Join(m.Mounts, "c1", "media"))
	os.MkdirAll(filepath.Join(m.Mounts, "c1", "media"), 0o755)
	if err := m.Sync(ctx, false); err == nil || !strings.Contains(err.Error(), "Aspen looks empty") {
		t.Fatalf("empty Drive: %v", err)
	}
	write(filepath.Join(m.Mounts, "c1", "media", "b.mkv"))
	// Too many deletions.
	run.diff = "   100 equal\n     0 added\n    10 removed\n     0 updated\n     0 moved\n     0 copied\n     0 restored\n"
	if err := m.Sync(ctx, false); err == nil || !strings.Contains(err.Error(), "10 of 110 protected files (9%) were deleted") {
		t.Fatalf("deletions: %v", err)
	}
	if m.Status(ctx).GetHeld() == "" {
		t.Fatal("not held")
	}
	// Protect now forces it, and clears the hold.
	before := len(run.cmds)
	if err := m.Sync(ctx, true); err != nil {
		t.Fatal(err)
	}
	if m.Status(ctx).GetHeld() != "" {
		t.Fatal("still held after a forced sync")
	}
	synced := 0
	for _, c := range run.cmds[before:] {
		if strings.HasSuffix(c, " sync") {
			synced++
		}
	}
	if synced != 2 {
		t.Fatalf("expected both arrays synced, got %d in %v", synced, run.cmds[before:])
	}
	// Scrub.
	if err := m.Scrub(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(run.cmds, "\n"), " scrub -p 8 -o 10") || m.Status(ctx).GetLastScrubUnixMs() == 0 {
		t.Fatal("scrub did not run")
	}
	// The state file brings the times back.
	m2 := New(st, m.log, "a", m.stor, nil)
	m2.StateFile = m.StateFile
	m2.load()
	if m2.state.LastSync != m.state.LastSync || m2.state.LastScrub == 0 {
		t.Fatal("state not kept")
	}
}

func TestReplace(t *testing.T) {
	ctx := context.Background()
	m, st, run, mounted := setup(t)
	addParity(t, st, "p1")
	mounted["p1"] = true
	mounted["b1"] = false // Atlas failed
	if _, err := m.Replace(ctx, "c1", "/dev/sdd", "Seagate 8TB"); err == nil || !strings.Contains(err.Error(), "still connected") {
		t.Fatalf("replacing a working Drive: %v", err)
	}
	d, err := m.Replace(ctx, "b1", "/dev/sdd", "Seagate 8TB")
	if err != nil {
		t.Fatal(err)
	}
	rec := d.GetRecord()
	if rec.GetName() != "Atlas" || strings.Join(rec.GetTags(), ",") != "cold" || rec.GetSnapraidName() != "b1" || rec.GetDriveId() != "d1" {
		t.Fatalf("new record %v", rec)
	}
	var old maitrev1.DriveRecord
	st.Get(ctx, storage.KindDrive, "b1", &old)
	if old.GetReplacedBy() != "d1" {
		t.Fatalf("old record %v", &old)
	}
	// The rebuild runs with the failed Drive's SnapRAID name, then a sync.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(strings.Join(run.cmds, "\n"), " fix -d b1") {
		time.Sleep(10 * time.Millisecond)
	}
	mounted["d1"] = true
	all := strings.Join(run.cmds, "\n")
	if !strings.Contains(all, "snapraid -c "+m.configPath(1)+" fix -d b1") {
		t.Fatalf("no rebuild:\n%s", all)
	}
	// The replaced record is out of the arrays; the new one is in under its old name.
	arrays, _ := m.Arrays(ctx)
	for _, a := range arrays {
		for _, dr := range a.Drives {
			if dr.GetDriveId() == "b1" {
				t.Fatal("replaced Drive still in an array")
			}
		}
	}
	cfg, _ := os.ReadFile(m.configPath(1))
	if !strings.Contains(string(cfg), "data b1 "+filepath.Join(m.Mounts, "d1")+"/") {
		t.Fatalf("config:\n%s", cfg)
	}
}

func TestNextSync(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC) // a Saturday
	if got := NextSync(&maitrev1.ProtectionSchedule{}, now); got != time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC) {
		t.Fatalf("default %v", got)
	}
	if got := NextSync(&maitrev1.ProtectionSchedule{SyncTime: "23:30"}, now); got != time.Date(2026, 10, 31, 23, 30, 0, 0, time.UTC) {
		t.Fatalf("today %v", got)
	}
	// Mondays and Thursdays only.
	if got := NextSync(&maitrev1.ProtectionSchedule{SyncTime: "02:00", SyncDays: []uint32{1, 4}}, now); got != time.Date(2026, 11, 2, 2, 0, 0, 0, time.UTC) {
		t.Fatalf("days %v", got)
	}
}
