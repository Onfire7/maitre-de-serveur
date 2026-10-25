// Package trust is the Fleet trust list: the set of public keys allowed to
// talk to this Node. There is no certificate authority; a peer is trusted if
// and only if its key fingerprint is in the list.
//
// In dev mode the list is loaded from a directory of *.pub PEM files. On a
// real Node it will come from the Fleet database.
package trust

import (
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
)

// List is a concurrency-safe set of trusted fingerprints.
type List struct {
	mu      sync.RWMutex
	keys    map[string]struct{}
	limited map[string]struct{} // recovery-only keys: may add a Trusted Device, nothing else
}

// New returns an empty list.
func New() *List { return &List{keys: map[string]struct{}{}, limited: map[string]struct{}{}} }

// ReplaceWithLimited swaps the whole list: full keys and recovery-only keys.
func (l *List) ReplaceWithLimited(full, limited []string) {
	nf := make(map[string]struct{}, len(full)+len(limited))
	nl := make(map[string]struct{}, len(limited))
	for _, fp := range full {
		nf[fp] = struct{}{}
	}
	for _, fp := range limited {
		nf[fp] = struct{}{}
		nl[fp] = struct{}{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keys, l.limited = nf, nl
}

// Limited reports whether fingerprint is trusted for adding a Trusted
// Device only (the Recovery Phrase, decision 100).
func (l *List) Limited(fingerprint string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.limited[fingerprint]
	return ok
}

// Add trusts a fingerprint.
func (l *List) Add(fingerprint string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keys[fingerprint] = struct{}{}
}

// Replace swaps the whole list atomically. Used when the Fleet database
// changes so connections already open are unaffected and new ones see the
// current set.
func (l *List) Replace(fingerprints []string) {
	next := make(map[string]struct{}, len(fingerprints))
	for _, fp := range fingerprints {
		next[fp] = struct{}{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keys = next
	l.limited = map[string]struct{}{}
}

// Fingerprints returns the trusted fingerprints in no particular order.
func (l *List) Fingerprints() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]string, 0, len(l.keys))
	for fp := range l.keys {
		out = append(out, fp)
	}
	return out
}

// Trusted reports whether fingerprint is in the list.
func (l *List) Trusted(fingerprint string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.keys[fingerprint]
	return ok
}

// Len is the number of trusted keys.
func (l *List) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.keys)
}

// VerifyPeer is a tls.Config.VerifyPeerCertificate function. It ignores
// chains entirely and accepts the connection only if the leaf certificate's
// Ed25519 key is trusted.
func (l *List) VerifyPeer(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	if len(rawCerts) == 0 {
		return fmt.Errorf("trust: peer presented no certificate")
	}
	cert, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return fmt.Errorf("trust: parse peer certificate: %w", err)
	}
	fp, err := identity.FingerprintOfCertificate(cert)
	if err != nil {
		return fmt.Errorf("trust: %w", err)
	}
	if !l.Trusted(fp) {
		return fmt.Errorf("trust: peer %s is not in the trust list", fp)
	}
	return nil
}

// LoadDir adds every *.pub file in dir to the list. A missing dir is not an
// error: it simply yields an empty list.
func (l *List) LoadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".pub" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		pub, err := identity.ParsePublicKeyPEM(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
		l.Add(identity.Fingerprint(pub))
	}
	return nil
}
