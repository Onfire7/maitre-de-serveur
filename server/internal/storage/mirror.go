package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// A Mirrored Drive (ADR 0009, decision 254) is two whole disks formatted
// together as btrfs RAID1 for data and metadata: one filesystem UUID, so
// one Drive, whose every block is on both disks. It belongs only to
// Mirrored Spaces (decision 255). A failed disk puts it in Attention; the
// agent never mounts it degraded by itself, only for ReplaceMirrorDisk.

// MakeMirrored formats two devices as one RAID1 filesystem and records the
// Drive. confirm is the first device's model (or path).
func (m *Manager) MakeMirrored(ctx context.Context, devices []string, name string, tags []string, confirm string) (*maitrev1.Drive, error) {
	if len(devices) != 2 || devices[0] == devices[1] {
		return nil, errors.New("a Mirrored Drive is made from exactly two disks")
	}
	drives, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	var pair []*maitrev1.Drive
	for _, dev := range devices {
		var d *maitrev1.Drive
		for _, x := range drives {
			if x.GetDevice() == dev {
				d = x
			}
		}
		switch {
		case d == nil:
			return nil, fmt.Errorf("no such disk %s", dev)
		case d.GetSystem():
			return nil, fmt.Errorf("%s holds the running system", dev)
		case d.GetAdopted():
			return nil, fmt.Errorf("%s is a Drive of this Node already (%s); forget it first", dev, d.GetRecord().GetName())
		case d.GetDevice() != d.GetDisk():
			return nil, fmt.Errorf("%s is a partition; a Mirrored Drive takes whole disks", dev)
		}
		pair = append(pair, d)
	}
	if pair[0].GetDisk() == pair[1].GetDisk() {
		return nil, errors.New("both devices are the same disk")
	}
	if strings.TrimSpace(confirm) != ConfirmWord(pair[0]) {
		return nil, fmt.Errorf("to format both, repeat exactly: %s", ConfirmWord(pair[0]))
	}
	if name == "" {
		name = m.SuggestName(ctx)
	}
	if name == "" {
		name = ConfirmWord(pair[0])
	}
	label := "maitre-" + strings.ToLower(strings.NewReplacer(" ", "-", "/", "-").Replace(name))
	if len(label) > 32 {
		label = label[:32]
	}
	for _, dev := range devices {
		if _, err := m.run.Run(ctx, "wipefs", "-a", dev); err != nil {
			return nil, err
		}
	}
	if _, err := m.run.Run(ctx, "mkfs.btrfs", "-f", "-L", label, "-d", "raid1", "-m", "raid1", devices[0], devices[1]); err != nil {
		return nil, err
	}
	_, _ = m.run.Run(ctx, "btrfs", "device", "scan")
	d, err := m.find(ctx, devices[0])
	if err != nil {
		return nil, err
	}
	if d.GetDriveId() == "" {
		return nil, errors.New("formatted, but the new filesystem reports no UUID")
	}
	role := Role{Role: RoleMirrored}
	for _, p := range pair {
		serial := p.GetSerial()
		if serial == "" {
			serial = p.GetDevice()
		}
		role.Disks = append(role.Disks, serial)
	}
	return m.recordAs(ctx, d, name, tags, "formatted", role)
}

var (
	missingRe   = regexp.MustCompile(`devid\s+(\d+)\s.*MISSING`)
	errsRe      = regexp.MustCompile(`\]\.(write_io_errs|read_io_errs|flush_io_errs|corruption_errs|generation_errs)\s+(\d+)`)
	replacingRe = regexp.MustCompile(`([\d.]+)% done`)
)

