package svcproxy_test

import (
	"compress/gzip"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/svcproxy"
)

// The app sees one first-party client at its own host (decision 292): Origin
// and Referer follow Host, a refusal to be framed is dropped, frame-ancestors
// goes from the CSP and the rest stays, and a redirect to its own host is
// made relative while one elsewhere is left.
func TestFirstPartyClient(t *testing.T) {
	ctx := context.Background()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/go":
			http.Redirect(w, r, "http://kuma.local/stockoverview?x=1", http.StatusFound)
			return
		case "/away":
			http.Redirect(w, r, "https://example.com/elsewhere", http.StatusFound)
			return
		case "/home":
			http.Redirect(w, r, "http://KUMA.local", http.StatusFound)
			return
		}
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; img-src 'self' data:")
		w.Header().Set("Content-Security-Policy-Report-Only", "frame-ancestors 'none'")
		io.WriteString(w, "origin="+r.Header.Get("Origin")+" referer="+r.Header.Get("Referer"))
	}))
	defer app.Close()
	_, portStr, _ := net.SplitHostPort(app.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	st, _ := store.Open(filepath.Join(t.TempDir(), "f.db"), "n")
	defer st.Close()
	st.Put(ctx, services.KindService, "kuma", &maitrev1.ServiceSpec{Name: "kuma", WebHostPort: uint32(port)}, false)
	srv := httptest.NewServer(svcproxy.Handler(st, http.NotFoundHandler()))
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string, hdr map[string]string) *http.Response {
		req, _ := http.NewRequest("POST", srv.URL+path, nil)
		req.Header.Set(svcproxy.Header, "kuma")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := get("/login", map[string]string{"Origin": "http://127.0.0.1:21336", "Referer": "http://127.0.0.1:21336/accounts/login/?next=/"})
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if got := string(b); got != "origin=http://kuma.local referer=http://kuma.local/accounts/login/?next=/" {
		t.Fatalf("headers seen by the app: %q", got)
	}
	if v := res.Header.Get("X-Frame-Options"); v != "" {
		t.Fatalf("X-Frame-Options kept: %q", v)
	}
	if v := res.Header.Get("Content-Security-Policy"); v != "default-src 'self'; img-src 'self' data:" {
		t.Fatalf("CSP: %q", v)
	}
	if _, ok := res.Header["Content-Security-Policy-Report-Only"]; ok {
		t.Fatalf("a CSP of only frame-ancestors should go")
	}
	// No Origin or Referer sent: none made up.
	res = get("/plain", nil)
	b, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if got := string(b); got != "origin= referer=" {
		t.Fatalf("headers made up: %q", got)
	}
	for path, want := range map[string]string{"/go": "/stockoverview?x=1", "/away": "https://example.com/elsewhere", "/home": "/"} {
		res = get(path, nil)
		res.Body.Close()
		if res.StatusCode != 302 || res.Header.Get("Location") != want {
			t.Fatalf("%s: %d %q, want %q", path, res.StatusCode, res.Header.Get("Location"), want)
		}
	}
}

func TestForwardsByHeader(t *testing.T) {
	ctx := context.Background()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello from "+r.URL.Path+" host="+r.Host+" hdr="+r.Header.Get(svcproxy.Header)+" xff="+r.Header.Get("X-Forwarded-For"))
	}))
	defer app.Close()
	_, portStr, _ := net.SplitHostPort(app.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	st, _ := store.Open(filepath.Join(t.TempDir(), "f.db"), "n")
	defer st.Close()
	st.Put(ctx, services.KindService, "kuma", &maitrev1.ServiceSpec{Name: "kuma", WebHostPort: uint32(port)}, false)
	st.Put(ctx, services.KindService, "headless", &maitrev1.ServiceSpec{Name: "headless"}, false)

	h := svcproxy.Handler(st, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "rpc") }))
	srv := httptest.NewServer(h)
	defer srv.Close()

	get := func(name, path string) (int, string) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if name != "" {
			req.Header.Set(svcproxy.Header, name)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, body := get("", "/maitre.v1.X/Y"); code != 200 || body != "rpc" {
		t.Fatalf("passthrough: %d %q", code, body)
	}
	// No X-Forwarded-For: Home Assistant refuses requests that carry one from an untrusted proxy.
	if code, body := get("kuma", "/dashboard/1"); code != 200 || body != "hello from /dashboard/1 host=kuma.local hdr= xff=" {
		t.Fatalf("proxied: %d %q", code, body)
	}
	if code, _ := get("headless", "/"); code != 404 {
		t.Fatalf("headless: %d", code)
	}
	if code, _ := get("nope", "/"); code != 404 {
		t.Fatalf("unknown: %d", code)
	}
}

