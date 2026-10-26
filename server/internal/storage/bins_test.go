package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

func TestBinsOf(t *testing.T) {
	bins := []*maitrev1.Bin{
		{BinId: "parity", RequireTags: []string{"parity"}, Exclusive: true, Priority: 50},
		{BinId: "scratch", RequireTags: []string{"ssd"}, Exclusive: true, Priority: 20},
		{BinId: "fast", RequireTags: []string{"ssd"}, Exclusive: true, Priority: 10},
		{BinId: "media", RequireTags: []string{"hdd"}, SpanNodes: true},
		{BinId: "general", DefaultForNew: true},
		{BinId: "b-only", NodeIds: []string{"b"}},
	}
	cases := []struct {
		d    *maitrev1.DriveRecord
		want string
	}{
		// An hdd on a: every non-exclusive Bin whose rule matches.
		{&maitrev1.DriveRecord{NodeId: "a", AutoTags: []string{"hdd", "large"}}, "general,media"},
		// On b, the Node-scoped Bin joins too.
		{&maitrev1.DriveRecord{NodeId: "b", AutoTags: []string{"hdd"}}, "b-only,general,media"},
		// An ssd: the highest-priority exclusive Bin alone.
		{&maitrev1.DriveRecord{NodeId: "a", AutoTags: []string{"ssd", "nvme"}}, "scratch"},
		// A parity tag outranks the rest.
		{&maitrev1.DriveRecord{NodeId: "a", AutoTags: []string{"ssd"}, Tags: []string{"parity"}}, "parity"},
	}
	for _, c := range cases {
		if got := strings.Join(BinsOf(c.d, bins), ","); got != c.want {
			t.Errorf("BinsOf(%v) = %q, want %q", c.d, got, c.want)
		}
	}
	if DefaultBin(bins).GetBinId() != "general" {
		t.Fatal("default bin")
	}
}

func TestValidateBin(t *testing.T) {
	ok := &maitrev1.Bin{BinId: "media", Name: "Media", Placement: PlacementMostFree}
	if err := ValidateBin(ok); err != nil {
		t.Fatal(err)
	}
	bad := []*maitrev1.Bin{
		{BinId: "media", Name: "Media", Placement: "fastest"},
		{BinId: "Media!", Name: "Media", Placement: PlacementMostFree},
		{BinId: "all", Name: "All", Placement: PlacementLocalFirst, Exclusive: true},
		{Name: "x", Placement: PlacementLocalFirst},
	}
	for _, b := range bad {
		if ValidateBin(b) == nil {
			t.Errorf("accepted %v", b)
		}
	}
}

// Old Tier records become Bins once, and a Drive's root files move into the
// default Bin's folder; a Drive adopted with its own files is left alone.
func TestMigrateTiersAndLayout(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	defer st.Close()
	m := New(st, &fakeRunner{})
	m.Mounts = t.TempDir()
	// Old records share field numbers with Bin.
	for _, tier := range []*maitrev1.Bin{
		{BinId: "default", Name: "Default", Placement: "local", Priority: -1000},
		{BinId: "media", Name: "Media", Placement: "spanning", RequireTags: []string{"hdd"}, Priority: 1},
	} {
		if _, err := st.Put(ctx, "tier", tier.GetBinId(), tier, true); err != nil {
			t.Fatal(err)
		}
	}
	// Two Drives of this Node: one formatted by Maître with a Service's
	// files at its root (old layout), one adopted with the user's files.
	for _, d := range []*maitrev1.DriveRecord{
		{DriveId: "f1", NodeId: "node-a", Name: "Apollo", Origin: "formatted", AutoTags: []string{"ssd"}},
		{DriveId: "a1", NodeId: "node-a", Name: "Atlas", Origin: "adopted", AutoTags: []string{"hdd"}},
	} {
		if _, err := st.Put(ctx, KindDrive, d.GetDriveId(), d, false); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(m.Mounts, d.GetDriveId())
		os.MkdirAll(filepath.Join(dir, "jellyfin", "config"), 0o755)
		os.WriteFile(filepath.Join(dir, "jellyfin", "config", "x"), []byte("1"), 0o644)
	}
	mountedDirs = map[string]bool{filepath.Join(m.Mounts, "f1"): true, filepath.Join(m.Mounts, "a1"): true}
	defer func() { mountedDirs = nil }()

	converted, err := m.MigrateTiers(ctx)
	if err != nil || !converted {
		t.Fatalf("migrate: %v %v", converted, err)
	}
	bins, _ := Bins(ctx, st)
	if len(bins) != 2 || DefaultBin(bins).GetBinId() != "default" {
		t.Fatalf("bins after migration: %v", bins)
	}
	for _, b := range bins {
		if b.GetBinId() == "media" && (!b.GetSpanNodes() || b.GetPlacement() != PlacementLocalFirst) {
			t.Fatalf("media bin: %v", b)
		}
	}
	if again, _ := m.MigrateTiers(ctx); again {
		t.Fatal("migration ran twice")
	}
	if err := m.MoveRootFilesIntoBin(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.Mounts, "f1", "default", "jellyfin", "config", "x")); err != nil {
		t.Fatal("formatted Drive's files did not move into default/")
	}
	if _, err := os.Stat(filepath.Join(m.Mounts, "a1", "jellyfin", "config", "x")); err != nil {
		t.Fatal("adopted Drive's own files were moved")
	}
	// Branches and folders.
	byBin, err := m.LocalBranches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(byBin["default"], ","); got != filepath.Join(m.Mounts, "a1", "default")+","+filepath.Join(m.Mounts, "f1", "default") {
		t.Fatalf("default branches %q", got)
	}
	if got := strings.Join(byBin["media"], ","); got != filepath.Join(m.Mounts, "a1", "media") {
		t.Fatalf("media branches %q", got)
	}
	MarkBinFolders(byBin["media"])
	folders := Folders(filepath.Join(m.Mounts, "a1"), bins, []string{"default", "media"})
	kinds := map[string]string{}
	for _, f := range folders {
		kinds[f.GetName()] = f.GetKind()
	}
	if kinds["default"] != "bin" || kinds["media"] != "bin" || kinds["jellyfin"] != "unsorted" {
		t.Fatalf("folders %v", kinds)
	}
	// A removed Bin's folder is recognised by its marker.
	folders = Folders(filepath.Join(m.Mounts, "a1"), []*maitrev1.Bin{bins[0]}, []string{"default"})
	for _, f := range folders {
		if f.GetName() == "media" && f.GetKind() != "orphan" {
			t.Fatalf("removed bin folder: %v", f)
		}
	}
}
