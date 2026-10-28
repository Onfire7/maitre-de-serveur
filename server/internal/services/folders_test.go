package services_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
	"github.com/Onfire7/maitre-de-serveur/server/internal/owner"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Jellyfin's media volume points at Media/Movies, with Media/Shows as an
// extra read-only folder: both mount shared (:z), the container joins the
// Media Space's group, its own volumes stay private (:Z), and removing it
// with its data leaves the picked folders alone.
func TestSetFolders(t *testing.T) {
	ctx := context.Background()
	idx, err := catalog.LoadDir("../../../catalog/entries")
	if err != nil {
		t.Fatal(err)
	}
	if v := idx.Find("jellyfin").Volumes; !v[2].Shared {
		t.Fatal("jellyfin's media volume should be marked shared")
	}
	oc, oa := owner.Chown, owner.ACL
	owner.Chown = func(string, int, int) error { return nil }
	owner.ACL = func(...string) error { return nil }
	defer func() { owner.Chown, owner.ACL = oc, oa }()

	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	defer st.Close()
	dir := t.TempDir()
	run := &fakeRunner{state: "active"}
	pools := filepath.Join(dir, "pools")
	for _, sp := range []string{"general", "media"} {
		os.MkdirAll(filepath.Join(pools, sp), 0o755)
	}
	os.MkdirAll(filepath.Join(pools, "media", "Shows"), 0o755)
	owners := map[string]uint32{"media": 61001}
	m := services.New(services.Config{UnitsDir: filepath.Join(dir, "units"), PoolsDir: pools, DefaultSpace: func(context.Context) string { return "general" }, HostPortBase: 20000, SpaceOwner: func(_ context.Context, id string) uint32 { return owners[id] }}, st, run)
	if _, err := m.Install(ctx, catalog.SpecFor(idx.Find("jellyfin"), "", nil, "")); err != nil {
		t.Fatal(err)
	}
	run.cmds = nil

	for _, bad := range []*maitrev1.SetFoldersRequest{
		{Name: "jellyfin", VolumeFolders: map[string]string{"nope": "x"}},
		{Name: "jellyfin", VolumeFolders: map[string]string{"media": "../../etc"}, VolumeSpaces: map[string]string{"media": "media"}},
		{Name: "jellyfin", Mounts: []*maitrev1.ServiceMount{{SpaceId: "media", Folder: "Shows", Path: "/config"}}},
		{Name: "jellyfin", Mounts: []*maitrev1.ServiceMount{{SpaceId: "media", Folder: "Shows", Path: "relative"}}},
		{Name: "jellyfin", VolumeSpaces: map[string]string{"config": "media"}},
	} {
		if _, err := m.SetFolders(ctx, bad); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}

	_, err = m.SetFolders(ctx, &maitrev1.SetFoldersRequest{
		Name:          "jellyfin",
		VolumeFolders: map[string]string{"media": "Movies"},
		VolumeSpaces:  map[string]string{"media": "media"},
		Mounts:        []*maitrev1.ServiceMount{{SpaceId: "media", Folder: "Shows", Path: "/media/shows", ReadOnly: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pools, "media", "Movies")); err != nil {
		t.Fatal("the picked folder was not made")
	}
	unit, _ := os.ReadFile(filepath.Join(dir, "units", "jellyfin.container"))
	u := string(unit)
	for _, want := range []string{
		"Volume=" + filepath.Join(pools, "media", "Movies") + ":/media:z\n",
		"Volume=" + filepath.Join(pools, "media", "Shows") + ":/media/shows:z,ro\n",
		"Volume=" + filepath.Join(pools, "general", ".Services", "jellyfin", "config") + ":/config:Z\n",
		"GroupAdd=61001\n",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("unit lacks %q:\n%s", want, u)
		}
	}
	if !strings.Contains(strings.Join(run.cmds, "\n"), "systemctl try-restart --no-block jellyfin.service") {
		t.Fatalf("not restarted: %v", run.cmds)
	}

	if _, err := m.Control(ctx, &maitrev1.ControlServiceRequest{Name: "jellyfin", Action: maitrev1.ControlServiceRequest_ACTION_REMOVE, DeleteData: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pools, "media", "Movies")); err != nil {
		t.Fatal("removing the Service deleted a picked folder")
	}
	if _, err := os.Stat(filepath.Join(pools, "general", ".Services", "jellyfin", "config")); !os.IsNotExist(err) {
		t.Fatal("its own volume was not deleted")
	}
}
