// Package pairing derives the short pairing code shown on an Installer's
// screen and in the Console (design question 66).
//
// The code is a six-digit decimal number taken from SHA-256 over both raw
// Ed25519 public keys in a fixed order. Both sides compute it independently;
// the person at the Console confirms the two displays match. An attacker in
// the middle would have to present keys that produce the same six digits on
// both screens, which they cannot influence after the fact.
package pairing

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// Code returns the pairing code for an Installer key and a device key.
// Order matters and is fixed: installer first.
func Code(installer, device ed25519.PublicKey) string {
	h := sha256.New()
	h.Write([]byte("maitre-pairing-v1\x00"))
	h.Write(installer)
	h.Write(device)
	sum := h.Sum(nil)
	n := binary.BigEndian.Uint32(sum[:4]) % 1_000_000
	return fmt.Sprintf("%03d %03d", n/1000, n%1000)
}
