package depot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
	"github.com/insomniacslk/dhcp/iana"
	"github.com/pin/tftp/v3"
)

// Netboot files produced by BuildBootFiles and served by TFTP and HTTP.
const (
	// BootLoader is what the firmware downloads: the image's shim, signed by
	// Microsoft, so Secure Boot may stay on (decision 79 revised). shim then
	// loads grubx64.efi from the same TFTP directory.
	BootLoader = "shimx64.efi"
	BootGrub   = "grubx64.efi"
	BootMok    = "mmx64.efi" // shim's MOK manager, in case the firmware asks for it
	BootKernel = "vmlinuz"
	BootInitrd = "initramfs.img" // dracut with dmsquash-live and livenet
	BootRootFS = "rootfs.squashfs"
	BootConfig = "grub.cfg"
	// BootManifest lists the files with digests, for downloads to verify.
	BootManifest = "netboot.json"
)

// Manifest is BootManifest's content.
type Manifest struct {
	// Image the files were built from, as the Installer should pull it
	// (empty for a local build; the Depot's registry supplies it then).
	Image  string                  `json:"image"`
	Kernel string                  `json:"kernel,omitempty"`
	Files  map[string]ManifestFile `json:"files"`
}

// ManifestFile is one netboot file.
type ManifestFile struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// BootFiles are the files a netboot needs, in download order.
var BootFiles = []string{BootLoader, BootGrub, BootMok, BootKernel, BootInitrd, BootRootFS}

// WriteManifest hashes the boot files in l.Boot() and writes BootManifest.
func WriteManifest(l Layout, image string) error {
	m := Manifest{Image: image, Files: map[string]ManifestFile{}}
	for _, name := range BootFiles {
		f, err := os.Open(filepath.Join(l.Boot(), name))
		if err != nil {
			return err
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		m.Files[name] = ManifestFile{Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(l.Boot(), BootManifest), append(b, '\n'), 0o644)
}

// ReadManifest reads BootManifest from l.Boot().
func ReadManifest(l Layout) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(l.Boot(), BootManifest))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// BuildBootFiles turns a local image into netboot files under l.Boot():
// kernel, a live initramfs, the squashed root, and GRUB. Everything runs
// inside the image itself with podman, so the workstation needs no dracut or
// squashfs-tools. The image must contain dracut-live. podman's bind-mounted
// /etc/hostname, hosts and resolv.conf are left out. A rootless build cannot
// record SELinux labels, so the live system boots permissive (enforcing=0);
// bootc labels the installed disk from the image's own policy regardless.
// imageRef is recorded in the manifest as the reference the Installer
// should pull; empty for a local build served by a Depot.
func BuildBootFiles(ctx context.Context, l Layout, image, imageRef string, stdout, stderr io.Writer) error {
	out := l.Boot()
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	script := `set -euo pipefail
kver=$(ls /usr/lib/modules | head -1)
test -d /usr/lib/dracut/modules.d/90dmsquash-live || { echo "image lacks dracut-live; rebuild it (make image)" >&2; exit 2; }
echo "kernel $kver"
cp /usr/lib/modules/$kver/vmlinuz /out/vmlinuz.tmp
cp /usr/lib/bootupd/updates/EFI/fedora/grubx64.efi /out/grubx64.efi.tmp
cp /usr/lib/bootupd/updates/EFI/fedora/shimx64.efi /out/shimx64.efi.tmp
cp /usr/lib/bootupd/updates/EFI/fedora/mmx64.efi /out/mmx64.efi.tmp
echo "building live initramfs"
dracut --force --quiet --no-hostonly --add "dmsquash-live livenet" --omit "ostree" --kver "$kver" /out/initramfs.img.tmp
echo "squashing root (a few minutes)"
mksquashfs / /out/rootfs.squashfs.tmp -quiet -no-progress -noappend -comp zstd -Xcompression-level 6 \
  -e proc sys dev run tmp out var/tmp var/cache boot/efi etc/hostname etc/hosts etc/resolv.conf
for f in vmlinuz grubx64.efi shimx64.efi mmx64.efi initramfs.img rootfs.squashfs; do mv /out/$f.tmp /out/$f; chmod 644 /out/$f; done
ls -la /out
`
	cmd := exec.CommandContext(ctx, "podman", "run", "--rm", "-v", out+":/out:Z", image, "bash", "-c", script)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build netboot files from %s: %w", image, err)
	}
	return WriteManifest(l, imageRef)
}

