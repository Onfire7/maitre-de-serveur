// Package identity holds the key pair that names a Node or a Trusted Device.
//
// An identity is an Ed25519 key pair. Its fingerprint (the lowercase hex
// SHA-256 of the raw public key) is the stable ID used everywhere else. The
// X.509 certificate wrapping it is self-signed and exists only because TLS
// needs one; peers never validate the certificate chain, only the key.
package identity

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const (
	privateKeyFile = "identity.key"
	publicKeyFile  = "identity.pub"
)

// Identity is an Ed25519 key pair plus the self-signed certificate TLS needs.
type Identity struct {
	priv ed25519.PrivateKey
	cert tls.Certificate
}

// Generate creates a fresh identity.
func Generate() (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	return fromKey(pub, priv)
}

// LoadOrGenerate reads the identity in dir, creating one if none exists.
func LoadOrGenerate(dir string) (*Identity, error) {
	id, err := Load(dir)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	id, err = Generate()
	if err != nil {
		return nil, err
	}
	if err := id.Save(dir); err != nil {
		return nil, err
	}
	return id, nil
}

// Load reads an identity previously written by Save.
func Load(dir string) (*Identity, error) {
	raw, err := os.ReadFile(filepath.Join(dir, privateKeyFile))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("%s: not a PEM private key", privateKeyFile)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an ed25519 key", privateKeyFile)
	}
	return fromKey(priv.Public().(ed25519.PublicKey), priv)
}

// Save writes the private key (0600) and public key (0644) into dir.
func (id *Identity) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(id.priv)
	if err != nil {
		return err
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(filepath.Join(dir, privateKeyFile), privPEM, 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, publicKeyFile), id.PublicKeyPEM(), 0o644)
}

// Fingerprint is the stable ID of this identity.
func (id *Identity) Fingerprint() string {
	return Fingerprint(id.priv.Public().(ed25519.PublicKey))
}

// PublicKeyPEM encodes the public key as a PEM block suitable for a trust list.
func (id *Identity) PublicKeyPEM() []byte {
	der, err := x509.MarshalPKIXPublicKey(id.priv.Public())
	if err != nil {
		panic(err) // ed25519 keys always marshal
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// PublicKey returns the raw Ed25519 public key.
func (id *Identity) PublicKey() ed25519.PublicKey { return id.priv.Public().(ed25519.PublicKey) }

// TLSCertificate is the self-signed certificate for use in a tls.Config.
func (id *Identity) TLSCertificate() tls.Certificate { return id.cert }

// Signer exposes the private key for signing pairing approvals.
func (id *Identity) Signer() crypto.Signer { return id.priv }

// Fingerprint computes the ID for any Ed25519 public key.
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// FingerprintOfCertificate returns the fingerprint of the Ed25519 key in cert,
// or an error if the certificate does not carry one.
func FingerprintOfCertificate(cert *x509.Certificate) (string, error) {
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok {
		return "", errors.New("peer certificate key is not ed25519")
	}
	return Fingerprint(pub), nil
}

// ParsePublicKeyPEM reads a PEM public key as written by PublicKeyPEM.
func ParsePublicKeyPEM(raw []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, errors.New("not a PEM public key")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("not an ed25519 public key")
	}
	return pub, nil
}

func fromKey(pub ed25519.PublicKey, priv ed25519.PrivateKey) (*Identity, error) {
	// A self-signed certificate with a long life. Nothing checks its dates
	// or name; the trust list checks the key. Rotating a key means pairing
	// again, so the certificate's lifetime is deliberately not a factor.
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: Fingerprint(pub)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(100, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{"maitre"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, fmt.Errorf("self-sign certificate: %w", err)
	}
	return &Identity{
		priv: priv,
		cert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv},
	}, nil
}
