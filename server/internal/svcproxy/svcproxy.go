// Package svcproxy lets a Trusted Device reach a Private Service through the
// agent's mutual-TLS listener (decision 71). The Console runs a loopback
// proxy whose requests carry X-Maitre-Service; the agent forwards them to
// the Service's loopback web port. Paths are untouched, so web apps that
// use absolute URLs keep working, and WebSocket upgrades pass through.
//
// The app sees one first-party client at http://<name>.local (decision
// 292): the Console's frame is on a loopback origin of its own, and the
// app must neither notice nor refuse that. So the request's Host, Origin
// and Referer all name the app's own host (Django's CSRF check compares
// Origin with Host and refused Tandoor's and Paperless's logins), a
// response's X-Frame-Options and Content-Security-Policy frame-ancestors
// are dropped (Tandoor, Paperless, Docmost, Home Assistant and PhotoPrism
// told the WebView not to be framed, and their pages stayed white), and a
// redirect to the app's own host is made relative (Grocy sent the frame to
// http://grocy.local/stockoverview, which nothing resolves). So is every
// URL at the app's own host in a page, script, stylesheet or JSON answer
// (decision 328): Stash builds every image and video address from Host,
// and the Console's view asked http://stash.local for them.
package svcproxy

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Header names the Service.
const Header = "X-Maitre-Service"

// PageHeader names the Page of the Service (ADR 0010); empty is its first.
const PageHeader = "X-Maitre-Page"

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
		var page *maitrev1.ServicePage
		if _, err := st.Get(r.Context(), services.KindService, name, &spec); err == nil {
			page = services.PageByName(&spec, r.Header.Get(PageHeader))
		}
		if page == nil || page.GetHostPort() == 0 {
			http.Error(w, "no such Service or it has no such Page", http.StatusNotFound)
			return
		}
		target := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(int(page.GetHostPort()))}
		rp := httputil.NewSingleHostReverseProxy(target)
		director := rp.Director
		host := services.PageHost(&spec, page) + "." + services.DefaultSuffix
		rp.Director = func(req *http.Request) {
			director(req)
			req.Header.Del(Header)
			req.Header.Del(PageHeader)
			// Apps see the name they are known by, not the loopback port.
			req.Host = host
			// The Console's frame is on a loopback origin of its own; the
			// app sees one first-party client, so a CSRF check that compares
			// Origin or Referer with Host passes.
			if req.Header.Get("Origin") != "" {
				req.Header.Set("Origin", "http://"+host)
			}
			if ref := req.Header.Get("Referer"); ref != "" {
				req.Header.Set("Referer", sameHost(ref, host))
			}
			// ReverseProxy adds X-Forwarded-For unless told not to, and
			// Home Assistant answers "400: Bad Request" to any request
			// carrying it from a proxy it was not configured to trust.
			// The Console is the client, not a proxy the app should know
			// about, so it sees none.
			req.Header["X-Forwarded-For"] = nil
			// The body may need its own-host URLs rewritten, so it must
			// come uncompressed. With no Accept-Encoding of the client's,
			// the transport asks for gzip itself and undoes it.
			req.Header.Del("Accept-Encoding")
		}
		rp.ModifyResponse = func(res *http.Response) error {
			// The Console frames the page; the app's refusal to be framed
			// is for other sites, not for its own Console.
			res.Header.Del("X-Frame-Options")
			for _, h := range []string{"Content-Security-Policy", "Content-Security-Policy-Report-Only"} {
				if v := res.Header.Get(h); v != "" {
					if nv := withoutFrameAncestors(v); nv != v {
						if nv == "" {
							res.Header.Del(h)
						} else {
							res.Header.Set(h, nv)
						}
					}
				}
			}
			// A redirect to the app's own host would leave the frame for an
			// address nothing resolves; relative, it stays on the proxy.
			if loc := res.Header.Get("Location"); loc != "" {
				res.Header.Set("Location", relativeTo(loc, host))
			}
			return rewriteBody(res, host)
		}
		rp.ServeHTTP(w, r)
	})
}

