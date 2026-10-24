// Package recovery implements the Recovery Phrase (design question 67).
//
// A phrase is twelve words from the BIP39 English list encoding 128 bits of
// entropy plus a 4-bit checksum. The entropy seeds an Ed25519 key with the
// standard library; the public half is stored in the Fleet as a Trusted
// Device marked recovery_only. Typing the phrase into a new Console lets it
// derive the same key and approve itself. Nothing secret is kept on Nodes.
package recovery

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"strings"
)

//go:embed english.txt
var wordlistRaw string

var (
	words     []string
	wordIndex map[string]int
)

func init() {
	words = strings.Fields(wordlistRaw)
	if len(words) != 2048 {
		panic(fmt.Sprintf("recovery: word list has %d words, want 2048", len(words)))
	}
	wordIndex = make(map[string]int, len(words))
	for i, w := range words {
		wordIndex[w] = i
	}
}

// Phrase is twelve words.
type Phrase []string

// String joins the words with single spaces.
func (p Phrase) String() string { return strings.Join(p, " ") }

// Generate makes a fresh phrase from the system random source.
func Generate() (Phrase, error) {
	entropy := make([]byte, 16)
	if _, err := rand.Read(entropy); err != nil {
		return nil, err
	}
	return FromEntropy(entropy), nil
}

// FromEntropy encodes 16 bytes as twelve words with a BIP39 checksum.
func FromEntropy(entropy []byte) Phrase {
	if len(entropy) != 16 {
		panic("recovery: entropy must be 16 bytes")
	}
	sum := sha256.Sum256(entropy)
	// 128 entropy bits + 4 checksum bits = 132 bits = 12 x 11-bit indexes.
	bits := make([]byte, 0, 17)
	bits = append(bits, entropy...)
	bits = append(bits, sum[0]) // only the top 4 bits are used
	p := make(Phrase, 12)
	for i := 0; i < 12; i++ {
		var idx int
		for b := 0; b < 11; b++ {
			pos := i*11 + b
			bit := (bits[pos/8] >> (7 - pos%8)) & 1
			idx = idx<<1 | int(bit)
		}
		p[i] = words[idx]
	}
	return p
}

// ErrInvalid is returned by Parse for anything that is not a valid phrase.
var ErrInvalid = errors.New("recovery phrase is not valid")

// Parse accepts the words in any spacing or case and checks the checksum.
// It returns the 16 bytes of entropy.
func Parse(text string) ([]byte, error) {
	fields := strings.Fields(strings.ToLower(text))
	if len(fields) != 12 {
		return nil, fmt.Errorf("%w: need 12 words, got %d", ErrInvalid, len(fields))
	}
	var bits [17]byte
	for i, w := range fields {
		idx, ok := wordIndex[w]
		if !ok {
			return nil, fmt.Errorf("%w: %q is not a recovery word", ErrInvalid, w)
		}
		for b := 0; b < 11; b++ {
			pos := i*11 + b
			if idx>>(10-b)&1 == 1 {
				bits[pos/8] |= 1 << (7 - pos%8)
			}
		}
	}
	entropy := bits[:16]
	sum := sha256.Sum256(entropy)
	if sum[0]>>4 != bits[16]>>4 {
		return nil, fmt.Errorf("%w: a word is wrong", ErrInvalid)
	}
	return append([]byte(nil), entropy...), nil
}

// Key derives the recovery Ed25519 key from entropy. The seed is a domain-
// separated hash of the entropy so the same entropy could seed other keys
// later without reuse.
func Key(entropy []byte) ed25519.PrivateKey {
	seed := sha256.Sum256(append([]byte("maitre-recovery-v1\x00"), entropy...))
	return ed25519.NewKeyFromSeed(seed[:])
}
