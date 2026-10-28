// Package recsig signs and checks Fleet database Records (decision 189).
// A signature covers what the signer decided: the kind, id, body, whether
// it is a tombstone, and when it was signed. The storing Node's owner,
// version and timestamp are left out, because the Node that stores a
// Device's change assigns them. Which keys may sign which kinds is
// package authz.
//
// The Console signs the same bytes in Rust (desktop/src-tauri/src/sign.rs).
package recsig

import (
	"crypto"
	"crypto/ed25519"
	"encoding/binary"
	"errors"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
)

const domain = "maitre record v1\x00"

// Canonical is what a Record signature covers: a domain string, then kind,
// id and body each with a 4-byte big-endian length, a byte for deleted,
// and signed_unix_ms as 8 bytes big-endian.
func Canonical(kind, id string, body []byte, deleted bool, signedUnixMs int64) []byte {
	out := make([]byte, 0, len(domain)+12+len(kind)+len(id)+len(body)+9)
	out = append(out, domain...)
	for _, f := range [][]byte{[]byte(kind), []byte(id), body} {
		out = binary.BigEndian.AppendUint32(out, uint32(len(f)))
		out = append(out, f...)
	}
	if deleted {
		out = append(out, 1)
	} else {
		out = append(out, 0)
	}
	return binary.BigEndian.AppendUint64(out, uint64(signedUnixMs))
}

// Sign sets rec's signer, signature and signing time.
func Sign(id *identity.Identity, rec *maitrev1.Record, signedUnixMs int64) error {
	return SignWith(id.Signer(), rec, signedUnixMs)
}

// SignWith is Sign with any Ed25519 signer (the Recovery Phrase's key).
func SignWith(s crypto.Signer, rec *maitrev1.Record, signedUnixMs int64) error {
	pub, ok := s.Public().(ed25519.PublicKey)
	if !ok {
		return errors.New("recsig: not an Ed25519 key")
	}
	body := rec.GetBody()
	if rec.GetDeleted() {
		body = nil
	}
	sig, err := s.Sign(nil, Canonical(rec.GetKind(), rec.GetId(), body, rec.GetDeleted(), signedUnixMs), crypto.Hash(0))
	if err != nil {
		return err
	}
	rec.SignerKey, rec.Signature, rec.SignedUnixMs = []byte(pub), sig, signedUnixMs
	return nil
}

// ErrUnsigned is a Record with no signature.
var ErrUnsigned = errors.New("the record is not signed")

// Verify checks rec's signature against its own signer key and returns the
// signer's fingerprint. It says nothing about whether that key may sign
// this kind; that is authz.
func Verify(rec *maitrev1.Record) (string, error) {
	key := rec.GetSignerKey()
	if len(key) == 0 && len(rec.GetSignature()) == 0 {
		return "", ErrUnsigned
	}
	if len(key) != ed25519.PublicKeySize {
		return "", errors.New("the record's signer key is not an Ed25519 key")
	}
	body := rec.GetBody()
	if rec.GetDeleted() {
		body = nil
	}
	if !ed25519.Verify(key, Canonical(rec.GetKind(), rec.GetId(), body, rec.GetDeleted(), rec.GetSignedUnixMs()), rec.GetSignature()) {
		return "", errors.New("the record's signature does not match its content")
	}
	return identity.Fingerprint(ed25519.PublicKey(key)), nil
}

// Signer returns the fingerprint of rec's signer without checking the
// signature, or "" when it has none.
func Signer(rec *maitrev1.Record) string {
	if len(rec.GetSignerKey()) != ed25519.PublicKeySize {
		return ""
	}
	return identity.Fingerprint(ed25519.PublicKey(rec.GetSignerKey()))
}
