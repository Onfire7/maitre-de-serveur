package pairing_test

import (
	"crypto/ed25519"
	"regexp"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/pairing"
)

func TestCodeIsStableAndOrdered(t *testing.T) {
	a, _, _ := ed25519.GenerateKey(nil)
	b, _, _ := ed25519.GenerateKey(nil)
	c1 := pairing.Code(a, b)
	if c1 != pairing.Code(a, b) {
		t.Fatal("code not deterministic")
	}
	if !regexp.MustCompile(`^\d{3} \d{3}$`).MatchString(c1) {
		t.Fatalf("unexpected format %q", c1)
	}
	if c1 == pairing.Code(b, a) {
		t.Fatal("swapping keys should (almost surely) change the code")
	}
}

// Known answer so the Rust side can check against the same vector.
func TestKnownVector(t *testing.T) {
	installer := make(ed25519.PublicKey, 32)
	device := make(ed25519.PublicKey, 32)
	for i := range device {
		device[i] = byte(i)
	}
	if got := pairing.Code(installer, device); got != "036 565" {
		t.Fatalf("vector changed: %s", got)
	}
}
