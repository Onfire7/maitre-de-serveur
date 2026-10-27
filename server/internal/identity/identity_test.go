package identity_test

import (
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
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

// The NFS certificate never changes for a key, and is checked against the
// Node it claims to be (decision 187).
func TestNFSCertificate(t *testing.T) {
	a, _ := identity.Generate()
	b, _ := identity.Generate()
	c1, err := a.NFSCertificatePEM()
	if err != nil {
		t.Fatal(err)
	}
	c2, _ := a.NFSCertificatePEM()
	if string(c1) != string(c2) {
		t.Fatal("NFS certificate changed between calls")
	}
	if err := identity.CheckNFSCertificate(c1, a.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if identity.CheckNFSCertificate(c1, b.Fingerprint()) == nil {
		t.Fatal("accepted another Node's certificate")
	}
	if identity.CheckNFSCertificate([]byte("junk"), a.Fingerprint()) == nil {
		t.Fatal("accepted junk")
	}
	if n := identity.NFSName(a.Fingerprint()); len(n) != 39 || n[:32] != a.Fingerprint()[:32] {
		t.Fatalf("NFSName: %q", n)
	}
}
