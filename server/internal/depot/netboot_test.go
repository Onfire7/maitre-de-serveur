package depot

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/iana"
)

func TestWriteBootConfig(t *testing.T) {
	l := Layout{Dir: t.TempDir()}
	err := WriteBootConfig(l, BootParams{IP: "192.168.4.54", HTTPPort: 8080, Image: "192.168.4.54:8080/maitre-server:dev", Insecure: "192.168.4.54:8080", Serial: true})
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
	if err := WriteBootConfig(l, BootParams{IP: "10.0.0.1", HTTPPort: 80, Image: "x/y:z"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(l.Boot(), "grub.cfg"))
	if !strings.Contains(string(b), "root=live:http://10.0.0.1:80/boot/rootfs.squashfs") || strings.Contains(string(b), "insecure") || strings.Contains(string(b), "ttyS0") {
		t.Errorf("port 80 config:\n%s", b)
	}
}

func TestWriteBootConfigWritesTheIPXEScript(t *testing.T) {
	l := Layout{Dir: t.TempDir()}
	if err := WriteBootConfig(l, BootParams{IP: "192.168.4.54", HTTPPort: 38273, Image: "192.168.4.54:8080/maitre-server:dev", Insecure: "192.168.4.54:8080"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(l.Boot(), BootIPXE))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.HasPrefix(s, "#!ipxe\n") {
		t.Errorf("boot.ipxe must start with #!ipxe:\n%s", s)
	}
	for _, want := range []string{
		"kernel http://192.168.4.54:38273/boot/vmlinuz root=live:http://192.168.4.54:38273/boot/rootfs.squashfs ",
		" maitre.installer maitre.netboot maitre.image=192.168.4.54:8080/maitre-server:dev maitre.insecure_registry=192.168.4.54:8080\n",
		"initrd http://192.168.4.54:38273/boot/initramfs.img\n",
		"boot\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("boot.ipxe lacks %q:\n%s", want, s)
		}
	}
	g, _ := os.ReadFile(filepath.Join(l.Boot(), BootConfig))
	if !strings.Contains(string(g), "linux /vmlinuz root=live:http://192.168.4.54:38273/boot/rootfs.squashfs ") {
		t.Errorf("grub.cfg lost its kernel line:\n%s", g)
	}
}

func TestManifestListsBIOSAsOptional(t *testing.T) {
	l := Layout{Dir: t.TempDir()}
	if err := os.MkdirAll(l.Boot(), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range BootFiles {
		if err := os.WriteFile(filepath.Join(l.Boot(), f), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteManifest(l, "x/y:z"); err != nil {
		t.Fatal(err)
	}
	m, err := ReadManifest(l)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != len(BootFiles) || m.Optional != nil {
		t.Fatalf("without iPXE: files %v, optional %v", m.Files, m.Optional)
	}
	if err := os.WriteFile(filepath.Join(l.Boot(), BootBIOS), []byte("ipxe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(l, "x/y:z"); err != nil {
		t.Fatal(err)
	}
	m, _ = ReadManifest(l)
	if _, in := m.Files[BootBIOS]; in || len(m.Files) != len(BootFiles) {
		t.Errorf("iPXE must stay out of files, which older Desktops check strictly: %v", m.Files)
	}
	if m.Optional[BootBIOS].Size != 4 {
		t.Errorf("optional: %v", m.Optional)
	}
}

func pxeRequest(t *testing.T, class string, archs []iana.Arch, userClass string) *dhcpv4.DHCPv4 {
	t.Helper()
	mods := []dhcpv4.Modifier{dhcpv4.WithOption(dhcpv4.OptClassIdentifier(class))}
	if archs != nil {
		mods = append(mods, dhcpv4.WithOption(dhcpv4.OptClientArch(archs...)))
	}
	if userClass != "" {
		mods = append(mods, dhcpv4.WithUserClass(userClass, false))
	}
	m, err := dhcpv4.NewDiscovery(net.HardwareAddr{0, 0x1b, 0x21, 1, 2, 3}, mods...)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestReplyByFirmware(t *testing.T) {
	l := Layout{Dir: t.TempDir()}
	if err := os.MkdirAll(l.Boot(), 0o755); err != nil {
		t.Fatal(err)
	}
	n := &Netboot{Layout: l, IP: net.IPv4(192, 168, 4, 54).To4()}
	bios := []iana.Arch{iana.INTEL_X86PC}
	cases := []struct {
		name, class string
		archs       []iana.Arch
		userClass   string
		firmware    string
		file        string
	}{
		{"UEFI", "PXEClient:Arch:00007:UNDI:003016", []iana.Arch{iana.EFI_X86_64}, "", FirmwareUEFI, BootLoader},
		{"BIOS ROM", "PXEClient:Arch:00000:UNDI:002001", bios, "", FirmwareBIOS, BootBIOS},
		{"PXE 2.0 ROM without option 93", "PXEClient:Arch:00000:UNDI:002001", nil, "", FirmwareBIOS, BootBIOS},
		{"iPXE after chaining", "PXEClient:Arch:00000:UNDI:002001", bios, "iPXE", FirmwareBIOS, BootIPXE},
		{"ARM UEFI", "PXEClient:Arch:00011:UNDI:003000", []iana.Arch{iana.EFI_ARM64}, "", "", ""},
		{"not PXE", "MSFT 5.0", nil, "", "", ""},
	}
	run := func(t *testing.T, haveBIOS bool) {
		for _, c := range cases {
			r, fw, err := n.reply(pxeRequest(t, c.class, c.archs, c.userClass))
			if err != nil {
				t.Fatal(err)
			}
			want := c.file
			if !haveBIOS && fw == FirmwareBIOS {
				want = ""
			}
			if c.firmware != "" && fw != c.firmware {
				t.Errorf("%s: firmware %q, want %q", c.name, fw, c.firmware)
			}
			if want == "" {
				if r != nil {
					t.Errorf("%s: answered with %q, want silence", c.name, r.BootFileName)
				}
				continue
			}
			if r == nil {
				t.Errorf("%s: no answer, want %s", c.name, want)
				continue
			}
			if r.BootFileName != want || !r.ServerIPAddr.Equal(n.IP) || !r.YourIPAddr.IsUnspecified() || r.MessageType() != dhcpv4.MessageTypeOffer {
				t.Errorf("%s: file %q server %v yours %v type %v", c.name, r.BootFileName, r.ServerIPAddr, r.YourIPAddr, r.MessageType())
			}
			opt43 := r.Options.Get(dhcpv4.OptionVendorSpecificInformation)
			if (fw == FirmwareBIOS) != bytes.Equal(opt43, pxeBypassDiscovery) {
				t.Errorf("%s: option 43 = %v", c.name, opt43)
			}
		}
	}
	t.Run("bundle without iPXE", func(t *testing.T) { run(t, false) })
	if err := os.WriteFile(filepath.Join(l.Boot(), BootBIOS), []byte("ipxe"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Run("bundle with iPXE", func(t *testing.T) { run(t, true) })
}
