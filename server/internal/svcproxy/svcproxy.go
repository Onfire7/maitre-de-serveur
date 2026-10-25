// Package svcproxy lets a Trusted Device reach a Private Service through the
// agent's mutual-TLS listener (decision 71). The Console runs a loopback
// proxy whose requests carry X-Maitre-Service; the agent forwards them to
// the Service's loopback web port. Paths are untouched, so web apps that
// use absolute URLs keep working, and WebSocket upgrades pass through.
package svcproxy

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Header names the Service.
const Header = "X-Maitre-Service"

// Handler wraps next: requests with the header go to the Service, the rest
// to next.
func Handler(st *store.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.Header.Get(Header)
		if name == "" {
			next.ServeHTTP(w, r)
			return
		}
		var spec maitrev1.ServiceSpec
		if _, err := st.Get(r.Context(), services.KindService, name, &spec); err != nil || spec.GetWebHostPort() == 0 {
			http.Error(w, "no such Service or it has no web page", http.StatusNotFound)
			return
		}
		target := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(int(spec.GetWebHostPort()))}
		rp := httputil.NewSingleHostReverseProxy(target)
		director := rp.Director
		rp.Director = func(req *http.Request) {
			director(req)
			req.Header.Del(Header)
			// Apps see the name they are known by, not the loopback port.
			req.Host = spec.GetName() + "." + services.DefaultSuffix
		}
		rp.ServeHTTP(w, r)
	})
}

// Lookup is exposed for tests.
func Lookup(ctx context.Context, st *store.Store, name string) (int, bool) {
	var spec maitrev1.ServiceSpec
	if _, err := st.Get(ctx, services.KindService, name, &spec); err != nil {
		return 0, false
	}
	return int(spec.GetWebHostPort()), spec.GetWebHostPort() != 0
}
