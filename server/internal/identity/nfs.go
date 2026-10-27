package identity

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"time"
)

// NFS over TLS (decision 188): the kernel's TLS handshake daemon (tlshd,
// GnuTLS) cannot pin a raw key the way the agent's own mTLS does; it checks
// a certificate against a trust store and, as a client, the server's name.
// It then gives the peer's certificate to the kernel's keyring, which is how
// the NFS server learns the client authenticated; the kernel's X.509 parser
// has no Ed25519 (add_key fails with ENOPKG, and an export that demands
// xprtsec=mtls answers "wrong security").
//
// So each Node has an ECDSA P-256 key derived from its identity key (no
// second secret to keep or back up) and a self-signed certificate for it
// that never changes (fixed serial and dates, RFC 6979 signatures), naming
// the Node by NFSName. The identity key signs that certificate, and the
// Node record publishes both, so a peer trusts the certificate only when
// the Node's own key vouches for it.

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

// nfsKey derives the NFS key from the identity key's seed.
func (id *Identity) nfsKey() (*ecdsa.PrivateKey, error) {
	if id.priv == nil {
		return nil, errors.New("identity: no private key")
	}
	for i := 0; i < 16; i++ {
		raw, err := hkdf.Key(sha256.New, id.priv.Seed(), nil, fmt.Sprintf("maitre nfs p-256 %d", i), 32)
		if err != nil {
			return nil, err
		}
		// Rejects the rare value outside the curve's order; try the next.
		if k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw); err == nil {
			return k, nil
		}
	}
	return nil, errors.New("identity: no NFS key")
}

// deterministic signs with RFC 6979 nonces, so the certificate's bytes
// depend only on the key.
type deterministic struct{ k *ecdsa.PrivateKey }

func (d deterministic) Public() crypto.PublicKey { return d.k.Public() }
func (d deterministic) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return d.k.Sign(nil, digest, opts)
}

// NFSCredentials returns this Node's NFS certificate (PEM), the identity
// key's signature over its DER bytes, and the NFS private key (PKCS #8
// PEM) for tlshd. The same identity always yields the same bytes.
func (id *Identity) NFSCredentials() (certPEM, signature, keyPEM []byte, err error) {
	k, err := id.nfsKey()
	if err != nil {
		return nil, nil, nil, err
	}
	fp := id.Fingerprint()
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
	der, err := x509.CreateCertificate(nil, tmpl, tmpl, k.Public(), deterministic{k})
	if err != nil {
		return nil, nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		ed25519.Sign(id.priv, der),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

// CheckNFSCertificate reports whether certPEM is the NFS certificate of the
// Node whose identity public key is pub: signed by that key, self-signed,
// P-256, and naming that Node. A Node record cannot plant another Node's or
// a stranger's certificate in the trust store.
func CheckNFSCertificate(certPEM, signature []byte, pub ed25519.PublicKey) error {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return errors.New("not a PEM certificate")
	}
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, block.Bytes, signature) {
		return errors.New("certificate is not signed by this Node's key")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	if k, ok := cert.PublicKey.(*ecdsa.PublicKey); !ok || k.Curve != elliptic.P256() {
		return errors.New("certificate key is not P-256")
	}
	if want := NFSName(Fingerprint(pub)); len(cert.DNSNames) != 1 || cert.DNSNames[0] != want {
		return fmt.Errorf("certificate does not name %s", want)
	}
	// Self-signed but not a CA, so check the signature itself rather than
	// with CheckSignatureFrom, which wants a CA parent.
	if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
		return err
	}
	if !bytes.Equal(block.Bytes, cert.Raw) {
		return errors.New("certificate has trailing data")
	}
	return nil
}
