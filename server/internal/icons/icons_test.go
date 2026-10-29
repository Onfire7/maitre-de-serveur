package icons_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/icons"
)

// A stand-in for GitHub: each pack at a commit, with the icons it has.
func packs(t *testing.T, files map[string]string) *icons.Fetcher {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sha, ok := strings.CutPrefix(r.URL.Path, "/api/repos/"); ok {
			w.Write([]byte(strings.Repeat(string(sha[0]), 40)))
			return
		}
		body, ok := files[strings.TrimPrefix(r.URL.Path, "/raw/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &icons.Fetcher{Client: srv.Client(), RawBase: srv.URL + "/raw", APIBase: srv.URL + "/api"}
}

func TestFetchPrefersDashboardIconsSVG(t *testing.T) {
	h := strings.Repeat("h", 40)
	s := strings.Repeat("s", 40)
	f := packs(t, map[string]string{
		"homarr-labs/dashboard-icons/" + h + "/png/app.png": "PNG",
		"homarr-labs/dashboard-icons/" + h + "/svg/app.svg": "<svg/>",
		"selfhst/icons/" + s + "/svg/other.svg":             "<svg other/>",
	})
	icon, err := f.Fetch(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	if icon.File != "icon.svg" || string(icon.Body) != "<svg/>" || icon.Credit.Pack != "dashboard-icons" || !strings.Contains(icon.Credit.URL, "/"+h+"/svg/app.svg") {
		t.Fatalf("icon %+v", icon)
	}
	// Not in the first pack: the second, with its licence.
	icon, err = f.Fetch(context.Background(), "other")
	if err != nil || icon.Credit.Licence != "CC-BY-4.0" {
		t.Fatalf("other: %+v %v", icon, err)
	}
	if _, err := f.Fetch(context.Background(), "missing"); !errors.Is(err, icons.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := f.Fetch(context.Background(), "../x"); err == nil {
		t.Fatal("a path was taken for a name")
	}
}
