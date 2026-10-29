package catalog_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
)

func TestRepoCatalogLoadsAndRoundTrips(t *testing.T) {
	idx, err := catalog.LoadDir("../../../catalog/entries")
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Entries) != 12 {
		t.Fatalf("got %d entries", len(idx.Entries))
	}
	raw, _ := json.Marshal(idx)
	back, err := catalog.ParseIndex(raw)
	if err != nil {
		t.Fatal(err)
	}
	ag := back.Find("adguard-home")
	if ag == nil || !ag.Infrastructure || len(ag.HostPorts) != 2 || ag.Web.Port != 3000 {
		t.Fatalf("adguard entry wrong: %+v", ag)
	}
	if jf := back.Find("jellyfin"); jf == nil || jf.Settings[0].Type != "timezone" || jf.Volumes[2].Size != "large" {
		t.Fatalf("jellyfin entry wrong: %+v", jf)
	}
	// An entry of several containers: each with its template, the bundle's
	// own volume unit, and the page served by one of them.
	im := back.Find("immich")
	if im == nil || len(im.Components) != 4 || im.Web.Component != "server" || im.Image != "" || im.Quadlet != "" {
		t.Fatalf("immich entry wrong: %+v", im)
	}
	if !strings.Contains(im.Components[0].Quadlet, "{{ .Volumes.library }}") || len(im.Units) != 1 || im.Units[0].Name != "model-cache.volume" {
		t.Fatalf("immich templates or units wrong: %+v", im)
	}
	spec := catalog.SpecFor(im, "photos", nil, "")
	if spec.GetWebComponent() != "server" || len(spec.GetComponents()) != 4 || spec.GetComponents()[0].GetAfter()[1] != "database" || len(spec.GetUnits()) != 1 {
		t.Fatalf("immich spec wrong: %+v", spec)
	}
	// Its database password is made up at install: letters and digits.
	if pw := spec.GetSettings()["DB_PASSWORD"]; len(pw) != 32 || strings.Trim(pw, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789") != "" {
		t.Fatalf("generated password %q", pw)
	}
	if catalog.SpecFor(im, "", nil, "").GetSettings()["DB_PASSWORD"] == spec.GetSettings()["DB_PASSWORD"] {
		t.Fatal("two installs got the same password")
	}
}