// HaveBootFiles reports whether l.Boot() holds a complete set.
func HaveBootFiles(l Layout) bool {
	for _, f := range []string{BootLoader, BootGrub, BootKernel, BootInitrd, BootRootFS} {
		if _, err := os.Stat(filepath.Join(l.Boot(), f)); err != nil {
			return false
		}
	}
	return true
}

// BootParams fill the GRUB config.
type BootParams struct {
	// IP and HTTPPort locate the Depot's HTTP server for GRUB and dracut.
	IP       string
	HTTPPort int
	// Image is the registry reference the Installer will install, e.g.
	// 192.168.4.54:8080/maitre-server:dev.
	Image string
	// Insecure is the registry host:port to allow over plain HTTP.
	Insecure string
	// Serial adds a serial console (QEMU's serial log); real machines
	// without a UART must not get one, agetty crashes on it.
	Serial bool
}

// GRUB loads the kernel and initramfs over TFTP from the server it was
// booted from (its HTTP client only speaks to port 80); dracut then fetches
// the squashed root over HTTP from the Depot's port.
var grubTemplate = template.Must(template.New("grub.cfg").Parse(`# Written by maitre depot. Boots the Maître Installer from the network.
set timeout=3
set default=0
menuentry "Install Maître Server ({{.Image}})" {
  echo "Loading the Maître Installer from {{.IP}}..."
  linux /vmlinuz root=live:http://{{.IP}}:{{.HTTPPort}}/boot/rootfs.squashfs rd.live.image rd.live.overlay.overlayfs=1 rd.neednet=1 ip=dhcp rd.live.dir=/ enforcing=0 loglevel=4 console=tty0{{if .Serial}} console=ttyS0,115200n8{{end}} maitre.installer maitre.netboot maitre.image={{.Image}}{{if .Insecure}} maitre.insecure_registry={{.Insecure}}{{end}}
  initrd /initramfs.img
}
`))

