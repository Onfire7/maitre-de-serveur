package catalog_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
)

func TestProjectRepo(t *testing.T) {
	for _, tc := range []struct{ in, host, repo string }{
		{"https://github.com/home-assistant/core", "github.com", "home-assistant/core"},
		{"https://codeberg.org/forgejo/forgejo.git", "codeberg.org", "forgejo/forgejo"},
		{"https://gitlab.com/group/sub/project", "gitlab.com", "group/sub/project"},
	} {
		h, r, err := catalog.ProjectRepo(tc.in)
		if err != nil || h != tc.host || r != tc.repo {
			t.Errorf("%s: %s %s %v", tc.in, h, r, err)
		}
	}
	for _, bad := range []string{"http://github.com/a/b", "https://example.com/a/b", "https://github.com/a", "https://github.com/a/b/c"} {
		if _, _, err := catalog.ProjectRepo(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	for in, want := range map[string]string{
		"https://github.com/immich-app/immich":                "https://github.com/immich-app/immich",
		"https://github.com/linuxserver/docker-syncthing.git": "",
		"https://github.com/jellyfin/jellyfin-packaging":      "",
		"https://github.com/FreshRSS/FreshRSS.git":            "https://github.com/FreshRSS/FreshRSS",
		"https://github.com/home-assistant/core/tree/dev":     "https://github.com/home-assistant/core",
		"https://example.com/x/y":                             "",
		"":                                                    "",
	} {
		if got := catalog.ProjectFromSource(in); got != want {
			t.Errorf("ProjectFromSource(%q) = %q, want %q", in, got, want)
		}
	}
}

// popularity.json beside entries/ gives the index its stars.
func TestPopularityInIndex(t *testing.T) {
	root := t.TempDir()
	entries := filepath.Join(root, "entries")
	if err := os.CopyFS(filepath.Join(entries, "jellyfin"), os.DirFS("../../../catalog/entries/jellyfin")); err != nil {
		t.Fatal(err)
	}
	idx, err := catalog.LoadDir(entries)
	if err != nil || idx.Entries[0].Stars != 0 {
		t.Fatalf("without popularity.json: %v, %d", err, idx.Entries[0].Stars)
	}
	if err := catalog.WritePopularity(entries, &catalog.Popularity{Checked: "2026-10-29", Stars: map[string]int{"jellyfin": 41000}}); err != nil {
		t.Fatal(err)
	}
	idx, err = catalog.LoadDir(entries)
	if err != nil || idx.Entries[0].Stars != 41000 {
		t.Fatalf("with popularity.json: %v, %d", err, idx.Entries[0].Stars)
	}
	os.WriteFile(catalog.PopularityPath(entries), []byte("{"), 0o644)
	if _, problems, err := catalog.LoadDirLenient(entries); err != nil || len(problems) != 1 {
		t.Fatalf("a broken popularity.json in a user Catalog: %v, %v", problems, err)
	}
}
