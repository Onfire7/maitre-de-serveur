package catalogcheck_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
	"github.com/Onfire7/maitre-de-serveur/server/internal/catalogcheck"
)

const image = "docker.io/x/app:1@sha256:0000000000000000000000000000000000000000000000000000000000000000"

func entry(t *testing.T, grants, tmpl string) *catalog.Manifest {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "id: app\nname: App\nsummary: An app.\nimage: " + image + "\nexposure: private\nweb:\n  port: 8123\n" + grants
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.container"), []byte("[Container]\nImage={{ .Image }}\n"+tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := catalog.LoadEntry(dir)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func problems(t *testing.T, e *catalog.Manifest) string {
	t.Helper()
	res, err := catalogcheck.Check(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(res.Problems, "\n")
}

func TestDeclaredGrants(t *testing.T) {
	ok := entry(t, `grants:
  - option: Network=host
    why: Finds devices on the network.
    optional: discovery
    without: Add devices by address.
  - option: AddCapability=NET_RAW
    why: Pings devices.
`, "{{ if .Grants.discovery }}Network=host\n{{ else }}Network={{ .Network }}\n{{ end }}AddCapability=NET_RAW\n")
	if p := problems(t, ok); p != "" {
		t.Errorf("a well declared entry fails:\n%s", p)
	}

	undeclared := entry(t, "", "Network={{ .Network }}\nAddCapability=NET_ADMIN\nPodmanArgs=--privileged\n")
	p := problems(t, undeclared)
	for _, want := range []string{
		"needs AddCapability=NET_ADMIN but does not declare it",
		"needs PodmanArgs=--privileged but does not declare it; add it under grants: with why, once Maître supports it: that needs nothing in Maître can check raw Podman arguments",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("missing %q in:\n%s", want, p)
		}
	}

	unwrapped := entry(t, `grants:
  - option: Network=host
    why: Finds devices.
    optional: discovery
    without: Less.
`, "{{ if .Grants.discovery }}{{ end }}Network=host\n")
	if p := problems(t, unwrapped); !strings.Contains(p, "Network=host is optional (discovery) but stays in the unit with discovery off") {
		t.Errorf("unwrapped optional grant:\n%s", p)
	}

	unused := entry(t, `grants:
  - option: AddDevice=*
    why: A radio.
  - option: PodmanArgs=--privileged
    why: Everything.
`, "Network={{ .Network }}\nPodmanArgs=--privileged\n")
	p = problems(t, unused)
	for _, want := range []string{"declares AddDevice=*, which no unit uses", "PodmanArgs=--privileged is not supported yet: Maître needs"} {
		if !strings.Contains(p, want) {
			t.Errorf("missing %q in:\n%s", want, p)
		}
	}
}

// A feature declared but read by no template, or an optional grant that
// does not say what is lost, is refused when the entry loads.
func TestGrantDeclarations(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "app")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "app.container"), []byte("[Container]\nImage={{ .Image }}\n"), 0o644)
	for grants, want := range map[string]string{
		"grants:\n  - option: Network=host\n    optional: discovery\n    without: x\n":             "say why",
		"grants:\n  - option: Network=host\n    why: y\n    optional: discovery\n":                 "say what the person loses",
		"grants:\n  - option: Network=host\n    why: y\n    optional: discovery\n    without: x\n": "not used by any template",
	} {
		os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("id: app\nname: App\nsummary: An app.\nimage: "+image+"\nexposure: private\n"+grants), 0o644)
		if _, err := catalog.LoadEntry(dir); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", grants, err, want)
		}
	}
}

// The Catalog's own entries pass as they are.
func TestRepoCatalog(t *testing.T) {
	idx, err := catalog.LoadDir("../../../catalog/entries")
	if err != nil {
		t.Fatal(err)
	}
	for i := range idx.Entries {
		if p := problems(t, &idx.Entries[i]); p != "" {
			t.Errorf("%s:\n%s", idx.Entries[i].ID, p)
		}
	}
}
