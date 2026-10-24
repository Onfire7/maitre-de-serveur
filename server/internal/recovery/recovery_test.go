package recovery_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/recovery"
)

func TestRoundTrip(t *testing.T) {
	p, err := recovery.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 12 {
		t.Fatalf("got %d words", len(p))
	}
	ent, err := recovery.Parse("  " + strings.ToUpper(p.String()) + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if recovery.FromEntropy(ent).String() != p.String() {
		t.Fatal("phrase did not round trip")
	}
	k1 := recovery.Key(ent)
	k2 := recovery.Key(ent)
	if !bytes.Equal(k1.Public().(ed25519.PublicKey), k2.Public().(ed25519.PublicKey)) {
		t.Fatal("derivation not deterministic")
	}
}

// BIP39 test vector: zero entropy.
func TestBIP39Vector(t *testing.T) {
	ent, _ := hex.DecodeString("00000000000000000000000000000000")
	want := "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	if got := recovery.FromEntropy(ent).String(); got != want {
		t.Fatalf("got %q", got)
	}
	ent2, _ := hex.DecodeString("7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f")
	want2 := "legal winner thank year wave sausage worth useful legal winner thank yellow"
	if got := recovery.FromEntropy(ent2).String(); got != want2 {
		t.Fatalf("got %q", got)
	}
}

func TestChecksumCatchesWrongWord(t *testing.T) {
	bad := "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon"
	if _, err := recovery.Parse(bad); err == nil {
		t.Fatal("expected checksum failure")
	}
	if _, err := recovery.Parse("not a phrase"); err == nil {
		t.Fatal("expected length failure")
	}
}
