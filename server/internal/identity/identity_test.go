package identity_test

import (
	"testing"

	"github.com/maitre-de-serveur/maitre/server/internal/identity"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	a, err := identity.LoadOrGenerate(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := identity.LoadOrGenerate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatalf("second load produced a different identity: %s vs %s", a.Fingerprint(), b.Fingerprint())
	}
	pub, err := identity.ParsePublicKeyPEM(a.PublicKeyPEM())
	if err != nil {
		t.Fatal(err)
	}
	if identity.Fingerprint(pub) != a.Fingerprint() {
		t.Fatal("public key PEM does not fingerprint to the identity")
	}
}
