package recsig_test

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/recsig"
)

func seeded(t *testing.T) *identity.Identity {
	t.Helper()
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i)
	}
	id, err := identity.FromPrivateKey(ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// The Console signs the same bytes in Rust (desktop/src-tauri/src/sign.rs),
// tested against these vectors.
func TestVectors(t *testing.T) {
	id := seeded(t)
	for _, c := range []struct {
		rec       *maitrev1.Record
		canonical string
		sig       string
	}{
		{&maitrev1.Record{Kind: "space", Id: "media", Body: []byte{0x0a, 0x05, 'm', 'e', 'd', 'i', 'a'}},
			"6d6169747265207265636f726420763100000000057370616365000000056d65646961000000070a056d65646961000000019a278ad07b",
			"86387e9eb59456809df9926fc263989223ccc3d15e056c3435210f85dec09dbaa2c984342d86f7ddf6db518b313ac12c7821122554525dbb503069383195470a"},
		{&maitrev1.Record{Kind: "grant", Id: "g1", Body: []byte("ignored"), Deleted: true},
			"6d6169747265207265636f726420763100000000056772616e7400000002673100000000010000019a278ad07b",
			"394fa2ee58a263c193f7c57a6243b893ecfdda618d15b83dc4430c797f99cf9f3cf61eb4a07a6b592d1a3b735f07e40c78b494b9b82a05bde5eb718aab150601"},
	} {
		body := c.rec.Body
		if c.rec.Deleted {
			body = nil
		}
		if got := hex.EncodeToString(recsig.Canonical(c.rec.Kind, c.rec.Id, body, c.rec.Deleted, 1761600000123)); got != c.canonical {
			t.Errorf("%s canonical = %s", c.rec.Kind, got)
		}
		if err := recsig.Sign(id, c.rec, 1761600000123); err != nil {
			t.Fatal(err)
		}
		if got := hex.EncodeToString(c.rec.Signature); got != c.sig {
			t.Errorf("%s signature = %s", c.rec.Kind, got)
		}
		if fp, err := recsig.Verify(c.rec); err != nil || fp != id.Fingerprint() {
			t.Errorf("%s verify = %q, %v", c.rec.Kind, fp, err)
		}
	}
}

func TestVerifyRefusesTampering(t *testing.T) {
	id := seeded(t)
	rec := &maitrev1.Record{Kind: "space", Id: "media", Body: []byte("a")}
	if _, err := recsig.Verify(rec); err != recsig.ErrUnsigned {
		t.Fatalf("unsigned: %v", err)
	}
	if err := recsig.Sign(id, rec, 5); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(r *maitrev1.Record){
		"body":      func(r *maitrev1.Record) { r.Body = []byte("b") },
		"id":        func(r *maitrev1.Record) { r.Id = "other" },
		"kind":      func(r *maitrev1.Record) { r.Kind = "node" },
		"deleted":   func(r *maitrev1.Record) { r.Deleted = true },
		"signed at": func(r *maitrev1.Record) { r.SignedUnixMs = 6 },
		"signer": func(r *maitrev1.Record) {
			other, _ := identity.Generate()
			r.SignerKey = other.PublicKey()
		},
	} {
		r := &maitrev1.Record{Kind: rec.Kind, Id: rec.Id, Body: rec.Body, SignerKey: rec.SignerKey, Signature: rec.Signature, SignedUnixMs: rec.SignedUnixMs}
		change(r)
		if _, err := recsig.Verify(r); err == nil {
			t.Errorf("%s changed: verified", name)
		}
	}
}
