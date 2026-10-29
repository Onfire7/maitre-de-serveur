package convert_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
	"github.com/Onfire7/maitre-de-serveur/server/internal/convert"
	"github.com/Onfire7/maitre-de-serveur/server/internal/quadlet"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
)

var update = flag.Bool("update", false, "rewrite testdata/*/want from the converter")

// fakeRegistry pins every tag to a digest made of its own name.
type fakeRegistry struct{}

func (fakeRegistry) Digest(_ context.Context, image string) (string, error) {
	if strings.Contains(image, "unresolvable") {
		return "", errors.New("manifest unknown")
	}
	sum := sha256.Sum256([]byte(image))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// check is the Node's own check; where Quadlet is not installed it still
// classifies the options, so the report is the same everywhere.
func check(ctx context.Context, spec *maitrev1.ServiceSpec) (*services.CheckResult, error) {
	res, err := services.Check(ctx, spec)
	if errors.Is(err, quadlet.ErrNoGenerator) {
		err = nil
	}
	return res, err
}

func run(t *testing.T, name string, opts convert.Options) *convert.Result {
	t.Helper()
	opts.Resolver, opts.Check = fakeRegistry{}, check
	opts.Now = time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	res, err := convert.Convert(context.Background(), opts)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

// Every upstream file converts to the bundle in want/, which loads as a
// Catalog entry.
func TestCorpus(t *testing.T) {
	// id is given where the file has no name: of its own, as a person would.
	cases := []struct {
		name, input, licence, id string
	}{
		{"immich", "docker-compose.yml", "AGPL-3.0", ""},
		{"paperless-ngx", "docker-compose.postgres.yml", "GPL-3.0", "paperless-ngx"},
		{"authentik", "docker-compose.yml", "MIT", "authentik"},
		{"casaos-jellyfin", "docker-compose.yml", "Apache-2.0", ""},
		{"escaping", "compose.yaml", "MIT", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := run(t, tc.name, convert.Options{Compose: filepath.Join("testdata", tc.name, "input", tc.input), Licence: tc.licence, ID: tc.id, URL: "https://example.invalid/" + tc.name})
			if len(res.Report.MustFix) > 0 {
				t.Fatalf("must fix: %v", res.Report.MustFix)
			}
			want := filepath.Join("testdata", tc.name, "want")
			got := map[string]string{}
			for n, b := range res.Files {
				if n != "compose.yaml" {
					got[n] = string(b)
				}
			}
			if *update {
				os.RemoveAll(want)
				os.MkdirAll(want, 0o755)
				for n, b := range got {
					os.WriteFile(filepath.Join(want, n), []byte(b), 0o644)
				}
			}
			files, _ := os.ReadDir(want)
			var names []string
			for _, f := range files {
				names = append(names, f.Name())
				b, _ := os.ReadFile(filepath.Join(want, f.Name()))
				if got[f.Name()] != string(b) {
					t.Errorf("%s differs; run with -update and review.\n--- got\n%s\n--- want\n%s", f.Name(), got[f.Name()], b)
				}
			}
			var gotNames []string
			for n := range got {
				gotNames = append(gotNames, n)
			}
			sort.Strings(names)
			sort.Strings(gotNames)
			if strings.Join(names, " ") != strings.Join(gotNames, " ") {
				t.Errorf("files: got %v, want %v", gotNames, names)
			}
			// The bundle is a Catalog entry as written.
			dir := t.TempDir()
			for n, b := range res.Files {
				os.WriteFile(filepath.Join(dir, n), b, 0o644)
			}
			if _, err := catalog.LoadEntry(dir); err != nil {
				t.Fatalf("written bundle does not load: %v", err)
			}
		})
	}
}

// What the converter decides, read from the bundles rather than the golden
// text, so a change of wording does not hide a change of meaning.
func TestDecisions(t *testing.T) {
	immich := run(t, "immich", convert.Options{Compose: "testdata/immich/input/docker-compose.yml", Licence: "AGPL-3.0"}).Entry
	byName := map[string]catalog.Component{}
	for _, c := range immich.Components {
		byName[c.Name] = c
	}
	if immich.Web.Component != "immich-server" || immich.Web.Port != 2283 {
		t.Errorf("web %+v", immich.Web)
	}
	if got := byName["immich-server"].After; strings.Join(got, ",") != "database,redis" {
		t.Errorf("server after %v", got)
	}
	// DB_PASSWORD reaches the database and the server as one made-up setting.
	if !strings.Contains(byName["database"].Quadlet, "POSTGRES_PASSWORD={{ .Settings.DB_PASSWORD }}") || !strings.Contains(byName["immich-server"].Quadlet, "DB_PASSWORD={{ .Settings.DB_PASSWORD }}") {
		t.Errorf("password not a setting:\n%s\n%s", byName["database"].Quadlet, byName["immich-server"].Quadlet)
	}
	// The host's clock became the TZ setting; ./library a Service volume.
	if strings.Contains(byName["immich-server"].Quadlet, "/etc/localtime") || !strings.Contains(byName["immich-server"].Quadlet, "{{ .Volumes.library }}:/data:Z") {
		t.Errorf("server volumes:\n%s", byName["immich-server"].Quadlet)
	}
	// Its images are pinned; its own .env tag (v3) was used.
	if !strings.HasPrefix(byName["immich-server"].Image, "ghcr.io/immich-app/immich-server:v3@sha256:") {
		t.Errorf("image %s", byName["immich-server"].Image)
	}

	authentik := run(t, "authentik", convert.Options{Compose: "testdata/authentik/input/docker-compose.yml", Licence: "MIT", ID: "authentik"}).Entry
	for _, c := range authentik.Components {
		if c.Name == "postgresql" && !c.WaitHealthy {
			t.Error("postgresql: service_healthy did not become wait_healthy")
		}
		if c.Name == "postgresql" && !strings.Contains(c.Quadlet, "HealthCmd=pg_isready -d $${POSTGRES_DB} -U $${POSTGRES_USER}") {
			t.Errorf("health check:\n%s", c.Quadlet)
		}
	}
	if len(authentik.Unfinished) == 0 {
		t.Error("the docker socket did not leave authentik unfinished")
	}

	jf := run(t, "casaos", convert.Options{Compose: "testdata/casaos-jellyfin/input", Licence: "Apache-2.0"}).Entry
	if jf.ID != "jellyfin" || jf.Name != "Jellyfin" || jf.Summary != "The personal Media System" || jf.Category != "media" || jf.Source.From != "casaos" {
		t.Errorf("casaos manifest %+v", jf)
	}
	if jf.Web.Port != 8096 || jf.Image == "" || len(jf.Components) != 0 {
		t.Errorf("casaos web or layout: %+v", jf)
	}
	for _, v := range jf.Volumes {
		if v.Name == "media" && !v.Shared {
			t.Error("CasaOS's /DATA/Media is not a shared volume")
		}
	}

	esc := run(t, "escaping", convert.Options{Compose: "testdata/escaping/input/compose.yaml", Licence: "MIT"}).Entry
	for _, want := range []string{
		`Environment="GREETING=hello world"`,
		`Environment="QUOTED=say \"hi\""`,
		`Environment="TEMPLATE={{ "{{" }} not a template }}"`,
		`Environment="PRICE=$$5 and 10%%"`,
		`Environment="API_TOKEN={{ .Settings.API_TOKEN }}"`,
		`HealthCmd=["wget","-q","-O-","http://localhost/"]`,
		`%%Y`,
	} {
		if !strings.Contains(esc.Quadlet, want) {
			t.Errorf("escaping: missing %s in\n%s", want, esc.Quadlet)
		}
	}
	if esc.Settings[0].Key != "API_TOKEN" || esc.Settings[0].Default != catalog.Generate {
		t.Errorf("settings %+v", esc.Settings)
	}
}

// Nothing is written when the file cannot be an entry.
func TestMustFix(t *testing.T) {
	res := run(t, "build", convert.Options{Compose: "testdata/build/input/compose.yaml"})
	if res.Files != nil || len(res.Report.MustFix) == 0 || !strings.Contains(res.Report.MustFix[0], "build:") {
		t.Fatalf("build: %+v", res.Report)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  app:\n    image: example.invalid/unresolvable:1\n"), 0o644)
	if res := run(t, "unresolvable", convert.Options{Compose: dir}); res.Files != nil || !strings.Contains(strings.Join(res.Report.MustFix, " "), "digest") {
		t.Fatalf("unresolvable: %+v", res.Report)
	}
	if _, err := convert.Write(t.TempDir(), res, false); err == nil {
		t.Fatal("wrote a bundle with things to fix")
	}
}