// Stash builds every image and video address from Host, and the view asked
// http://stash.local for them (decision 328). Own-host URLs in what a page
// is built from become root-relative, whether gzipped by the app or with
// their slashes escaped as PHP writes JSON; another host, another port, a
// longer name and media are left as they were.
func TestOwnHostURLsInBodies(t *testing.T) {
	ctx := context.Background()
	media := "http://stash.local/image/1/image"
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body, ct string
		switch r.URL.Path {
		case "/graphql":
			ct = "application/json"
			body = `{"thumbnail":"http://stash.local/image/1/thumbnail?t=ca","image":"HTTPS://Stash.Local/image/1/image","home":"http://stash.local","php":"http:\/\/stash.local\/api\/x","phproot":"http:\/\/stash.local","other":"http://stash.localhost/a","port":"http://stash.local:9999/a","far":"https://example.com/x","proto":"//stash.local/s.js"}`
		case "/page":
			ct = "text/html; charset=utf-8"
			body = `<link href="http://stash.local/app.css"><a href="http://stash.local?x=1">stash.local</a>`
		case "/video":
			ct = "video/mp4"
			body = media
		case "/gz":
			if r.Header.Get("Accept-Encoding") == "" {
				t.Errorf("no gzip asked for")
			}
			w.Header().Set("Content-Type", "text/css")
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			io.WriteString(zw, `body{background:url(http://stash.local/bg.png)}`)
			zw.Close()
			return
		}
		w.Header().Set("Content-Type", ct)
		io.WriteString(w, body)
	}))
	defer app.Close()
	_, portStr, _ := net.SplitHostPort(app.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	st, _ := store.Open(filepath.Join(t.TempDir(), "f.db"), "n")
	defer st.Close()
	st.Put(ctx, services.KindService, "stash", &maitrev1.ServiceSpec{Name: "stash", WebHostPort: uint32(port)}, false)
	srv := httptest.NewServer(svcproxy.Handler(st, http.NotFoundHandler()))
	defer srv.Close()
	for path, want := range map[string]string{
		"/graphql": `{"thumbnail":"/image/1/thumbnail?t=ca","image":"/image/1/image","home":"/","php":"\/api\/x","phproot":"\/","other":"http://stash.localhost/a","port":"http://stash.local:9999/a","far":"https://example.com/x","proto":"/s.js"}`,
		"/page":    `<link href="/app.css"><a href="/?x=1">stash.local</a>`,
		"/video":   media,
		"/gz":      `body{background:url(/bg.png)}`,
	} {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set(svcproxy.Header, "stash")
		req.Header.Set("Accept-Encoding", "gzip, br")
		// A transport of its own, so nothing undoes an encoding the
		// Console would have had to.
		res, err := (&http.Transport{DisableCompression: true}).RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if string(b) != want || res.Header.Get("Content-Encoding") != "" {
			t.Errorf("%s: %q (encoding %q)\nwant %q", path, b, res.Header.Get("Content-Encoding"), want)
		}
		if cl := res.Header.Get("Content-Length"); cl != "" && cl != strconv.Itoa(len(b)) {
			t.Errorf("%s: Content-Length %s for %d bytes", path, cl, len(b))
		}
	}
}