func TestComponentsValidate(t *testing.T) {
	pin := "@sha256:" + repeat("a", 64)
	good := func() catalog.Manifest {
		return catalog.Manifest{ID: "xy", Name: "X", Summary: "s", Exposure: "remote",
			Web: &catalog.Web{Port: 80, Component: "app"},
			Components: []catalog.Component{
				{Name: "app", Image: "docker.io/x/app:1" + pin, After: []string{"db"}, Quadlet: "[Container]\nVolume={{ .Volumes.data }}:/d\n"},
				{Name: "db", Image: "docker.io/x/db:1" + pin, Quadlet: "[Container]\n"},
			},
			Volumes: []catalog.Volume{{Name: "data", Path: "/d"}},
		}
	}
	if m := good(); m.Validate() != nil {
		t.Fatal(m.Validate())
	}
	for name, break_ := range map[string]func(*catalog.Manifest){
		"unpinned component":          func(m *catalog.Manifest) { m.Components[1].Image = "docker.io/x/db:latest" },
		"image beside components":     func(m *catalog.Manifest) { m.Image = "docker.io/x/x:1" + pin },
		"after an unknown component":  func(m *catalog.Manifest) { m.Components[0].After = []string{"cache"} },
		"a ring of waits":             func(m *catalog.Manifest) { m.Components[1].After = []string{"app"} },
		"page from no component":      func(m *catalog.Manifest) { m.Web.Component = "" },
		"two of a name":               func(m *catalog.Manifest) { m.Components[1].Name = "app" },
		"no [Container]":              func(m *catalog.Manifest) { m.Components[1].Quadlet = "Image=x\n" },
		"a dash in a volume name":     func(m *catalog.Manifest) { m.Volumes[0].Name = "my-data" },
		"a bad unit name":             func(m *catalog.Manifest) { m.Units = []catalog.Unit{{Name: "cache.pod"}} },
		"network other than internal": func(m *catalog.Manifest) { m.Network = "host" },
		"a generated non-password": func(m *catalog.Manifest) {
			m.Settings = []catalog.Setting{{Key: "PORT", Type: "number", Default: catalog.Generate}}
		},
	} {
		m := good()
		break_(&m)
		if m.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// A folder's templates are its components', and nothing else.
func TestLoadEntryBundle(t *testing.T) {
	pin := "@sha256:" + repeat("a", 64)
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.yaml", "id: xy\nname: X\nsummary: s\nexposure: private\nnetwork: internal\ncomponents:\n  - name: app\n    image: docker.io/x/app:1"+pin+"\n    egress: true\n  - name: db\n    image: docker.io/x/db:1"+pin+"\n    wait_healthy: true\n")
	write("app.container", "[Container]\nImage={{ .Image }}\n")
	write("db.container", "[Container]\nImage={{ .Image }}\nVolume=data.volume:/var/lib/db\n")
	write("data.volume", "[Volume]\n")
	m, err := catalog.LoadEntry(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Components[1].Quadlet == "" || !m.Components[1].WaitHealthy || !m.Components[0].Egress || m.Units[0].Name != "data.volume" {
		t.Fatalf("loaded %+v", m)
	}
	if spec := catalog.SpecFor(m, "", nil, ""); !spec.GetNetworkInternal() || !spec.GetComponents()[0].GetEgress() {
		t.Fatalf("spec %+v", spec)
	}
	// A template no component names is refused.
	write("cache.container", "[Container]\n")
	if _, err := catalog.LoadEntry(dir); err == nil || !strings.Contains(err.Error(), "cache.container") {
		t.Fatalf("stray template: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	good := catalog.Manifest{ID: "xy", Name: "X", Summary: "s", Image: "docker.io/x/x:1@sha256:" + repeat("a", 64), Exposure: "remote", Quadlet: "[Container]\n"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := good
	bad.Image = "docker.io/x/x:latest"
	if err := bad.Validate(); err == nil {
		t.Fatal("unpinned image accepted")
	}
	bad = good
	bad.Infrastructure = true
	if err := bad.Validate(); err == nil {
		t.Fatal("infrastructure without host ports accepted")
	}
	bad = good
	bad.Volumes = []catalog.Volume{{Name: "data", Path: "/data"}}
	if err := bad.Validate(); err == nil {
		t.Fatal("unused volume accepted")
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// A user's Catalog shows the entries that load and says why the others
// do not.
func TestLoadDirLenient(t *testing.T) {
	dir := t.TempDir()
	pin := "@sha256:" + repeat("a", 64)
	os.MkdirAll(filepath.Join(dir, "good"), 0o755)
	os.WriteFile(filepath.Join(dir, "good", "manifest.yaml"), []byte("id: good\nname: G\nsummary: s\nexposure: private\nimage: docker.io/x/x:1"+pin+"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "good", "good.container"), []byte("[Container]\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "bad"), 0o755)
	os.WriteFile(filepath.Join(dir, "bad", "manifest.yaml"), []byte("id: bad\nname: B\nsummary: s\nexposure: private\nimage: docker.io/x/x:latest\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "bad", "bad.container"), []byte("[Container]\n"), 0o644)
	idx, problems, err := catalog.LoadDirLenient(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Entries) != 1 || idx.Entries[0].ID != "good" || len(problems) != 1 || problems[0].Entry != "bad" || !strings.Contains(problems[0].Error, "digest") {
		t.Fatalf("entries %v problems %v", idx.Entries, problems)
	}
}