// MirrorStatuses checks every Mirrored Drive of this Node: a disk gone,
// errors btrfs counted, a replace under way.
func (m *Manager) MirrorStatuses(ctx context.Context) []*maitrev1.MirrorStatus {
	records, err := listLive(ctx, m.st)
	if err != nil {
		return nil
	}
	var out []*maitrev1.MirrorStatus
	for _, r := range records {
		if r.GetNodeId() != m.st.NodeID() || RoleOf(r) != RoleMirrored || r.GetReplacedBy() != "" {
			continue
		}
		st := &maitrev1.MirrorStatus{DriveId: r.GetDriveId(), Replacing: -1}
		dir := filepath.Join(m.Mounts, r.GetDriveId())
		if !mounted(dir) {
			// Not mounted: a disk may be missing (btrfs refuses a degraded
			// mount), or the whole Drive is gone. Ask btrfs about the UUID.
			show, _ := m.run.Run(ctx, "btrfs", "filesystem", "show", r.GetDriveId())
			if strings.Contains(show, "missing") || strings.Contains(show, "MISSING") {
				st.MissingDisk = missingDisk(show, r)
			} else if strings.TrimSpace(show) == "" {
				st.MissingDisk = "both"
			}
			out = append(out, st)
			continue
		}
		if show, err := m.run.Run(ctx, "btrfs", "filesystem", "show", dir); err == nil && (strings.Contains(show, "missing") || strings.Contains(show, "MISSING")) {
			st.MissingDisk = missingDisk(show, r)
		}
		if stats, err := m.run.Run(ctx, "btrfs", "device", "stats", dir); err == nil {
			for _, mm := range errsRe.FindAllStringSubmatch(stats, -1) {
				n, _ := strconv.ParseUint(mm[2], 10, 64)
				st.Errors += n
			}
		}
		if rep, err := m.run.Run(ctx, "btrfs", "replace", "status", "-1", dir); err == nil {
			if mm := replacingRe.FindStringSubmatch(rep); mm != nil && !strings.Contains(rep, "finished") {
				p, _ := strconv.ParseFloat(mm[1], 64)
				st.Replacing = p / 100
			}
		}
		out = append(out, st)
	}
	return out
}

// missingDisk names the disk gone from a Mirrored Drive: the record's
// serial that no present device carries, else btrfs's device id.
func missingDisk(show string, r *maitrev1.DriveRecord) string {
	if mm := missingRe.FindStringSubmatch(show); mm != nil {
		if id, err := strconv.Atoi(mm[1]); err == nil && id >= 1 && id <= len(r.GetDisks()) {
			return r.GetDisks()[id-1]
		}
		return "devid " + mm[1]
	}
	return "a disk"
}

// ReplaceMirrorDisk puts device in a Mirrored Drive's missing disk's
// place with btrfs replace, mounting the Drive degraded first if it is
// not mounted (the one time the agent does that, on the user's word).
// confirm is the new disk's model (or path).
func (m *Manager) ReplaceMirrorDisk(ctx context.Context, driveID, device, confirm string) (*maitrev1.Drive, error) {
	var rec maitrev1.DriveRecord
	if _, err := m.st.Get(ctx, KindDrive, driveID, &rec); err != nil {
		return nil, fmt.Errorf("no Drive %s", driveID)
	}
	if RoleOf(&rec) != RoleMirrored {
		return nil, fmt.Errorf("%s is not a Mirrored Drive", rec.GetName())
	}
	d, err := m.find(ctx, device)
	if err != nil {
		return nil, err
	}
	if d.GetSystem() || d.GetAdopted() {
		return nil, fmt.Errorf("%s is in use", device)
	}
	if strings.TrimSpace(confirm) != ConfirmWord(d) {
		return nil, fmt.Errorf("to use it, repeat exactly: %s", ConfirmWord(d))
	}
	dir := filepath.Join(m.Mounts, driveID)
	if !mounted(dir) {
		if _, err := m.run.Run(ctx, "mount", "-t", "btrfs", "-o", "noatime,degraded", "UUID="+driveID, dir); err != nil {
			return nil, fmt.Errorf("mount %s degraded: %w", rec.GetName(), err)
		}
	}
	show, _ := m.run.Run(ctx, "btrfs", "filesystem", "show", dir)
	mm := missingRe.FindStringSubmatch(show)
	if mm == nil {
		return nil, fmt.Errorf("%s has both its disks; nothing to replace", rec.GetName())
	}
	if _, err := m.run.Run(ctx, "btrfs", "replace", "start", "-f", mm[1], device, dir); err != nil {
		return nil, err
	}
	serial := d.GetSerial()
	if serial == "" {
		serial = device
	}
	if id, err := strconv.Atoi(mm[1]); err == nil && id >= 1 && id <= len(rec.Disks) {
		rec.Disks[id-1] = serial
	} else {
		rec.Disks = append(rec.Disks, serial)
	}
	if _, err := m.st.Put(ctx, KindDrive, driveID, &rec, false); err != nil {
		return nil, err
	}
	if m.OnChange != nil {
		m.OnChange()
	}
	drives, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, x := range drives {
		if x.GetDriveId() == driveID {
			return x, nil
		}
	}
	return nil, nil
}
