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

// The NFS certificate never changes for a key, is P-256 (the kernel's
// keyring cannot parse Ed25519), and is accepted only with the signature of
// the Node it names (decision 188).
func TestNFSCredentials(t *testing.T) {
	a, _ := identity.Generate()
	b, _ := identity.Generate()
	c1, sig, key, err := a.NFSCredentials()
	if err != nil {
		t.Fatal(err)
	}
	c2, sig2, key2, _ := a.NFSCredentials()
	if string(c1) != string(c2) || string(sig) != string(sig2) || string(key) != string(key2) {
		t.Fatal("NFS credentials changed between calls")
	}
	if err := identity.CheckNFSCertificate(c1, sig, a.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if identity.CheckNFSCertificate(c1, sig, b.PublicKey()) == nil {
		t.Fatal("accepted a certificate under another Node's key")
	}
	cb, sigb, _, _ := b.NFSCredentials()
	if identity.CheckNFSCertificate(cb, sig, a.PublicKey()) == nil || identity.CheckNFSCertificate(c1, sigb, a.PublicKey()) == nil {
		t.Fatal("accepted a mismatched signature")
	}
	if identity.CheckNFSCertificate([]byte("junk"), sig, a.PublicKey()) == nil {
		t.Fatal("accepted junk")
	}
	if n := identity.NFSName(a.Fingerprint()); len(n) != 39 || n[:32] != a.Fingerprint()[:32] {
		t.Fatalf("NFSName: %q", n)
	}
}
