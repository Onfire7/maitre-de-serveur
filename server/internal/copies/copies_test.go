package copies

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

func setup(t *testing.T) (*Manager, *store.Store, map[string]bool) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "a")
	t.Cleanup(func() { st.Close() })
	st.Put(ctx, storage.KindSpace, "photos", &maitrev1.Space{SpaceId: "photos", Name: "Photos", Placement: storage.PlacementMostFree, Copies: 2}, true)
	st.Put(ctx, storage.KindSpace, "scratch", &maitrev1.Space{SpaceId: "scratch", Name: "Scratch", Placement: storage.PlacementMostFree, RequireTags: []string{"ssd"}}, true)
	for _, d := range []*maitrev1.DriveRecord{
		{DriveId: "d1", NodeId: "a", Name: "Aspen", AutoTags: []string{"hdd"}},
		{DriveId: "d2", NodeId: "a", Name: "Atlas", AutoTags: []string{"hdd"}},
		{DriveId: "d3", NodeId: "a", Name: "Alder", AutoTags: []string{"hdd"}}, // same disk as d2
		{DriveId: "p1", NodeId: "a", Name: "Arbor", Role: storage.RoleParity},
		{DriveId: "b1", NodeId: "b", Name: "Birch", AutoTags: []string{"hdd"}},
	} {
		st.Put(ctx, storage.KindDrive, d.DriveId, d, false)
	}
	m := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), "a", nil)
	m.Mounts = t.TempDir()
	m.Remote = t.TempDir()
	mounted := map[string]bool{"d1": true, "d2": true, "d3": true, "p1": true}
	m.Mounted = func(dir string) bool { return mounted[filepath.Base(dir)] }
	m.Free = func(dir string) uint64 { return 10 << 30 }
	m.DiskOf = func(context.Context) map[string]string {
		return map[string]string{"d1": "/dev/sda", "d2": "/dev/sdb", "d3": "/dev/sdb", "p1": "/dev/sdc"}
	}
	for _, d := range []string{"d1", "d2", "d3", "p1"} {
		os.MkdirAll(filepath.Join(m.Mounts, d, "photos"), 0o755)
	}
	return m, st, mounted
}

func write(t *testing.T, p, content string, mtime time.Time) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, mtime, mtime)
}

func TestPass(t *testing.T) {
	ctx := context.Background()
	m, st, _ := setup(t)
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	// One copy of a.jpg on d1; b.jpg on d1 (new) and d2 (stale); c.jpg on
	// d2 and d3 (the same disk, so one copy really).
	write(t, filepath.Join(m.Mounts, "d1", "photos", "2026", "a.jpg"), "aaaa", old)
	write(t, filepath.Join(m.Mounts, "d1", "photos", "b.jpg"), "new-b", newer)
	write(t, filepath.Join(m.Mounts, "d2", "photos", "b.jpg"), "old", old)
	write(t, filepath.Join(m.Mounts, "d2", "photos", "c.jpg"), "cc", old)
	write(t, filepath.Join(m.Mounts, "d3", "photos", "c.jpg"), "cc", old)
	if err := m.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	// a.jpg got a second copy on another disk (d2 or d3, same disk; the
	// roomiest, which the fake makes d2 by order), with its mode and time.
	a2 := filepath.Join(m.Mounts, "d2", "photos", "2026", "a.jpg")
	if _, err := os.Stat(a2); err != nil {
		a2 = filepath.Join(m.Mounts, "d3", "photos", "2026", "a.jpg")
		if _, err := os.Stat(a2); err != nil {
			t.Fatal("a.jpg has one copy")
		}
	}
	if info, _ := os.Stat(a2); info.Mode().Perm() != 0o640 || !info.ModTime().Equal(old) {
		t.Errorf("copy's mode %v time %v", info.Mode(), info.ModTime())
	}
	if b, _ := os.ReadFile(filepath.Join(m.Mounts, "d2", "photos", "b.jpg")); string(b) != "new-b" {
		t.Errorf("stale copy of b.jpg kept: %q", b)
	}
	// c.jpg: both copies on /dev/sdb; a copy on d1 makes a real second.
	if _, err := os.Stat(filepath.Join(m.Mounts, "d1", "photos", "c.jpg")); err != nil {
		t.Error("c.jpg has no copy on another disk")
	}
	// Never into the Parity Drive, never for Spaces with one copy.
	if _, err := os.Stat(filepath.Join(m.Mounts, "p1", "photos", "b.jpg")); err == nil {
		t.Error("copied onto the Parity Drive")
	}
	sts := m.Statuses()
	if len(sts) != 2 || sts[0].SpaceId != "photos" || sts[0].ShortFiles != 0 || sts[0].MaxCopies != 2 || sts[0].LastPassUnixMs == 0 || sts[1].MaxCopies != 0 {
		t.Fatalf("statuses %v", sts)
	}
	// Three copies asked, two disks: every file is short, and said so.
	var photos maitrev1.Space
	st.Get(ctx, storage.KindSpace, "photos", &photos)
	photos.Copies = 3
	st.Put(ctx, storage.KindSpace, "photos", &photos, true)
	m.Pass(ctx)
	if sts := m.Statuses(); sts[0].ShortFiles != 3 {
		t.Fatalf("short %d", sts[0].ShortFiles)
	}
	// Left untouched: no temporary files anywhere.
	filepath.WalkDir(m.Mounts, func(p string, d os.DirEntry, _ error) error {
		if d != nil && len(d.Name()) > 12 && d.Name()[:12] == ".maitre-copy" {
			t.Errorf("temporary file left: %s", p)
		}
		return nil
	})
}

func TestChangedThenFlush(t *testing.T) {
	ctx := context.Background()
	m, _, _ := setup(t)
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	m.Now = func() time.Time { return now }
	p := filepath.Join(m.Mounts, "d1", "photos", "x.jpg")
	write(t, p, "x", now)
	m.Changed(p)
	// Not yet quiet.
	if next := m.flush(ctx); next != Quiet {
		t.Fatalf("next %v", next)
	}
	if _, err := os.Stat(filepath.Join(m.Mounts, "d2", "photos", "x.jpg")); err == nil {
		t.Fatal("copied before it went quiet")
	}
	now = now.Add(Quiet)
	m.flush(ctx)
	if _, err := os.Stat(filepath.Join(m.Mounts, "d2", "photos", "x.jpg")); err != nil {
		t.Fatal("not copied once quiet")
	}
}

func TestDifferentNodes(t *testing.T) {
	ctx := context.Background()
	m, st, mounted := setup(t)
	var photos maitrev1.Space
	st.Get(ctx, storage.KindSpace, "photos", &photos)
	photos.SpanNodes, photos.CopiesOnDifferentNodes = true, true
	st.Put(ctx, storage.KindSpace, "photos", &photos, true)
	os.MkdirAll(filepath.Join(m.Remote, "b", "photos"), 0o755)
	mounted["photos"] = true // the remote mount, by base name in this fake
	write(t, filepath.Join(m.Mounts, "d1", "photos", "a.jpg"), "a", time.Now())
	m.Pass(ctx)
	if _, err := os.Stat(filepath.Join(m.Remote, "b", "photos", "a.jpg")); err != nil {
		t.Fatal("second copy did not go to the other Node")
	}
	if _, err := os.Stat(filepath.Join(m.Mounts, "d2", "photos", "a.jpg")); err == nil {
		t.Fatal("a third copy was made locally")
	}
	if sts := m.Statuses(); sts[0].MaxCopies != 2 {
		t.Fatalf("max copies counts local disks only: %d", sts[0].MaxCopies)
	}
}
