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
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
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
	// BootBIOS is what legacy BIOS PXE ROMs download: iPXE's UNDI build
	// from the image's ipxe-bootimgs-x86, which drives the NIC through the
	// ROM's own driver, so it works with any PXE 2.x card. GRUB cannot take
	// this role: under proxyDHCP its BIOS build only sees the router's
	// answer and never learns where the Depot is. iPXE asks again with user
	// class "iPXE" and gets BootIPXE, which loads the kernel and initramfs
	// over HTTP (decision 223). Optional: bundles built before it lack it.
	BootBIOS = "undionly.kpxe"
	BootIPXE = "boot.ipxe"
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
	// Optional lists OptionalBootFiles present in the bundle. A separate
	// field, because Desktops before decision 223 refuse a bundle whose
	// files they do not know.
	Optional map[string]ManifestFile `json:"optional,omitempty"`
}

// ManifestFile is one netboot file.
type ManifestFile struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// BootFiles are the files a netboot needs, in download order.
var BootFiles = []string{BootLoader, BootGrub, BootMok, BootKernel, BootInitrd, BootRootFS}

// OptionalBootFiles add abilities when present: BIOS machines.
var OptionalBootFiles = []string{BootBIOS}

// WriteManifest hashes the boot files in l.Boot() and writes BootManifest.
func WriteManifest(l Layout, image string) error {
	m := Manifest{Image: image, Files: map[string]ManifestFile{}}
	for _, name := range BootFiles {
		mf, err := hashFile(filepath.Join(l.Boot(), name))
		if err != nil {
			return err
		}
		m.Files[name] = mf
	}
	for _, name := range OptionalBootFiles {
		mf, err := hashFile(filepath.Join(l.Boot(), name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if m.Optional == nil {
			m.Optional = map[string]ManifestFile{}
		}
		m.Optional[name] = mf
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(l.Boot(), BootManifest), append(b, '\n'), 0o644)
}

func hashFile(p string) (ManifestFile, error) {
	f, err := os.Open(p)
	if err != nil {
		return ManifestFile{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return ManifestFile{}, err
	}
	return ManifestFile{Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
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
rm -f /out/undionly.kpxe
bios=
if [ -f /usr/share/ipxe/undionly.kpxe ]; then cp /usr/share/ipxe/undionly.kpxe /out/undionly.kpxe.tmp; bios=undionly.kpxe
else echo "image lacks ipxe-bootimgs-x86: BIOS machines will not netboot (rebuild it: make image)" >&2; fi
echo "building live initramfs"
dracut --force --quiet --no-hostonly --add "dmsquash-live livenet" --omit "ostree" --kver "$kver" /out/initramfs.img.tmp
echo "squashing root (a few minutes)"
mksquashfs / /out/rootfs.squashfs.tmp -quiet -no-progress -noappend -comp zstd -Xcompression-level 6 \
  -e proc sys dev run tmp out var/tmp var/cache boot/efi etc/hostname etc/hosts etc/resolv.conf
for f in vmlinuz grubx64.efi shimx64.efi mmx64.efi initramfs.img rootfs.squashfs $bios; do mv /out/$f.tmp /out/$f; chmod 644 /out/$f; done
ls -la /out
`
	cmd := exec.CommandContext(ctx, "podman", "run", "--rm", "-v", out+":/out:Z", image, "bash", "-c", script)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build netboot files from %s: %w", image, err)
	}
	return WriteManifest(l, imageRef)
}

// BootFileHandler serves exactly the netboot files (and the manifest) from
// l.Boot() under /boot/: no listings, no other files, no symlinks followed
// outside the directory's known names.
func BootFileHandler(l Layout) http.Handler {
	allowed := map[string]bool{BootManifest: true, BootConfig: true, BootIPXE: true}
	for _, f := range append(BootFiles, OptionalBootFiles...) {
		allowed[f] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/boot/")
		if !strings.HasPrefix(r.URL.Path, "/boot/") || !allowed[name] || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(l.Boot(), name))
	})
}

// HaveBIOS reports whether l.Boot() can boot BIOS machines.
func HaveBIOS(l Layout) bool {
	_, err := os.Stat(filepath.Join(l.Boot(), BootBIOS))
	return err == nil
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

// netbootArgs is the Installer's kernel command line, shared by GRUB (UEFI)
// and iPXE (BIOS).
const netbootArgs = `{{define "args"}}root=live:http://{{.IP}}:{{.HTTPPort}}/boot/rootfs.squashfs rd.live.image rd.live.overlay.overlayfs=1 rd.neednet=1 ip=dhcp rd.live.dir=/ enforcing=0 loglevel=4 console=tty0{{if .Serial}} console=ttyS0,115200n8{{end}} maitre.installer maitre.netboot maitre.image={{.Image}}{{if .Insecure}} maitre.insecure_registry={{.Insecure}}{{end}}{{end}}`

// GRUB loads the kernel and initramfs over TFTP from the server it was
// booted from (its HTTP client only speaks to port 80); dracut then fetches
// the squashed root over HTTP from the Depot's port.
// ASCII only in what GRUB prints: its console font has no "î".
var grubTemplate = template.Must(template.New("grub.cfg").Parse(netbootArgs + `# Written by maitre depot. Boots the Maitre Installer from the network.
set timeout=3
set default=0
menuentry "Install Maitre Server ({{.Image}})" {
  echo "Loading the Maitre Installer from {{.IP}}..."
  linux /vmlinuz {{template "args" .}}
  initrd /initramfs.img
}
`))

// iPXE speaks HTTP to any port, so everything comes from the Depot's HTTP
// server, much faster than TFTP through an old ROM.
var ipxeTemplate = template.Must(template.New("boot.ipxe").Parse(netbootArgs + `#!ipxe
# Written by maitre depot. Boots the Maitre Installer on BIOS machines.
echo Loading the Maitre Installer from {{.IP}}...
kernel http://{{.IP}}:{{.HTTPPort}}/boot/vmlinuz {{template "args" .}}
initrd http://{{.IP}}:{{.HTTPPort}}/boot/initramfs.img
boot
`))

// WriteBootConfig renders grub.cfg and boot.ipxe into l.Boot(). grub.cfg is
// written at the root and under EFI/fedora/, the two places Fedora's GRUB
// looks when netbooted.
// Files are replaced by rename, so a copy left by an elevated run can still
// be swapped out by the directory's owner, and anything written while
// elevated is handed back to the invoking user.
func WriteBootConfig(l Layout, p BootParams) error {
	var grub, ipxe bytes.Buffer
	if err := grubTemplate.Execute(&grub, p); err != nil {
		return err
	}
	if err := ipxeTemplate.Execute(&ipxe, p); err != nil {
		return err
	}
	for rel, buf := range map[string]*bytes.Buffer{
		BootConfig: &grub,
		filepath.Join("EFI", "fedora", BootConfig): &grub,
		BootIPXE: &ipxe,
	} {
		dst := filepath.Join(l.Boot(), rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		OwnByInvoker(filepath.Dir(dst))
		tmp := dst + ".tmp"
		if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
			if errors.Is(err, os.ErrPermission) {
				return fmt.Errorf("%w (left behind by an elevated run? fix with: sudo chown -R $USER %s)", err, l.Boot())
			}
			return err
		}
		OwnByInvoker(tmp)
		if err := os.Rename(tmp, dst); err != nil {
			return err
		}
	}
	return nil
}

// OwnByInvoker gives path to the user who ran sudo or pkexec, when this
// process is root on their behalf, so they keep control of their own files.
func OwnByInvoker(path string) {
	if os.Getuid() != 0 {
		return
	}
	uid, gid := invokerIDs()
	if uid <= 0 {
		return
	}
	_ = os.Lchown(path, uid, gid)
}

func invokerIDs() (uid, gid int) {
	uid, gid = -1, -1
	for _, k := range []string{"SUDO_UID", "PKEXEC_UID"} {
		if v := os.Getenv(k); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				uid = n
				break
			}
		}
	}
	if v := os.Getenv("SUDO_GID"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			gid = n
		}
	}
	if uid >= 0 && gid < 0 {
		if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
			if n, err := strconv.Atoi(u.Gid); err == nil {
				gid = n
			}
		}
	}
	return uid, gid
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
	// BindIP binds TFTP to IP instead of every address.
	BindIP bool
	Log    *slog.Logger
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
	taddr := ":69"
	if n.BindIP && n.IP != nil {
		taddr = n.IP.String() + ":69"
	}
	tconn, err := net.ListenPacket("udp4", taddr)
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

// Firmware kinds a PXE client may have.
const (
	FirmwareUEFI = "uefi"
	FirmwareBIOS = "bios"
)

// clientFirmware tells x86-64 UEFI from legacy BIOS PXE clients, "" for
// anything else (ARM, 32-bit UEFI). PXE 2.x ROMs older than RFC 4578 send
// no architecture option, only "PXEClient:Arch:00000" in their class.
func clientFirmware(m *dhcpv4.DHCPv4) string {
	archs := m.ClientArch()
	if len(archs) == 0 {
		class := string(m.ClassIdentifier())
		if !strings.Contains(class, ":Arch:") || strings.Contains(class, ":Arch:00000") {
			return FirmwareBIOS
		}
		return ""
	}
	for _, a := range archs {
		switch a {
		case iana.EFI_X86_64, iana.EFI_X86_64_HTTP, iana.EFI_BC:
			return FirmwareUEFI
		case iana.INTEL_X86PC:
			return FirmwareBIOS
		}
	}
	return ""
}

// isIPXE reports an iPXE client, which must get the script, not iPXE again.
func isIPXE(m *dhcpv4.DHCPv4) bool {
	for _, c := range m.UserClass() {
		if c == "iPXE" {
			return true
		}
	}
	return false
}

// pxeBypassDiscovery is option 43 telling a BIOS PXE ROM to boot the file
// named in the offer instead of looking for boot servers (PXE spec,
// PXE_DISCOVERY_CONTROL bit 3), as dnsmasq and pixiecore send.
var pxeBypassDiscovery = []byte{6, 1, 8, 255}

// reply builds the proxyDHCP answer to a PXE DISCOVER (port 67) or REQUEST
// (port 4011): no address, only where the boot loader is. UEFI clients get
// shim; BIOS clients get iPXE and then, from iPXE, its script. nil means
// stay quiet; firmware says what asked.
func (n *Netboot) reply(m *dhcpv4.DHCPv4) (reply *dhcpv4.DHCPv4, firmware string, err error) {
	if m == nil || m.OpCode != dhcpv4.OpcodeBootRequest || !strings.HasPrefix(string(m.ClassIdentifier()), "PXEClient") {
		return nil, "", nil
	}
	firmware = clientFirmware(m)
	var file string
	switch {
	case firmware == FirmwareUEFI:
		file = BootLoader
	case firmware == FirmwareBIOS && !HaveBIOS(n.Layout):
		return nil, firmware, nil
	case firmware == FirmwareBIOS && isIPXE(m):
		file = BootIPXE
	case firmware == FirmwareBIOS:
		file = BootBIOS
	default:
		return nil, firmware, nil
	}
	switch m.MessageType() {
	case dhcpv4.MessageTypeDiscover:
		reply, err = dhcpv4.NewReplyFromRequest(m, dhcpv4.WithMessageType(dhcpv4.MessageTypeOffer))
	case dhcpv4.MessageTypeRequest:
		reply, err = dhcpv4.NewReplyFromRequest(m, dhcpv4.WithMessageType(dhcpv4.MessageTypeAck))
	default:
		return nil, firmware, nil
	}
	if err != nil {
		return nil, firmware, err
	}
	reply.ServerIPAddr = n.IP
	reply.BootFileName = file
	reply.UpdateOption(dhcpv4.OptServerIdentifier(n.IP))
	reply.UpdateOption(dhcpv4.OptClassIdentifier("PXEClient"))
	if firmware == FirmwareBIOS {
		reply.UpdateOption(dhcpv4.OptGeneric(dhcpv4.OptionVendorSpecificInformation, pxeBypassDiscovery))
	}
	if id := m.Options.Get(dhcpv4.OptionClientMachineIdentifier); id != nil {
		reply.UpdateOption(dhcpv4.OptGeneric(dhcpv4.OptionClientMachineIdentifier, id))
	}
	return reply, firmware, nil
}

// handleDHCP sends reply's answer. A BIOS client the bundle cannot boot is
// reported as an "unsupported" event so the Desktop can say why nothing
// happens on that machine (its ROM shows PXE-E53).
func (n *Netboot) handleDHCP(conn net.PacketConn, peer net.Addr, m *dhcpv4.DHCPv4) {
	reply, firmware, err := n.reply(m)
	if err != nil {
		n.Log.Error("netboot: build reply", "err", err)
		return
	}
	if reply == nil {
		if m != nil && strings.HasPrefix(string(m.ClassIdentifier()), "PXEClient") && m.MessageType() == dhcpv4.MessageTypeDiscover {
			n.Log.Warn("netboot: cannot boot this PXE client", "mac", m.ClientHWAddr.String(), "firmware", firmware, "arch", m.ClientArch())
			if n.OnEvent != nil {
				n.OnEvent("unsupported", map[string]any{"mac": m.ClientHWAddr.String(), "firmware": firmware})
			}
		}
		return
	}
	dst := peer
	if ua, ok := peer.(*net.UDPAddr); ok && (ua.IP == nil || ua.IP.IsUnspecified()) {
		dst = &net.UDPAddr{IP: net.IPv4bcast, Port: dhcpv4.ClientPort}
	}
	if _, err := conn.WriteTo(reply.ToBytes(), dst); err != nil {
		n.Log.Error("netboot: send reply", "err", err)
		return
	}
	n.Log.Info("netboot: PXE client", "mac", m.ClientHWAddr.String(), "type", m.MessageType().String(), "to", dst.String(), "firmware", firmware, "bootfile", reply.BootFileName)
	if n.OnEvent != nil {
		n.OnEvent("client", map[string]any{"mac": m.ClientHWAddr.String(), "type": m.MessageType().String(), "firmware": firmware})
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
