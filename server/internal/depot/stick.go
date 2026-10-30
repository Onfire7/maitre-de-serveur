package depot

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/template"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/partition/gpt"
)

// Decision 221: an installer USB stick made from the netboot files, so a
// Depot's build can go on a stick without a release. The stick is one FAT32
// EFI System Partition: shim as the removable-media loader, GRUB beside it,
// and the kernel, live initramfs and squashed root that netboot serves. It
// boots the same live Installer as netboot, reading its root from the stick
// instead of over HTTP, and installs the image named on its command line.

// StickLabel is the stick's FAT label, which the kernel finds its root by.
// FAT labels are at most 11 characters.
const StickLabel = "MAITRELIVE"

// StickParams name what the stick's Installer installs.
type StickParams struct {
	// Image is the registry reference the Installer will install.
	Image string
	// Insecure is the registry host:port to allow over plain HTTP (a Depot).
	Insecure string
	// Serial adds a serial console (for a VM's serial log).
	Serial bool
}

// The live root comes from the stick by label; the network is brought up in
// the initramfs as for netboot, since the Installer pulls its image.
var stickTemplate = template.Must(template.New("grub.cfg").Parse(`# Written by maitre media build. Boots the Maitre Installer from this stick.
set timeout=3
set default=0
menuentry "Install Maitre Server ({{.Image}})" {
  echo "Loading the Maitre Installer from this stick..."
  search --no-floppy --label --set=root ` + StickLabel + `
  linux /vmlinuz root=live:LABEL=` + StickLabel + ` rd.live.image rd.live.dir=/ rd.live.squashimg=` + BootRootFS + ` rd.live.overlay.overlayfs=1 rd.neednet=1 ip=dhcp enforcing=0 loglevel=4 console=tty0{{if .Serial}} console=ttyS0,115200n8{{end}} maitre.installer maitre.netboot maitre.image={{.Image}}{{if .Insecure}} maitre.insecure_registry={{.Insecure}}{{end}}
  initrd /initramfs.img
}
`))

// StickConfig renders the stick's grub.cfg.
func StickConfig(p StickParams) ([]byte, error) {
	if p.Image == "" {
		return nil, errors.New("no image to install")
	}
	var buf bytes.Buffer
	err := stickTemplate.Execute(&buf, p)
	return buf.Bytes(), err
}

// stickFiles maps each file on the stick to its netboot file. shim is the
// removable-media loader (EFI/BOOT/BOOTX64.EFI) and loads grubx64.efi from
// its own directory; Fedora's GRUB reads grub.cfg from EFI/fedora, so the
// config goes in both.
var stickFiles = []struct{ dst, src string }{
	{"/EFI/BOOT/BOOTX64.EFI", BootLoader},
	{"/EFI/BOOT/grubx64.efi", BootGrub},
	{"/EFI/BOOT/mmx64.efi", BootMok},
	{"/" + BootKernel, BootKernel},
	{"/" + BootInitrd, BootInitrd},
	{"/" + BootRootFS, BootRootFS},
}

const mib = 1 << 20

// BuildStick writes a raw disk image to out from the netboot files in
// l.Boot(). progress, if set, is told the bytes copied so far.
func BuildStick(l Layout, out string, p StickParams, progress func(done, total int64)) error {
	cfg, err := StickConfig(p)
	if err != nil {
		return err
	}
	var total int64
	for _, f := range stickFiles {
		st, err := os.Stat(filepath.Join(l.Boot(), f.src))
		if err != nil {
			return fmt.Errorf("netboot files: %w", err)
		}
		total += st.Size()
	}
	// The files, FAT's tables and some slack, in whole MiB; 1 MiB before the
	// partition and 1 MiB after it for the backup GPT.
	part := (total + total/20 + 64*mib + mib - 1) / mib * mib
	tmp := out + ".part"
	_ = os.Remove(tmp)
	d, err := diskfs.Create(tmp, part+2*mib, diskfs.SectorSizeDefault)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		d.Close()
		os.Remove(tmp)
		return err
	}
	start := uint64(mib / 512)
	table := &gpt.Table{
		LogicalSectorSize:  512,
		PhysicalSectorSize: 512,
		ProtectiveMBR:      true,
		Partitions: []*gpt.Partition{{
			Index: 1, Start: start, End: start + uint64(part/512) - 1,
			Type: gpt.EFISystemPartition, Name: "Maitre Installer",
		}},
	}
	if err := d.Partition(table); err != nil {
		return fail(fmt.Errorf("partition: %w", err))
	}
	fs, err := d.CreateFilesystem(disk.FilesystemSpec{Partition: 1, FSType: filesystem.TypeFat32, VolumeLabel: StickLabel})
	if err != nil {
		return fail(fmt.Errorf("FAT32: %w", err))
	}
	for _, dir := range []string{"/EFI", "/EFI/BOOT", "/EFI/fedora"} {
		if err := fs.Mkdir(dir); err != nil {
			return fail(fmt.Errorf("mkdir %s: %w", dir, err))
		}
	}
	var done int64
	buf := make([]byte, 4*mib)
	for _, f := range stickFiles {
		in, err := os.Open(filepath.Join(l.Boot(), f.src))
		if err != nil {
			return fail(err)
		}
		dst, err := fs.OpenFile(f.dst, os.O_CREATE|os.O_RDWR)
		if err != nil {
			in.Close()
			return fail(fmt.Errorf("%s: %w", f.dst, err))
		}
		for {
			n, rerr := in.Read(buf)
			if n > 0 {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					in.Close()
					return fail(fmt.Errorf("%s: %w", f.dst, werr))
				}
				done += int64(n)
				if progress != nil {
					progress(done, total)
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				in.Close()
				return fail(rerr)
			}
		}
		in.Close()
		dst.Close()
	}
	for _, name := range []string{"/EFI/BOOT/grub.cfg", "/EFI/fedora/grub.cfg", "/grub.cfg"} {
		dst, err := fs.OpenFile(name, os.O_CREATE|os.O_RDWR)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", name, err))
		}
		if _, err := dst.Write(cfg); err != nil {
			return fail(fmt.Errorf("%s: %w", name, err))
		}
		dst.Close()
	}
	if err := d.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, out)
}
