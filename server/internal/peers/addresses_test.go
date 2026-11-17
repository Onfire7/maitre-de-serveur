package peers

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
)

// The Relay dials only the addresses the Fleet's own records give a Node
// (ADR 0012), best first; an address mDNS reported is not one of them.
func TestRecordedAddressesLeaveOutDiscoveredOnes(t *testing.T) {
	ctx := context.Background()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "f.db"), id.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Put(ctx, store.KindNode, "b", &maitrev1.Node{NodeId: "b", Name: "Basalt", Addresses: []string{"10.0.0.2:7443", "192.168.1.2:7443"}}, false); err != nil {
		t.Fatal(err)
	}
	m := New(st, id, trust.New(), slog.Default())
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	m.Discovered("b", "192.168.9.9:7443")
	m.Discovered("b", "192.168.1.2:7443")
	if got := m.Addresses()["b"]; !slices.Contains(got, "192.168.9.9:7443") {
		t.Fatalf("every known address: %v", got)
	}
	if got, want := m.RecordedAddresses("b"), []string{"192.168.1.2:7443", "10.0.0.2:7443"}; !slices.Equal(got, want) {
		t.Fatalf("recorded: got %v, want %v", got, want)
	}
	if got := m.RecordedAddresses("nobody"); len(got) != 0 {
		t.Fatalf("an unknown Node: %v", got)
	}
}
