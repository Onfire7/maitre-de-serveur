// Package peer carries the TLS peer's identity from the HTTP layer into
// handler contexts, so services can tell which Trusted Device or Node is
// calling without re-parsing certificates.
package peer

import (
	"context"
	"crypto/ed25519"
	"net/http"

	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
)

type key struct{}

// Info describes the verified TLS client.
type Info struct {
	Fingerprint string
	PublicKey   ed25519.PublicKey
}

// Middleware stores the leaf client certificate's key in the request context.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			if pub, ok := r.TLS.PeerCertificates[0].PublicKey.(ed25519.PublicKey); ok {
				r = r.WithContext(context.WithValue(r.Context(), key{}, Info{
					Fingerprint: identity.Fingerprint(pub),
					PublicKey:   pub,
				}))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// From returns the caller's identity, if the connection was mutually
// authenticated.
func From(ctx context.Context) (Info, bool) {
	p, ok := ctx.Value(key{}).(Info)
	return p, ok
}

// WithInfo returns ctx carrying info, for tests and in-process callers.
func WithInfo(ctx context.Context, info Info) context.Context {
	return context.WithValue(ctx, key{}, info)
}
