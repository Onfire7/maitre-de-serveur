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

func TestForwardsByHeader(t *testing.T) {
	ctx := context.Background()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello from "+r.URL.Path+" host="+r.Host+" hdr="+r.Header.Get(svcproxy.Header))
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
	if code, body := get("kuma", "/dashboard/1"); code != 200 || body != "hello from /dashboard/1 host=kuma.maitre.local hdr=" {
		t.Fatalf("proxied: %d %q", code, body)
	}
	if code, _ := get("headless", "/"); code != 404 {
		t.Fatalf("headless: %d", code)
	}
	if code, _ := get("nope", "/"); code != 404 {
		t.Fatalf("unknown: %d", code)
	}
}
