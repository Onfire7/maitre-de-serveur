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

func TestSpacesOf(t *testing.T) {
	spaces := []*maitrev1.Space{
		{SpaceId: "parity", RequireTags: []string{"parity"}, Exclusive: true, Priority: 50},
		{SpaceId: "scratch", RequireTags: []string{"ssd"}, Exclusive: true, Priority: 20},
		{SpaceId: "fast", RequireTags: []string{"ssd"}, Exclusive: true, Priority: 10},
		{SpaceId: "media", RequireTags: []string{"hdd"}, SpanNodes: true},
		{SpaceId: "general", DefaultForNew: true},
		{SpaceId: "b-only", NodeIds: []string{"b"}},
	}
	cases := []struct {
		d    *maitrev1.DriveRecord
		want string
	}{
		// An hdd on a: every non-exclusive Space whose rule matches.
		{&maitrev1.DriveRecord{NodeId: "a", AutoTags: []string{"hdd", "large"}}, "general,media"},
		// On b, the Node-scoped Space joins too.
		{&maitrev1.DriveRecord{NodeId: "b", AutoTags: []string{"hdd"}}, "b-only,general,media"},
		// An ssd: the highest-priority exclusive Space alone.
		{&maitrev1.DriveRecord{NodeId: "a", AutoTags: []string{"ssd", "nvme"}}, "scratch"},
		// A parity tag outranks the rest.
		{&maitrev1.DriveRecord{NodeId: "a", AutoTags: []string{"ssd"}, Tags: []string{"parity"}}, "parity"},
	}
	for _, c := range cases {
		if got := strings.Join(SpacesOf(c.d, spaces), ","); got != c.want {
			t.Errorf("SpacesOf(%v) = %q, want %q", c.d, got, c.want)
		}
	}
	if DefaultSpace(spaces).GetSpaceId() != "general" {
		t.Fatal("default space")
	}
}

func TestValidateSpace(t *testing.T) {
	for _, ok := range []*maitrev1.Space{
		{SpaceId: "media", Name: "Media", Placement: PlacementMostFree},
		{SpaceId: "media", Name: "Media", Placement: PlacementLeastFreePercent, PreferLocal: true, Color: "#3a7bd5"},
	} {
		if err := ValidateSpace(ok); err != nil {
			t.Fatal(err)
		}
	}
	bad := []*maitrev1.Space{
		{SpaceId: "media", Name: "Media", Placement: "fastest"},
		{SpaceId: "Media!", Name: "Media", Placement: PlacementMostFree},
		{SpaceId: "all", Name: "All", Placement: PlacementLocalFirst, Exclusive: true},
		{Name: "x", Placement: PlacementLocalFirst},
		{SpaceId: "media", Name: "Media", Placement: PlacementMostFree, Color: "red; background: url(x)"},
	}
	for _, b := range bad {
		if ValidateSpace(b) == nil {
			t.Errorf("accepted %v", b)
		}
	}
}

// Old Tier records become Spaces once, and a Drive's root files move into the
// default Space's folder; a Drive adopted with its own files is left alone.
func TestMigrateTiersAndLayout(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	defer st.Close()
	m := New(st, &fakeRunner{})
	m.Mounts = t.TempDir()
	// Old records share field numbers with Space.
	for _, tier := range []*maitrev1.Space{
		{SpaceId: "default", Name: "Default", Placement: "local", Priority: -1000},
		{SpaceId: "media", Name: "Media", Placement: "spanning", RequireTags: []string{"hdd"}, Priority: 1},
	} {
		if _, err := st.Put(ctx, "tier", tier.GetSpaceId(), tier, true); err != nil {
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
	spaces, _ := Spaces(ctx, st)
	if len(spaces) != 2 || DefaultSpace(spaces).GetSpaceId() != "default" {
		t.Fatalf("spaces after migration: %v", spaces)
	}
	for _, b := range spaces {
		if b.GetSpaceId() == "media" && (!b.GetSpanNodes() || b.GetPlacement() != PlacementLocalFirst) {
			t.Fatalf("media space: %v", b)
		}
	}
	if again, _ := m.MigrateTiers(ctx); again {
		t.Fatal("migration ran twice")
	}
	if err := m.MoveRootFilesIntoSpace(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.Mounts, "f1", "default", "jellyfin", "config", "x")); err != nil {
		t.Fatal("formatted Drive's files did not move into default/")
	}
	if _, err := os.Stat(filepath.Join(m.Mounts, "a1", "jellyfin", "config", "x")); err != nil {
		t.Fatal("adopted Drive's own files were moved")
	}
	// Branches and folders.
	bySpace, err := m.LocalBranches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(bySpace["default"], ","); got != filepath.Join(m.Mounts, "a1", "default")+","+filepath.Join(m.Mounts, "f1", "default") {
		t.Fatalf("default branches %q", got)
	}
	if got := strings.Join(bySpace["media"], ","); got != filepath.Join(m.Mounts, "a1", "media") {
		t.Fatalf("media branches %q", got)
	}
	MarkSpaceFolders(bySpace["media"])
	folders := Folders(filepath.Join(m.Mounts, "a1"), spaces, []string{"default", "media"})
	kinds := map[string]string{}
	for _, f := range folders {
		kinds[f.GetName()] = f.GetKind()
	}
	if kinds["default"] != "space" || kinds["media"] != "space" || kinds["jellyfin"] != "unsorted" {
		t.Fatalf("folders %v", kinds)
	}
	// A removed Space's folder is recognised by its marker.
	folders = Folders(filepath.Join(m.Mounts, "a1"), []*maitrev1.Space{spaces[0]}, []string{"default"})
	for _, f := range folders {
		if f.GetName() == "media" && f.GetKind() != "orphan" {
			t.Fatalf("removed space folder: %v", f)
		}
	}
}