// WriteGrubConfig renders grub.cfg into l.Boot(). It is written at the root
// and under EFI/fedora/, the two places Fedora's GRUB looks when netbooted.
func WriteGrubConfig(l Layout, p BootParams) error {
	var buf bytes.Buffer
	if err := grubTemplate.Execute(&buf, p); err != nil {
		return err
	}
	for _, rel := range []string{BootConfig, filepath.Join("EFI", "fedora", BootConfig)} {
		dst := filepath.Join(l.Boot(), rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, buf.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Netboot answers PXE clients: proxyDHCP on 67 and 4011 (the LAN's own DHCP
// server keeps handing out addresses), TFTP on 69 for shim, GRUB and its
// config.
// Needs root or CAP_NET_BIND_SERVICE (decision 83).
type Netboot struct {
	Layout Layout
	// IP is the Depot's address on the interface clients boot from.
	IP net.IP
	// Interface limits DHCP to one NIC; empty means all.
	Interface string
	Log       *slog.Logger
	// OnEvent, when set, receives every client interaction: kind is
	// "client" (fields mac, type) or "tftp" (fields file). Used by the
	// Desktop's helper to show activity and to decide when it is idle.
	OnEvent func(kind string, fields map[string]any)

	servers []*server4.Server
	tftp    *tftp.Server
}

// Start binds the sockets and serves until ctx ends.
func (n *Netboot) Start(ctx context.Context) error {
	if n.Log == nil {
		n.Log = slog.Default()
	}
	for _, port := range []int{dhcpv4.ServerPort, 4011} {
		srv, err := server4.NewServer(n.Interface, &net.UDPAddr{IP: net.IPv4zero, Port: port}, n.handleDHCP)
		if err != nil {
			n.Close()
			return fmt.Errorf("proxyDHCP on udp/%d: %w (run with sudo?)", port, err)
		}
		n.servers = append(n.servers, srv)
		go func() { _ = srv.Serve() }()
	}
	n.tftp = tftp.NewServer(n.readTFTP, nil)
	n.tftp.SetTimeout(5 * time.Second)
	n.tftp.SetBlockSize(1428)
	tconn, err := net.ListenPacket("udp4", ":69")
	if err != nil {
		n.Close()
		return fmt.Errorf("tftp on udp/69: %w (run with sudo?)", err)
	}
	go func() { _ = n.tftp.Serve(tconn.(*net.UDPConn)) }()
	go func() {
		<-ctx.Done()
		n.Close()
	}()
	return nil
}

// Close stops serving.
func (n *Netboot) Close() {
	for _, s := range n.servers {
		_ = s.Close()
	}
	n.servers = nil
	if n.tftp != nil {
		n.tftp.Shutdown()
		n.tftp = nil
	}
}

// handleDHCP answers PXE DISCOVER (port 67) and REQUEST (port 4011) from
// x86-64 UEFI clients with the boot file, nothing else. The reply carries no
// address: it only tells the firmware where GRUB is.
func (n *Netboot) handleDHCP(conn net.PacketConn, peer net.Addr, m *dhcpv4.DHCPv4) {
	if m == nil || m.OpCode != dhcpv4.OpcodeBootRequest {
		return
	}
	class := string(m.ClassIdentifier())
	if !strings.HasPrefix(class, "PXEClient") {
		return
	}
	archs := m.ClientArch()
	efi := false
	for _, a := range archs {
		if a == iana.EFI_X86_64 || a == iana.EFI_X86_64_HTTP || a == iana.EFI_BC {
			efi = true
		}
	}
	if !efi {
		n.Log.Warn("netboot: ignoring non-UEFI-x64 PXE client", "mac", m.ClientHWAddr.String(), "arch", archs)
		return
	}
	var reply *dhcpv4.DHCPv4
	var err error
	switch m.MessageType() {
	case dhcpv4.MessageTypeDiscover:
		reply, err = dhcpv4.NewReplyFromRequest(m, dhcpv4.WithMessageType(dhcpv4.MessageTypeOffer))
	case dhcpv4.MessageTypeRequest:
		reply, err = dhcpv4.NewReplyFromRequest(m, dhcpv4.WithMessageType(dhcpv4.MessageTypeAck))
	default:
		return
	}
	if err != nil {
		n.Log.Error("netboot: build reply", "err", err)
		return
	}
	reply.ServerIPAddr = n.IP
	reply.BootFileName = BootLoader
	reply.UpdateOption(dhcpv4.OptServerIdentifier(n.IP))
	reply.UpdateOption(dhcpv4.OptClassIdentifier("PXEClient"))
	if id := m.Options.Get(dhcpv4.OptionClientMachineIdentifier); id != nil {
		reply.UpdateOption(dhcpv4.OptGeneric(dhcpv4.OptionClientMachineIdentifier, id))
	}
	dst := peer
	if ua, ok := peer.(*net.UDPAddr); ok && (ua.IP == nil || ua.IP.IsUnspecified()) {
		dst = &net.UDPAddr{IP: net.IPv4bcast, Port: dhcpv4.ClientPort}
	}
	if _, err := conn.WriteTo(reply.ToBytes(), dst); err != nil {
		n.Log.Error("netboot: send reply", "err", err)
		return
	}
	n.Log.Info("netboot: PXE client", "mac", m.ClientHWAddr.String(), "type", m.MessageType().String(), "to", dst.String(), "bootfile", BootLoader)
	if n.OnEvent != nil {
		n.OnEvent("client", map[string]any{"mac": m.ClientHWAddr.String(), "type": m.MessageType().String()})
	}
}

// readTFTP serves files from the boot directory. Any request whose name ends
// in grub.cfg (Fedora's GRUB tries several paths and MAC/IP variants) gets
// the generated config.
func (n *Netboot) readTFTP(filename string, rf io.ReaderFrom) error {
	clean := path.Clean("/" + strings.ReplaceAll(filename, "\\", "/"))
	if strings.HasPrefix(path.Base(clean), BootConfig) {
		clean = "/" + BootConfig
	}
	f, err := os.Open(filepath.Join(n.Layout.Boot(), filepath.FromSlash(clean)))
	if err != nil {
		n.Log.Warn("netboot: tftp miss", "file", filename)
		return err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil {
		if t, ok := rf.(tftp.OutgoingTransfer); ok {
			t.SetSize(st.Size())
		}
	}
	_, err = rf.ReadFrom(f)
	if err == nil {
		n.Log.Info("netboot: tftp", "file", clean)
		if n.OnEvent != nil {
			n.OnEvent("tftp", map[string]any{"file": clean})
		}
	}
	return err
}

// InterfaceFor returns the name of the interface holding ip.
func InterfaceFor(ip string) (string, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, i := range ifs {
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.String() == ip {
				return i.Name, nil
			}
		}
	}
	return "", errors.New("no interface has " + ip)
}
