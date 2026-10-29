package catalog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
)

// An icon copied into an entry is recorded with its pack, kept the author's
// comments, and travels in the index as a data URI; the NOTICE names it.
func TestIconCopiedIntoEntry(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "xy")
	os.MkdirAll(dir, 0o755)
	pin := "@sha256:" + strings.Repeat("a", 64)
	os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("id: xy\n# who wrote this, and why\nname: X\nsummary: s\nexposure: private\nimage: docker.io/x/x:1"+pin+"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "xy.container"), []byte("[Container]\nImage={{ .Image }}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "icon.svg"), []byte("<svg/>"), 0o644)
	credit := catalog.IconCredit{Pack: "dashboard-icons", Licence: "Apache-2.0", URL: "https://example.invalid/xy.svg"}
	if err := catalog.SetIcon(dir, "icon.svg", credit); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	if !strings.Contains(string(raw), "# who wrote this, and why\nname: X") || !strings.Contains(string(raw), "icon: icon.svg") || !strings.Contains(string(raw), "  icon:\n    pack: dashboard-icons") {
		t.Fatalf("manifest:\n%s", raw)
	}
	m, err := catalog.LoadEntry(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Icon != "data:image/svg+xml;base64,PHN2Zy8+" {
		t.Fatalf("icon %q", m.Icon)
	}
	if n := catalog.Notice(&catalog.Index{Entries: []catalog.Manifest{*m}}); !strings.Contains(n, "dashboard-icons by Homarr Labs") || !strings.Contains(n, "xy: https://example.invalid/xy.svg") {
		t.Fatalf("notice:\n%s", n)
	}
	// A copied icon must say where it came from.
	os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("id: xy\nname: X\nsummary: s\nexposure: private\nicon: icon.svg\nimage: docker.io/x/x:1"+pin+"\n"), 0o644)
	if _, err := catalog.LoadEntry(dir); err == nil {
		t.Fatal("an icon without its source was accepted")
	}
}
