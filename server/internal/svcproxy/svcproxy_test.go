package svcproxy_test

import (
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
