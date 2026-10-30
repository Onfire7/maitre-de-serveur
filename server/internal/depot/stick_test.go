package depot

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	diskfs "github.com/diskfs/go-diskfs"
)

func TestStickHoldsTheBootFilesAndItsImage(t *testing.T) {
	l := Layout{Dir: t.TempDir()}
	if err := os.MkdirAll(l.Boot(), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range BootFiles {
		if err := os.WriteFile(filepath.Join(l.Boot(), f), []byte("contents of "+f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "stick.img")
	var last int64
	if err := BuildStick(l, out, StickParams{Image: "10.0.0.2:8080/maitre-server:dev", Insecure: "10.0.0.2:8080"}, func(done, _ int64) { last = done }); err != nil {
		t.Fatal(err)
	}
	if last == 0 {
		t.Error("no progress reported")
	}
	d, err := diskfs.Open(out, diskfs.WithOpenMode(diskfs.ReadOnly))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	fs, err := d.GetFilesystem(1)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(fs.Label()); got != StickLabel {
		t.Errorf("label %q, want %q", got, StickLabel)
	}
	read := func(name string) string {
		f, err := fs.OpenFile(name, os.O_RDONLY)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := io.ReadAll(f)
		return string(b)
	}
	if got := read("/EFI/BOOT/BOOTX64.EFI"); got != "contents of "+BootLoader {
		t.Errorf("loader is %q, want shim", got)
	}
	if got := read("/" + BootRootFS); got != "contents of "+BootRootFS {
		t.Errorf("root is %q", got)
	}
	cfg := read("/EFI/fedora/grub.cfg")
	for _, want := range []string{"root=live:LABEL=" + StickLabel, "maitre.image=10.0.0.2:8080/maitre-server:dev", "maitre.insecure_registry=10.0.0.2:8080", "maitre.installer"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("grub.cfg lacks %q:\n%s", want, cfg)
		}
	}
	if _, err := os.Stat(out + ".part"); !os.IsNotExist(err) {
		t.Error("left its temporary file behind")
	}
}

func TestStickNeedsAnImage(t *testing.T) {
	if err := BuildStick(Layout{Dir: t.TempDir()}, filepath.Join(t.TempDir(), "x.img"), StickParams{}, nil); err == nil {
		t.Error("built a stick that installs nothing")
	}
}
