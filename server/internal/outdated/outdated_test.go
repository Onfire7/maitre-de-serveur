package outdated

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRegistry struct {
	digests map[string]string   // repo:tag → digest
	tags    map[string][]string // repo → tags
}

func (f fakeRegistry) Digest(_ context.Context, image string) (string, error) {
	if d, ok := f.digests[image]; ok {
		return d, nil
	}
	return "", fmt.Errorf("%s: not found", image)
}

func (f fakeRegistry) Tags(_ context.Context, image string) ([]string, error) {
	return f.tags[image[:strings.LastIndex(image, ":")]], nil
}

func digest(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

func TestNewer(t *testing.T) {
	tags := []string{"v3.3.5", "v3.3.10", "v3.4.0", "v3.4.1-rc1", "v3.4.0-cuda", "v4.0.0", "3.9.9", "latest", "v3.4"}
	for _, tc := range []struct{ tag, series, later string }{
		{"v3.3.5", "v3.4.0", "v4.0.0"},
		{"v3.4.0", "", "v4.0.0"},
		{"v4.0.0", "", ""},
		{"latest", "", ""},
	} {
		s, l := newer(tc.tag, tags)
		if s != tc.series || l != tc.later {
			t.Errorf("newer(%s) = %q, %q; want %q, %q", tc.tag, s, l, tc.series, tc.later)
		}
	}
	// A tag of one number is its own series.
	if s, l := newer("16", []string{"16", "16.4", "17", "18", "18-alpine"}); s != "" || l != "18" {
		t.Errorf("postgres 16: %q, %q", s, l)
	}
	// Leading zeros compare as numbers and are kept as written.
	if s, _ := newer("2026.05.0", []string{"2026.10.0", "2026.09.1"}); s != "2026.10.0" {
		t.Errorf("calendar: %q", s)
	}
}

func TestFollowVersion(t *testing.T) {
	for _, tc := range []struct{ entry, old, new, want string }{
		{"3.3.5", "v3.3.5", "v3.4.0", "3.4.0"},
		{"2026.5.0", "2026.05.0", "2026.10.0", "2026.10.0"},
		{"2026.05.0", "2026.05.0", "2026.06.0", "2026.06.0"},
		{"2026.10.1", "2026.10.1-java21", "2026.11.0-java21", "2026.11.0"},
		{"1.0", "16", "16", ""},
	} {
		got, _ := followVersion(tc.entry, tc.old, tc.new)
		if got != tc.want {
			t.Errorf("followVersion(%s, %s, %s) = %q, want %q", tc.entry, tc.old, tc.new, got, tc.want)
		}
	}
}

func TestCheckAndWrite(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `id: app
# the version people see
version: "3.3.5" # kept in step with the server
components:
  - name: server
    image: ghcr.io/x/server:v3.3.5@` + digest('a') + `
  - name: db
    image: docker.io/library/postgres:16@` + digest('b') + `
  - name: cache
    image: docker.io/valkey/valkey:9@` + digest('c') + `
`
	if err := os.WriteFile(filepath.Join(app, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	r := fakeRegistry{
		digests: map[string]string{
			"ghcr.io/x/server:v3.3.5":       digest('a'),
			"ghcr.io/x/server:v3.4.0":       digest('d'),
			"docker.io/library/postgres:16": digest('e'), // rebuilt
			"docker.io/valkey/valkey:9":     digest('c'),
		},
		tags: map[string][]string{
			"ghcr.io/x/server":           {"v3.3.5", "v3.4.0", "v3.4.1-rc1"},
			"docker.io/library/postgres": {"16", "17"},
			"docker.io/valkey/valkey":    {"9"},
		},
	}
	entries, err := Check(context.Background(), r, dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("Check: %v, %v", entries, err)
	}
	e := entries[0]
	if !e.Changed() || e.NewVersion != "3.4.0" {
		t.Fatalf("entry: %+v", e)
	}
	server, db, cache := e.Images[0], e.Images[1], e.Images[2]
	if server.Newer != "v3.4.0" || server.NewerDigest != digest('d') || db.Moved != digest('e') || db.NewerSeries != "17" || cache.Next() != "" {
		t.Fatalf("images: %+v", e.Images)
	}
	if err := Write(app, e); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(app, "manifest.yaml"))
	want := strings.NewReplacer(
		`"3.3.5"`, `"3.4.0"`,
		"server:v3.3.5@"+digest('a'), "server:v3.4.0@"+digest('d'),
		"postgres:16@"+digest('b'), "postgres:16@"+digest('e'),
	).Replace(manifest)
	if string(got) != want {
		t.Errorf("manifest:\n%s\nwant:\n%s", got, want)
	}
	md := Markdown(entries)
	for _, s := range []string{"v3.3.5 to v3.4.0", "postgres:16` was rebuilt", "`docker.io/library/postgres` 17, after 16", "version 3.3.5 to 3.4.0"} {
		if !strings.Contains(md, s) {
			t.Errorf("report lacks %q:\n%s", s, md)
		}
	}
}

func TestUnpinnedAndLookupFailure(t *testing.T) {
	dir := t.TempDir()
	for id, img := range map[string]string{"loose": "docker.io/x/y:1", "gone": "docker.io/x/gone:1@" + digest('a')} {
		os.MkdirAll(filepath.Join(dir, id), 0o755)
		os.WriteFile(filepath.Join(dir, id, "manifest.yaml"), []byte("image: "+img+"\n"), 0o644)
	}
	entries, err := Check(context.Background(), fakeRegistry{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if len(e.Images) != 1 || e.Images[0].Error == "" || e.Changed() {
			t.Errorf("%s: %+v", e.ID, e)
		}
	}
	if md := Markdown(entries); !strings.Contains(md, "## Not looked up") || !strings.Contains(md, "not pinned by digest") {
		t.Errorf("report:\n%s", md)
	}
}
