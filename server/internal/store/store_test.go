package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

func open(t *testing.T, node string) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "fleet.db"), node)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPutGetListAndVersions(t *testing.T) {
	ctx := context.Background()
	s := open(t, "node-a")
	r1, err := s.Put(ctx, store.KindFleet, "f1", &maitrev1.Fleet{FleetId: "f1", Name: "Home"}, true)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := s.Put(ctx, store.KindFleet, "f1", &maitrev1.Fleet{FleetId: "f1", Name: "Home 2"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Version != 1 || r2.Version != 2 || r2.OwnerNodeId != "node-a" {
		t.Fatalf("versions %d %d owner %s", r1.Version, r2.Version, r2.OwnerNodeId)
	}
	var f maitrev1.Fleet
	if _, err := s.Get(ctx, store.KindFleet, "f1", &f); err != nil || f.Name != "Home 2" {
		t.Fatalf("get: %v %q", err, f.Name)
	}
	if _, err := s.Put(ctx, store.KindNode, "node-a", &maitrev1.Node{NodeId: "node-a"}, false); err != nil {
		t.Fatal(err)
	}
	all, _ := s.List(ctx, "")
	nodes, _ := s.List(ctx, store.KindNode)
	if len(all) != 2 || len(nodes) != 1 {
		t.Fatalf("list sizes %d %d", len(all), len(nodes))
	}
	if _, err := s.Delete(ctx, store.KindNode, "node-a", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, store.KindNode, "node-a", nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted record readable: %v", err)
	}
	live, _ := store.ListLive(ctx, s, "", func() *maitrev1.Fleet { return &maitrev1.Fleet{} })
	if len(live) != 1 {
		t.Fatalf("live %d", len(live))
	}
}

func TestOwnershipAndApply(t *testing.T) {
	ctx := context.Background()
	a := open(t, "node-a")
	b := open(t, "node-b")

	// node-a owns its own Node record; node-b receives a copy and may not edit it.
	rec, _ := a.Put(ctx, store.KindNode, "node-a", &maitrev1.Node{NodeId: "node-a", Name: "a"}, false)
	if ok, err := b.Apply(ctx, rec); err != nil || !ok {
		t.Fatalf("apply: %v %v", ok, err)
	}
	if _, err := b.Put(ctx, store.KindNode, "node-a", &maitrev1.Node{Name: "hijack"}, false); !errors.Is(err, store.ErrNotOwner) {
		t.Fatalf("expected ErrNotOwner, got %v", err)
	}
	// Re-applying the same version is a no-op; a newer one wins.
	if ok, _ := b.Apply(ctx, rec); ok {
		t.Fatal("same version applied twice")
	}
	rec2, _ := a.Put(ctx, store.KindNode, "node-a", &maitrev1.Node{NodeId: "node-a", Name: "a2"}, false)
	if ok, _ := b.Apply(ctx, rec2); !ok {
		t.Fatal("newer version not applied")
	}
	var n maitrev1.Node
	b.Get(ctx, store.KindNode, "node-a", &n)
	if n.Name != "a2" {
		t.Fatalf("got %q", n.Name)
	}

	// Fleet-wide records: either Node may write, newest timestamp wins on apply.
	fa, _ := a.Put(ctx, store.KindFleet, "f", &maitrev1.Fleet{Name: "from a"}, true)
	if ok, _ := b.Apply(ctx, fa); !ok {
		t.Fatal("fleet from a not applied")
	}
	fb, _ := b.Put(ctx, store.KindFleet, "f", &maitrev1.Fleet{Name: "from b"}, true)
	if fb.OwnerNodeId != "node-b" {
		t.Fatal("b did not take ownership")
	}
	if ok, _ := a.Apply(ctx, fb); !ok {
		t.Fatal("newer fleet-wide record from other owner not applied")
	}
	if ok, _ := b.Apply(ctx, fa); ok {
		t.Fatal("older record from other owner should not win")
	}
}

// A tombstone crosses the wire with no body (protobuf leaves it nil); the
// receiving Node must still apply it.
func TestApplyTombstoneFromTheWire(t *testing.T) {
	ctx := context.Background()
	a, b := open(t, "node-a"), open(t, "node-b")
	rec, err := a.Put(ctx, store.KindNode, "node-a", &maitrev1.Node{Name: "Alder"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := b.Apply(ctx, rec); err != nil || !ok {
		t.Fatalf("apply live: %v %v", ok, err)
	}
	del, err := a.Delete(ctx, store.KindNode, "node-a", false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := proto.Marshal(del)
	wire := &maitrev1.Record{}
	if err := proto.Unmarshal(raw, wire); err != nil || wire.Body != nil {
		t.Fatalf("expected a nil body off the wire, got %v (%v)", wire.Body, err)
	}
	if ok, err := b.Apply(ctx, wire); err != nil || !ok {
		t.Fatalf("apply tombstone: %v %v", ok, err)
	}
	if _, err := b.Get(ctx, store.KindNode, "node-a", nil); err == nil {
		t.Fatal("the record should be gone on b")
	}
}

// A Node removed and set up again from the same Installer boot comes back
// past its tombstone, with a version every Node applies (decision 272).
func TestIntroduceRevivesARemovedNode(t *testing.T) {
	ctx := context.Background()
	s := open(t, "sponsor")
	n := &maitrev1.Node{NodeId: "n2", Name: "Avalon", PublicKeyPem: "pem"}
	if added, err := s.Introduce(ctx, n); err != nil || !added {
		t.Fatalf("first: %v %v", added, err)
	}
	if added, err := s.Introduce(ctx, n); err != nil || added {
		t.Fatalf("a live Node is left alone: %v %v", added, err)
	}
	if _, err := s.Tombstone(ctx, store.KindNode, "n2", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, store.KindNode, "n2", nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("not removed: %v", err)
	}
	added, err := s.Introduce(ctx, &maitrev1.Node{NodeId: "n2", Name: "Avalon", PublicKeyPem: "pem2"})
	if err != nil || !added {
		t.Fatalf("revive: %v %v", added, err)
	}
	rec, err := s.Get(ctx, store.KindNode, "n2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.GetVersion() != 2 || rec.GetOwnerNodeId() != "n2" || rec.GetDeleted() {
		t.Errorf("revived record: %+v", rec)
	}
}