// sameHost returns raw with its host replaced by host, or raw as it was if
// it is not an absolute http(s) URL.
func sameHost(raw, host string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return raw
	}
	u.Scheme = "http"
	u.Host = host
	return u.String()
}

// relativeTo makes an absolute URL at host relative; any other is left.
func relativeTo(raw, host string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.EqualFold(u.Host, host) {
		return raw
	}
	u.Scheme, u.Host, u.User = "", "", nil
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

// rewriteBody makes the URLs at host in a text response root-relative.
// Media, downloads and streams pass untouched: only the types a page is
// built from are read, and nothing still encoded.
func rewriteBody(res *http.Response, host string) error {
	if res.Body == nil || res.Header.Get("Content-Encoding") != "" || !rewritable(res.Header.Get("Content-Type")) {
		return nil
	}
	b, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return err
	}
	b = relativeURLs(b, host)
	res.Body = io.NopCloser(bytes.NewReader(b))
	res.ContentLength = int64(len(b))
	res.Header.Set("Content-Length", strconv.Itoa(len(b)))
	return nil
}

// rewritable reports the content types a page is built from.
func rewritable(ct string) bool {
	t, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	switch t {
	case "text/html", "application/xhtml+xml", "text/css", "text/javascript", "application/javascript", "application/x-javascript", "application/json", "text/xml", "application/xml":
		return true
	}
	return strings.HasSuffix(t, "+json")
}

// relativeURLs replaces http://host, https://host and //host, also with
// their slashes escaped as JSON may write them, by the root they name.
// Only host itself: host:port, a longer name and host in any other text
// are left.
func relativeURLs(b []byte, host string) []byte {
	if !bytes.Contains(bytes.ToLower(b), []byte(strings.ToLower(host))) {
		return b
	}
	var prefixes []urlPrefix
	for _, slash := range []string{"/", `\/`} {
		for _, scheme := range []string{"http:", "https:", ""} {
			prefixes = append(prefixes, urlPrefix{[]byte(scheme + slash + slash + host), []byte(slash)})
		}
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		if c := b[i]; c == 'h' || c == 'H' || c == '/' || c == '\\' {
			if p, ok := ownURL(b[i:], prefixes); ok {
				i += len(p.p)
				// A path follows, or the root alone, which written as
				// nothing would be the current page instead.
				if !bytes.HasPrefix(b[i:], p.root) {
					out = append(out, p.root...)
				}
				continue
			}
		}
		out = append(out, b[i])
		i++
	}
	return out
}

// urlPrefix is a scheme and host to replace and the root it becomes, its
// slash escaped or not.
type urlPrefix struct{ p, root []byte }

// ownURL is the prefix b starts with, if b goes on to no longer host name
// and no port.
func ownURL(b []byte, prefixes []urlPrefix) (urlPrefix, bool) {
	for _, x := range prefixes {
		if len(b) >= len(x.p) && bytes.EqualFold(b[:len(x.p)], x.p) && (len(b) == len(x.p) || !hostByte(b[len(x.p)])) {
			return x, true
		}
	}
	return urlPrefix{}, false
}

// hostByte reports a byte that would continue a host name or add a port.
func hostByte(c byte) bool {
	return c == '.' || c == '-' || c == ':' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// withoutFrameAncestors drops the frame-ancestors directive from a CSP
// value, leaving the other directives as they were.
func withoutFrameAncestors(csp string) string {
	var kept []string
	for _, d := range strings.Split(csp, ";") {
		t := strings.TrimSpace(d)
		if t == "" {
			continue
		}
		if name, _, _ := strings.Cut(t, " "); strings.EqualFold(name, "frame-ancestors") {
			continue
		}
		kept = append(kept, t)
	}
	if len(kept) == 0 {
		return ""
	}
	return strings.Join(kept, "; ")
}

// Lookup is exposed for tests.
func Lookup(ctx context.Context, st *store.Store, name string) (int, bool) {
	var spec maitrev1.ServiceSpec
	if _, err := st.Get(ctx, services.KindService, name, &spec); err != nil {
		return 0, false
	}
	port := services.WebHostPort(&spec)
	return port, port != 0
}
