package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strings"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// Addresses returns the machine's non-loopback unicast IP addresses, IPv4
// first, as strings without zone or prefix.
func Addresses() []string {
	var v4, v6 []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			if ipn.IP.To4() != nil {
				v4 = append(v4, ipn.IP.String())
			} else {
				v6 = append(v6, ipn.IP.String())
			}
		}
	}
	sort.Strings(v4)
	sort.Strings(v6)
	return append(v4, v6...)
}

type lsblkOut struct {
	Blockdevices []lsblkDev `json:"blockdevices"`
}

type lsblkDev struct {
	Name        string     `json:"name"`
	Path        string     `json:"path"`
	Model       *string    `json:"model"`
	Size        uint64     `json:"size"`
	RM          bool       `json:"rm"`
	Type        string     `json:"type"`
	Tran        *string    `json:"tran"`
	Mountpoints []*string  `json:"mountpoints"`
	Children    []lsblkDev `json:"children"`
}

// Disks lists whole disks suitable as install targets, using lsblk. The disk
// holding the running system (anything mounted under / or /sysroot or /boot)
// is flagged as installer media. Virtual devices (zram, loop, ram) are
// skipped.
func Disks(ctx context.Context) ([]*maitrev1.Disk, error) {
	out, err := exec.CommandContext(ctx, "lsblk", "-J", "-b", "-o", "NAME,PATH,MODEL,SIZE,RM,TYPE,TRAN,MOUNTPOINTS").Output()
	if err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	var parsed lsblkOut
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	var disks []*maitrev1.Disk
	for _, d := range parsed.Blockdevices {
		if d.Type != "disk" || strings.HasPrefix(d.Name, "zram") || strings.HasPrefix(d.Name, "loop") || strings.HasPrefix(d.Name, "ram") {
			continue
		}
		disk := &maitrev1.Disk{
			Path:             d.Path,
			SizeBytes:        d.Size,
			Removable:        d.RM,
			IsInstallerMedia: holdsSystem(d),
		}
		if d.Model != nil {
			disk.Model = strings.TrimSpace(*d.Model)
		}
		if disk.Model == "" && d.Tran != nil {
			disk.Model = strings.ToUpper(*d.Tran) + " disk"
		}
		disks = append(disks, disk)
	}
	return disks, nil
}

func holdsSystem(d lsblkDev) bool {
	for _, m := range d.Mountpoints {
		if m == nil {
			continue
		}
		switch *m {
		case "/", "/sysroot", "/boot", "/boot/efi", "/run/media/iso":
			return true
		}
	}
	for _, c := range d.Children {
		if holdsSystem(c) {
			return true
		}
	}
	return false
}

// MachineName is a best-effort label: DMI product name, else hostname.
func MachineName() string {
	for _, f := range []string{"/sys/class/dmi/id/product_name", "/sys/class/dmi/id/board_name"} {
		if b, err := exec.Command("cat", f).Output(); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" && !strings.EqualFold(s, "To be filled by O.E.M.") {
				return s
			}
		}
	}
	return "this machine"
}
