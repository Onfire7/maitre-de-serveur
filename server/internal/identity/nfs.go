package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"time"
)

// NFS over TLS (decision 188): the kernel's TLS handshake daemon (tlshd,
// GnuTLS) cannot pin a raw key the way the agent's own mTLS does; it checks
// a certificate against a trust store and, as a client, the server's name.
// Each Node therefore has a second self-signed certificate, made from the
// same key, that never changes (fixed serial and dates; Ed25519 signatures
// are deterministic), names the Node by NFSName, and is published in its
// Node record so peers can put it in their trust store.

// NFSName is the host name a Node's NFS server answers to: the first half
// of its fingerprint under ".maitre". Peers mount by this name (mapped to
// an address in /etc/hosts), so the certificate does not depend on
// addresses that change.
func NFSName(fingerprint string) string {
	if len(fingerprint) > 32 {
		fingerprint = fingerprint[:32]
	}
	return fingerprint + ".maitre"
}

// NFSCertificatePEM is this Node's certificate for NFS over TLS, PEM encoded.
// The same identity always yields the same bytes.
func (id *Identity) NFSCertificatePEM() ([]byte, error) {
	if id.priv == nil {
		return nil, errors.New("identity: no private key")
	}
	pub := id.PublicKey()
	fp := Fingerprint(pub)
	serial, _ := hex.DecodeString(fp[:30])
	tmpl := &x509.Certificate{
		SerialNumber:          new(big.Int).SetBytes(serial),
		Subject:               pkix.Name{CommonName: NFSName(fp)},
		NotBefore:             time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{NFSName(fp)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, id.priv)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// PrivateKeyPEM is the private key as PKCS #8 PEM, for tlshd.
func (id *Identity) PrivateKeyPEM() ([]byte, error) {
	if id.priv == nil {
		return nil, errors.New("identity: no private key")
	}
	der, err := x509.MarshalPKCS8PrivateKey(id.priv)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// CheckNFSCertificate reports whether certPEM is a certificate for the Node
// with this fingerprint: its key must match, so a Node record cannot plant
// another Node's or a stranger's certificate in the trust store.
func CheckNFSCertificate(certPEM []byte, fingerprint string) error {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return errors.New("not a PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok || Fingerprint(pub) != fingerprint {
		return errors.New("certificate is not for this Node's key")
	}
	// Self-signed but not a CA, so check the signature itself rather than
	// with CheckSignatureFrom, which wants a CA parent.
	return cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature)
}
