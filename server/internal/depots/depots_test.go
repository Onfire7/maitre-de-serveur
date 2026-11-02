package depots_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/depots"
)

func TestRefreshMergesBootedRegistryAndDiscovered(t *testing.T) {
	ctx := context.Background()
	w := depots.New(func(context.Context) string { return "192.168.4.54:8080/maitre-server:dev" })
	w.Fetch = func(_ context.Context, host string) (*depots.Listing, error) {
		switch host {
		case "192.168.4.54:8080":
			return &depots.Listing{Registry: "192.168.4.54:8080", Images: map[string][]depots.TagInfo{"maitre-server": {
				{Tag: "dev", Digest: "sha256:new", Version: "e8e4f2f", Created: "2026-11-02T03:05:45Z"},
				{Tag: "dev2", Digest: "sha256:old", Version: "3c29513", Created: "2026-10-25T13:18:25Z"},
			}}}, nil
		case "10.0.0.9:8080":
			return &depots.Listing{Registry: "10.0.0.9:8080", Images: map[string][]depots.TagInfo{"maitre-server": {
				{Tag: "main", Digest: "sha256:m", Created: "2026-11-01T00:00:00Z"},
			}}}, nil
		}
		return nil, errors.New("no route")
	}
	w.Discovered("abc", "10.0.0.9:8080")
	w.Discovered("gone", "10.0.0.10:8080")
	w.Refresh(ctx)
	got := w.Images()
	var refs []string
	for _, i := range got {
		refs = append(refs, i.GetRef())
	}
	want := "192.168.4.54:8080/maitre-server:dev 10.0.0.9:8080/maitre-server:main 192.168.4.54:8080/maitre-server:dev2"
	if strings.Join(refs, " ") != want {
		t.Fatalf("got %q", strings.Join(refs, " "))
	}
	if got[0].GetDepot() != "192.168.4.54:8080" || got[0].GetVersion() != "e8e4f2f" || got[0].GetDigest() != "sha256:new" {
		t.Fatalf("first %+v", got[0])
	}
	if depots.Offered("192.168.4.54:8080/maitre-server:dev", got) != "sha256:new" || depots.Offered("ghcr.io/x:y", got) != "" {
		t.Fatal("Offered")
	}
}

// The default Fetch reads a Depot's /api/images over plain HTTP, and a
// registry that is not a Depot (ghcr.io: no such path) adds nothing.
func TestFetchHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/images" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"registry":"depot:8080","images":{"maitre-server":[{"tag":"dev","digest":"sha256:d","version":"v","created":"2026-11-02T00:00:00Z"}]}}`))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	w := depots.New(func(context.Context) string { return host + "/maitre-server:dev" })
	w.Refresh(context.Background())
	if got := w.Images(); len(got) != 1 || got[0].GetRef() != "depot:8080/maitre-server:dev" {
		t.Fatalf("got %+v", got)
	}
	plain := httptest.NewServer(http.NotFoundHandler())
	defer plain.Close()
	w = depots.New(func(context.Context) string { return strings.TrimPrefix(plain.URL, "http://") + "/maitre-server:dev" })
	w.Refresh(context.Background())
	if got := w.Images(); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}
