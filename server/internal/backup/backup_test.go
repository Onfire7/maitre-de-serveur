package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

func TestWriteScanRestore(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	os.MkdirAll(filepath.Join(state, "node"), 0o700)
	os.WriteFile(filepath.Join(state, "node", "identity.key"), []byte("KEY"), 0o600)
	os.WriteFile(filepath.Join(state, "node", "identity.pub"), []byte("PUB"), 0o644)
	st, err := store.Open(filepath.Join(state, "fleet.db"), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Put(ctx, store.KindFleet, "f1", &maitrev1.Fleet{FleetId: "f1", Name: "Home"}, true); err != nil {
		t.Fatal(err)
	}
	drive := t.TempDir()
	if err := Write(ctx, st, state, drive, Meta{NodeID: "node-a", NodeName: "a", FleetID: "f1", FleetName: "Home"}); err != nil {
		t.Fatal(err)
	}
	// Twice: the second replaces the first atomically.
	if err := Write(ctx, st, state, drive, Meta{NodeID: "node-a", NodeName: "a", FleetID: "f1", FleetName: "Home"}); err != nil {
		t.Fatal(err)
	}
	found := ScanDir(drive, "/dev/sdb1", "/dev/sdb")
	if len(found) != 1 || found[0].Meta.NodeName != "a" || found[0].Meta.SavedUnixMs == 0 {
		t.Fatalf("found %+v", found)
	}
	restored := t.TempDir()
	if err := Restore(found[0].Dir, restored); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(restored, "node", "identity.key")); string(b) != "KEY" {
		t.Errorf("key not restored")
	}
	st2, err := store.Open(filepath.Join(restored, "fleet.db"), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	fleets, err := store.ListLive(ctx, st2, store.KindFleet, func() *maitrev1.Fleet { return &maitrev1.Fleet{} })
	if err != nil || len(fleets) != 1 || fleets[0].GetName() != "Home" {
		t.Errorf("fleet not restored: %v %v", fleets, err)
	}
}
