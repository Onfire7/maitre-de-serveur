package nodeconn_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/nodeconn"
)

func TestAMissingStoreIsAFirstRunAndNotAnError(t *testing.T) {
	s, err := nodeconn.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Nodes) != 0 || s.Last != "" {
		t.Errorf("a fresh store is not empty: %+v", s)
	}
	if s.Find("") != nil || s.Find("avalon") != nil {
		t.Error("a fresh store found something")
	}
}

func TestAConnectionIsSavedAndFoundAgainEveryWayItIsNamed(t *testing.T) {
	dir := t.TempDir()
	s, _ := nodeconn.Load(dir)
	s.Remember(nodeconn.Known{
		NodeID: "ae7cb24dca8a8b052874664a518f586c", Name: "Avalon",
		Addresses: []string{"100.74.111.122:7443"}, Identity: "/keys/device", LastSeenUnix: 1762300000,
	}, true)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	again, err := nodeconn.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The whole point: a bare command finds the Node with nothing given.
	k := again.Find("")
	if k == nil || k.Name != "Avalon" {
		t.Fatalf("the last Node was not remembered: %+v", again)
	}
	if k.Identity != "/keys/device" {
		t.Errorf("identity %q", k.Identity)
	}
	// By name, by the start of the id, and by an address a caller typed
	// before — all three mean the same Node, so none of them needs a key.
	for _, q := range []string{"Avalon", "avalon", "ae7cb24d", "100.74.111.122:7443"} {
		if got := again.Find(q); got == nil || got.NodeID != k.NodeID {
			t.Errorf("Find(%q) did not find Avalon", q)
		}
	}
	if again.Find("boulder") != nil {
		t.Error("Find matched a Node that was never saved")
	}
}

func TestTheWayThatAnsweredIsPromotedAndTheOthersKept(t *testing.T) {
	s, _ := nodeconn.Load(t.TempDir())
	id := "ae7cb24d"
	s.Remember(nodeconn.Known{NodeID: id, Name: "Avalon", Addresses: []string{"192.168.4.80:7443"}}, true)
	s.Remember(nodeconn.Known{NodeID: id, Addresses: []string{"100.74.111.122:7443"}}, true)
	k := s.Find("Avalon")
	// Decision 284: the way that worked goes first and the way that did
	// not is demoted rather than forgotten, because a laptop comes home.
	want := []string{"100.74.111.122:7443", "192.168.4.80:7443"}
	if len(k.Addresses) != 2 || k.Addresses[0] != want[0] || k.Addresses[1] != want[1] {
		t.Errorf("addresses %v, want %v", k.Addresses, want)
	}
	// Back on the LAN, the LAN address leads again and the name is kept.
	s.Remember(nodeconn.Known{NodeID: id, Addresses: []string{"192.168.4.80:7443"}}, true)
	k = s.Find("Avalon")
	if k.Addresses[0] != "192.168.4.80:7443" || len(k.Addresses) != 2 || k.Name != "Avalon" {
		t.Errorf("addresses %v name %q", k.Addresses, k.Name)
	}
}

func TestAddressesDoNotGrowWithoutBound(t *testing.T) {
	s, _ := nodeconn.Load(t.TempDir())
	for i := 0; i < 30; i++ {
		s.Remember(nodeconn.Known{NodeID: "n", Addresses: []string{string(rune('a'+i)) + ":7443"}}, true)
	}
	if n := len(s.Find("n").Addresses); n > 8 {
		t.Errorf("%d addresses kept", n)
	}
}

func TestAPeerIsSavedWithoutBecomingTheDefault(t *testing.T) {
	s, _ := nodeconn.Load(t.TempDir())
	s.Remember(nodeconn.Known{NodeID: "ae7cb24d", Name: "Avalon", Addresses: []string{"a:7443"}}, true)
	s.Remember(nodeconn.Known{NodeID: "90b33937", Name: "boulder", Addresses: []string{"b:7443"}}, false)
	if s.Last != "Avalon" {
		t.Errorf("a peer became the default: Last = %q", s.Last)
	}
	// And it is reachable by name, which is what -node boulder needs.
	if k := s.Find("boulder"); k == nil || k.Addresses[0] != "b:7443" {
		t.Errorf("the peer was not saved: %+v", s.Nodes)
	}
}

func TestForgettingTheDefaultLeavesNoDanglingDefault(t *testing.T) {
	s, _ := nodeconn.Load(t.TempDir())
	s.Remember(nodeconn.Known{NodeID: "ae7cb24d", Name: "Avalon", Addresses: []string{"a:7443"}}, true)
	s.Remember(nodeconn.Known{NodeID: "90b33937", Name: "boulder"}, false)
	if !s.Forget("avalon") {
		t.Fatal("Forget said no")
	}
	if s.Last != "" {
		t.Errorf("Last still names a forgotten Node: %q", s.Last)
	}
	if s.Find("Avalon") != nil {
		t.Error("the Node was not forgotten")
	}
	if s.Forget("avalon") {
		t.Error("Forget said yes twice")
	}
	if s.Find("boulder") == nil {
		t.Error("forgetting one dropped another")
	}
}

func TestNothingPrivateIsWritten(t *testing.T) {
	dir := t.TempDir()
	s, _ := nodeconn.Load(dir)
	s.Remember(nodeconn.Known{NodeID: "ae7cb24d", Name: "Avalon", Addresses: []string{"a:7443"}, Identity: "/keys/device"}, true)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, nodeconn.File))
	if err != nil {
		t.Fatal(err)
	}
	// Only the path to the key, never the key: a Node's id is already
	// public, and the private half stays where its owner put it.
	for _, forbidden := range []string{"PRIVATE", "BEGIN "} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("the store holds %q:\n%s", forbidden, raw)
		}
	}
	info, err := os.Stat(filepath.Join(dir, nodeconn.File))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode %o, want 600", perm)
	}
}

func TestACorruptStoreDoesNotLockTheUserOut(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, nodeconn.File), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := nodeconn.Load(dir)
	if err != nil {
		t.Fatalf("a corrupt store is an error: %v", err)
	}
	if len(s.Nodes) != 0 {
		t.Error("a corrupt store yielded Nodes")
	}
	// And it can be written over, so the next connection repairs it.
	s.Remember(nodeconn.Known{NodeID: "n", Name: "n", Addresses: []string{"a:7443"}}, true)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if again, _ := nodeconn.Load(dir); again.Find("") == nil {
		t.Error("the store did not repair itself")
	}
}

func TestAnIDWithNoNameIsStillAddressable(t *testing.T) {
	s, _ := nodeconn.Load(t.TempDir())
	s.Remember(nodeconn.Known{NodeID: "ae7cb24dca8a8b05", Addresses: []string{"a:7443"}}, true)
	k := s.Find("")
	if k == nil {
		t.Fatal("a nameless Node was not remembered")
	}
	if k.Key() != "ae7cb24dca8a" || k.Short() != "ae7cb24dca8a" {
		t.Errorf("Key %q Short %q", k.Key(), k.Short())
	}
	if s.Last != "ae7cb24dca8a" {
		t.Errorf("Last %q", s.Last)
	}
}
