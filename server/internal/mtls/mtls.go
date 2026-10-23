// Package mtls builds the TLS configurations every Maître connection uses:
// both sides present an identity certificate, and both sides check the
// peer's key against the trust list instead of a certificate chain.
package mtls

import (
	"crypto/tls"

	"github.com/maitre-de-serveur/maitre/server/internal/identity"
	"github.com/maitre-de-serveur/maitre/server/internal/trust"
)

// ServerConfig returns a tls.Config for a listener that requires a trusted
// client key.
func ServerConfig(id *identity.Identity, list *trust.List) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{id.TLSCertificate()},
		// RequireAnyClientCert makes the handshake demand a certificate but
		// skips chain verification; VerifyPeerCertificate does the real check.
		ClientAuth:            tls.RequireAnyClientCert,
		VerifyPeerCertificate: list.VerifyPeer,
		NextProtos:            []string{"h2"},
	}
}

// ClientConfig returns a tls.Config for dialling a Node whose key must be in
// the trust list.
func ClientConfig(id *identity.Identity, list *trust.List) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{id.TLSCertificate()},
		// InsecureSkipVerify only disables chain and hostname checks, which
		// have no meaning here. VerifyPeerCertificate still runs and pins the
		// server's key to the trust list.
		InsecureSkipVerify:    true, //nolint:gosec // see comment above
		VerifyPeerCertificate: list.VerifyPeer,
		NextProtos:            []string{"h2"},
	}
}
