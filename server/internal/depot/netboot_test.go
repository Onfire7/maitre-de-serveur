package depot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteGrubConfig(t *testing.T) {
	l := Layout{Dir: t.TempDir()}
	err := WriteGrubConfig(l, BootParams{IP: "192.168.4.54", HTTPPort: 8080, Image: "192.168.4.54:8080/maitre-server:dev", Insecure: "192.168.4.54:8080", Serial: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"grub.cfg", "EFI/fedora/grub.cfg"} {
		b, err := os.ReadFile(filepath.Join(l.Boot(), rel))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, want := range []string{
			"linux /vmlinuz root=",
			"root=live:http://192.168.4.54:8080/boot/rootfs.squashfs",
			"maitre.installer maitre.netboot maitre.image=192.168.4.54:8080/maitre-server:dev maitre.insecure_registry=192.168.4.54:8080",
			"initrd /initramfs.img",
			"console=tty0 console=ttyS0,115200n8",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("%s lacks %q:\n%s", rel, want, s)
			}
		}
	}
	if err := WriteGrubConfig(l, BootParams{IP: "10.0.0.1", HTTPPort: 80, Image: "x/y:z"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(l.Boot(), "grub.cfg"))
	if !strings.Contains(string(b), "root=live:http://10.0.0.1:80/boot/rootfs.squashfs") || strings.Contains(string(b), "insecure") || strings.Contains(string(b), "ttyS0") {
		t.Errorf("port 80 config:\n%s", b)
	}
}
