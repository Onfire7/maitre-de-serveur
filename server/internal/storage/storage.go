// Package storage is a Node's view of its block devices (Milestone 4):
// detection, Adopt, Format, and the Drive records that pool-up turns into
// Pools (ADR 0004). Filesystem UUIDs identify Drives.
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/pool"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// KindDrive is the Record kind for DriveRecord bodies.
const KindDrive = "drive"

// Runner executes commands; swapped for a fake in tests.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// ExecRunner runs real commands.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Manager answers StorageService for one Node.
type Manager struct {
	st  *store.Store
	run Runner
	// Mounts is where Drives are mounted: Mounts/<drive_id>.
	Mounts string
	// OnChange runs after a Drive record changed (pool reassembly, sync).
	OnChange func()
}

// New builds a Manager. A nil Runner means real commands.
func New(st *store.Store, run Runner) *Manager {
	if run == nil {
		run = ExecRunner{}
	}
	return &Manager{st: st, run: run, Mounts: pool.DrivesDir}
}

type lsblkDev struct {
	Name        string     `json:"name"`
	Path        string     `json:"path"`
	Model       *string    `json:"model"`
	Serial      *string    `json:"serial"`
	Size        uint64     `json:"size"`
	RM          bool       `json:"rm"`
	Rota        bool       `json:"rota"`
	Type        string     `json:"type"`
	Tran        *string    `json:"tran"`
	FSType      *string    `json:"fstype"`
	UUID        *string    `json:"uuid"`
	Label       *string    `json:"label"`
	Mountpoints []*string  `json:"mountpoints"`
	Children    []lsblkDev `json:"children"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// Filesystems a Node can use as a Drive (Adopt keeps them; Format makes btrfs).
var adoptable = map[string]bool{"ext4": true, "ext3": true, "ext2": true, "btrfs": true, "xfs": true, "ntfs": true, "exfat": true, "vfat": false}

// List detects block devices and merges the Drive records.
func (m *Manager) List(ctx context.Context) ([]*maitrev1.Drive, error) {
	out, err := m.run.Run(ctx, "lsblk", "-J", "-b", "-o", "NAME,PATH,MODEL,SERIAL,SIZE,RM,ROTA,TYPE,TRAN,FSTYPE,UUID,LABEL,MOUNTPOINTS")
	if err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	var parsed struct {
		Blockdevices []lsblkDev `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	records, _ := store.ListLive(ctx, m.st, KindDrive, func() *maitrev1.DriveRecord { return &maitrev1.DriveRecord{} })
	byID := map[string]*maitrev1.DriveRecord{}
	for _, r := range records {
		if r.GetNodeId() == m.st.NodeID() {
			byID[r.GetDriveId()] = r
		}
	}
	suggested := m.SuggestName(ctx)
	spaces, _ := Spaces(ctx, m.st)
	var drives []*maitrev1.Drive
	seen := map[string]bool{}
	for _, d := range parsed.Blockdevices {
		if d.Type != "disk" || strings.HasPrefix(d.Name, "zram") || strings.HasPrefix(d.Name, "loop") || strings.HasPrefix(d.Name, "ram") {
			continue
		}
		system := holdsSystem(d)
		// A whole disk with a filesystem, or each partition with one.
		candidates := []lsblkDev{}
		if str(d.FSType) != "" {
			candidates = append(candidates, d)
		}
		for _, c := range d.Children {
			if str(c.FSType) != "" {
				candidates = append(candidates, c)
			}
		}
		if len(candidates) == 0 {
			candidates = append(candidates, d) // blank disk: Format only
		}
		for _, c := range candidates {
			dr := &maitrev1.Drive{
				Device:     c.Path,
				DriveId:    str(c.UUID),
				Model:      str(d.Model),
				Serial:     str(d.Serial),
				SizeBytes:  c.Size,
				Removable:  d.RM,
				Rotational: d.Rota,
				Transport:  str(d.Tran),
				Fstype:     str(c.FSType),
				Label:      str(c.Label),
				System:     isSystemPartition(c) || (c.Path == d.Path && system),
				AutoTags:   autoTags(d, c.Size, str(c.Label)),
				Disk:       d.Path,
			}
			if dr.Model == "" {
				dr.Model = strings.ToUpper(dr.Transport) + " disk"
			}
			// A Mirrored Drive's two disks carry one UUID: one Drive.
			if dr.DriveId != "" && seen[dr.DriveId] {
				continue
			}
			seen[dr.DriveId] = dr.DriveId != ""
			if rec, ok := byID[dr.DriveId]; ok && dr.DriveId != "" {
				dr.Record = rec
				dr.Adopted = true
				dr.MountPath = filepath.Join(m.Mounts, dr.DriveId)
				dr.FreeBytes = freeBytes(dr.MountPath)
				dr.Spaces = SpacesOf(rec, spaces)
				if mounted(dr.MountPath) {
					dr.Folders = Folders(dr.MountPath, spaces, dr.Spaces)
				}
			} else if !dr.System {
				dr.SuggestedName = suggested
			}
			drives = append(drives, dr)
		}
	}
	sort.Slice(drives, func(i, j int) bool { return drives[i].Device < drives[j].Device })
	return drives, nil
}

func isSystemPartition(c lsblkDev) bool {
	switch str(c.Label) {
	case "maitre-config", "maitre-image", "boot", "EFI-SYSTEM", "root":
		return true
	}
	for _, mp := range c.Mountpoints {
		if mp != nil && (*mp == "/" || *mp == "/sysroot" || *mp == "/boot" || *mp == "/boot/efi" || *mp == "/var/lib/maitre") {
			return true
		}
	}
	return false
}

func holdsSystem(d lsblkDev) bool {
	if isSystemPartition(d) {
		return true
	}
	for _, c := range d.Children {
		if isSystemPartition(c) {
			return true
		}
	}
	return false
}

// autoTags derives the automatic tags: medium, bus, a size class, and
// "internal" for the data partition the Installer made on the system disk.
func autoTags(d lsblkDev, size uint64, label string) []string {
	var tags []string
	if label == pool.DataLabel {
		tags = append(tags, "internal")
	}
	if d.Rota {
		tags = append(tags, "hdd")
	} else {
		tags = append(tags, "ssd")
	}
	switch str(d.Tran) {
	case "usb":
		tags = append(tags, "usb")
	case "nvme":
		tags = append(tags, "nvme")
	}
	switch {
	case size >= 4<<40:
		tags = append(tags, "large")
	case size >= 1<<40:
		tags = append(tags, "medium")
	default:
		tags = append(tags, "small")
	}
	return tags
}

func (m *Manager) find(ctx context.Context, device string) (*maitrev1.Drive, error) {
	drives, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, d := range drives {
		if d.GetDevice() == device {
			return d, nil
		}
	}
	return nil, fmt.Errorf("no such device %s", device)
}

// Contents mounts device read-only and lists its top-level entries.
func (m *Manager) Contents(ctx context.Context, device string) (*maitrev1.DriveContentsResponse, error) {
	d, err := m.find(ctx, device)
	if err != nil {
		return nil, err
	}
	if d.GetFstype() == "" {
		return &maitrev1.DriveContentsResponse{Empty: true}, nil
	}
	mnt, err := os.MkdirTemp("", "maitre-peek-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(mnt)
	if _, err := m.run.Run(ctx, "mount", "-o", "ro", device, mnt); err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", device, err)
	}
	defer m.run.Run(context.Background(), "umount", mnt)
	entries, _ := os.ReadDir(mnt)
	res := &maitrev1.DriveContentsResponse{Empty: len(entries) == 0}
	res.UsedBytes = usedBytes(mnt)
	for i, e := range entries {
		if i >= 50 {
			res.Entries = append(res.Entries, fmt.Sprintf("… and %d more", len(entries)-50))
			break
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		res.Entries = append(res.Entries, name)
	}
	return res, nil
}

// OriginInternal marks the Drive the Installer made from the rest of the
// system disk (label maitre-data). It is adopted by itself and cannot be
// removed: it is the Node's built-in storage.
const OriginInternal = "internal"

// AdoptInternal records the Installer's data partition as a Drive of this
// Node if it is not one yet, so it shows in the Console, takes a name and
// tags, and Tier rules can select it. Idempotent; returns whether a record
// was written.
func (m *Manager) AdoptInternal(ctx context.Context) (bool, error) {
	drives, err := m.List(ctx)
	if err != nil {
		return false, err
	}
	for _, d := range drives {
		if d.GetLabel() != pool.DataLabel || d.GetAdopted() || d.GetDriveId() == "" || d.GetFstype() == "" {
			continue
		}
		name := m.SuggestName(ctx)
		if name == "" {
			name = "Internal"
		}
		rec := &maitrev1.DriveRecord{
			DriveId: d.GetDriveId(), NodeId: m.st.NodeID(), Name: name, Model: d.GetModel(), Serial: d.GetSerial(),
			SizeBytes: d.GetSizeBytes(), Fstype: d.GetFstype(), AutoTags: d.GetAutoTags(), Origin: OriginInternal, AddedUnixMs: time.Now().UnixMilli(),
		}
		if err := m.Mount(ctx, rec); err != nil {
			return false, err
		}
		if _, err := m.st.Put(ctx, KindDrive, rec.DriveId, rec, false); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// Adopt mounts an existing filesystem and records the Drive.
func (m *Manager) Adopt(ctx context.Context, device, name string, tags []string) (*maitrev1.Drive, error) {
	return m.AdoptAs(ctx, device, name, tags, Role{})
}

// AdoptAs is Adopt with a role: a disk carrying Maître parity from before
// a reinstall comes back as a Parity Drive (decision 225).
func (m *Manager) AdoptAs(ctx context.Context, device, name string, tags []string, role Role) (*maitrev1.Drive, error) {
	d, err := m.find(ctx, device)
	if err != nil {
		return nil, err
	}
	if d.GetSystem() {
		return nil, errors.New("that holds the running system")
	}
	if d.GetAdopted() {
		return nil, errors.New("already a Drive of this Node")
	}
	if !adoptable[d.GetFstype()] {
		return nil, fmt.Errorf("%s cannot be adopted as is (%s); Format it instead", device, orNone(d.GetFstype()))
	}
	if d.GetDriveId() == "" {
		return nil, errors.New("that filesystem has no UUID; Format it instead")
	}
	return m.recordAs(ctx, d, name, tags, "adopted", role)
}

// Format erases device as btrfs and records the Drive. confirm must match
// the model (or the device path when it has none).
func (m *Manager) Format(ctx context.Context, device, name string, tags []string, confirm string) (*maitrev1.Drive, error) {
	return m.FormatAs(ctx, device, name, tags, confirm, Role{})
}

// Role is what a formatted Drive becomes beyond a plain data Drive (ADR
// 0009): its role, the SnapRAID name it inherits when it replaces a failed
// Drive, and the disks of a Mirrored Drive.
type Role struct {
	Role         string
	SnapraidName string
	Disks        []string
}

// ConfirmWord is what a caller repeats to format a device: its model, or
// its path when the model says nothing.
func ConfirmWord(d *maitrev1.Drive) string {
	want := d.GetModel()
	if strings.HasSuffix(want, " disk") && d.GetSerial() == "" {
		want = d.GetDevice()
	}
	return want
}

// FormatAs is Format with a role. A Parity Drive is labelled
// maitre-parity-<name>, so one from before a reinstall is recognised
// (decision 225), and its parity folder has copy-on-write off (226).
func (m *Manager) FormatAs(ctx context.Context, device, name string, tags []string, confirm string, role Role) (*maitrev1.Drive, error) {
	d, err := m.find(ctx, device)
	if err != nil {
		return nil, err
	}
	if d.GetSystem() {
		return nil, errors.New("that holds the running system")
	}
	if d.GetAdopted() {
		return nil, errors.New("already a Drive of this Node; forget it first")
	}
	want := ConfirmWord(d)
	if strings.TrimSpace(confirm) != want {
		return nil, fmt.Errorf("to format, repeat exactly: %s", want)
	}
	if name == "" {
		name = m.SuggestName(ctx)
	}
	if name == "" {
		name = want
	}
	prefix := "maitre-"
	if role.Role == RoleParity {
		prefix = ParityLabelPrefix
	}
	label := prefix + strings.ToLower(strings.NewReplacer(" ", "-", "/", "-").Replace(name))
	if len(label) > 32 {
		label = label[:32]
	}
	if _, err := m.run.Run(ctx, "wipefs", "-a", device); err != nil {
		return nil, err
	}
	if _, err := m.run.Run(ctx, "mkfs.btrfs", "-f", "-L", label, device); err != nil {
		return nil, err
	}
	d, err = m.find(ctx, device)
	if err != nil {
		return nil, err
	}
	if d.GetDriveId() == "" {
		return nil, errors.New("formatted, but the new filesystem reports no UUID")
	}
	return m.recordAs(ctx, d, name, tags, "formatted", role)
}

// ParityLabelPrefix starts the filesystem label of a Parity Drive.
const ParityLabelPrefix = "maitre-parity-"

// ParityDir is where a Parity Drive keeps SnapRAID's files, and where a
// protected Drive keeps its content files (decision 229), under the mount.
const ParityDir = ".maitre/snapraid"

// Reformat erases an adopted Drive of this Node and records it anew with a
// role, keeping its name and tags: a drained Drive becoming a Parity Drive
// (decision 241). The filesystem UUID changes, so the old record goes and
// a new one takes its place. It refuses while the Drive holds anything but
// Maître's own .maitre folder.
func (m *Manager) Reformat(ctx context.Context, driveID string, role Role) (*maitrev1.Drive, error) {
	var rec maitrev1.DriveRecord
	if _, err := m.st.Get(ctx, KindDrive, driveID, &rec); err != nil {
		return nil, fmt.Errorf("no Drive %s", driveID)
	}
	if rec.GetOrigin() == OriginInternal {
		return nil, errors.New("that is the Node's built-in storage")
	}
	drives, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	var d *maitrev1.Drive
	for _, x := range drives {
		if x.GetDriveId() == driveID {
			d = x
		}
	}
	if d == nil {
		return nil, fmt.Errorf("%s is not connected", rec.GetName())
	}
	dir := filepath.Join(m.Mounts, driveID)
	if mounted(dir) {
		if left := Leftovers(dir); len(left) > 0 {
			return nil, fmt.Errorf("%s still holds %s; it is not formatted", rec.GetName(), strings.Join(left, ", "))
		}
		if _, err := m.run.Run(ctx, "umount", dir); err != nil {
			return nil, err
		}
	}
	if _, err := m.st.Delete(ctx, KindDrive, driveID, false); err != nil {
		return nil, err
	}
	return m.FormatAs(ctx, d.GetDevice(), rec.GetName(), rec.GetTags(), ConfirmWord(d), role)
}

// Leftovers lists what a mounted Drive holds besides Maître's own files
// (.maitre…, lost+found): what stops a Drain from ending in a format.
func Leftovers(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".maitre") || name == "lost+found" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// SetRole changes a Drive's role (a Parity Drive turned off becomes a
// data Drive again, decision 245) and reassembles the Pools.
func (m *Manager) SetRole(ctx context.Context, driveID, role string) error {
	var rec maitrev1.DriveRecord
	if _, err := m.st.Get(ctx, KindDrive, driveID, &rec); err != nil {
		return fmt.Errorf("no Drive %s", driveID)
	}
	rec.Role = role
	if _, err := m.st.Put(ctx, KindDrive, driveID, &rec, false); err != nil {
		return err
	}
	if m.OnChange != nil {
		m.OnChange()
	}
	return nil
}

// Update rewrites a Drive's record through fn and reassembles the Pools.
func (m *Manager) Update(ctx context.Context, driveID string, fn func(*maitrev1.DriveRecord)) error {
	var rec maitrev1.DriveRecord
	if _, err := m.st.Get(ctx, KindDrive, driveID, &rec); err != nil {
		return fmt.Errorf("no Drive %s", driveID)
	}
	fn(&rec)
	if _, err := m.st.Put(ctx, KindDrive, driveID, &rec, false); err != nil {
		return err
	}
	if m.OnChange != nil {
		m.OnChange()
	}
	return nil
}

func (m *Manager) record(ctx context.Context, d *maitrev1.Drive, name string, tags []string, origin string) (*maitrev1.Drive, error) {
	return m.recordAs(ctx, d, name, tags, origin, Role{})
}

func (m *Manager) recordAs(ctx context.Context, d *maitrev1.Drive, name string, tags []string, origin string, role Role) (*maitrev1.Drive, error) {
	if name == "" {
		name = m.SuggestName(ctx)
	}
	if name == "" {
		name = d.GetLabel()
		if name == "" {
			name = d.GetModel()
		}
	}
	rec := &maitrev1.DriveRecord{
		DriveId: d.GetDriveId(), NodeId: m.st.NodeID(), Name: name, Model: d.GetModel(), Serial: d.GetSerial(),
		SizeBytes: d.GetSizeBytes(), Fstype: d.GetFstype(), AutoTags: d.GetAutoTags(), Tags: tags, Origin: origin, AddedUnixMs: time.Now().UnixMilli(),
		Role: role.Role, SnapraidName: role.SnapraidName, Disks: role.Disks,
	}
	if rec.Fstype == "" {
		rec.Fstype = "btrfs"
	}
	if err := m.Mount(ctx, rec); err != nil {
		return nil, err
	}
	if role.Role == RoleParity {
		// Decision 226: the parity file is rewritten in place at every
		// sync; copy-on-write would fragment it.
		pdir := filepath.Join(m.Mounts, rec.DriveId, ParityDir)
		if err := os.MkdirAll(pdir, 0o700); err != nil {
			return nil, err
		}
		_, _ = m.run.Run(ctx, "chattr", "+C", pdir)
	}
	if _, err := m.st.Put(ctx, KindDrive, rec.DriveId, rec, false); err != nil {
		return nil, err
	}
	if m.OnChange != nil {
		m.OnChange()
	}
	return m.find(ctx, d.GetDevice())
}

// Mount mounts a Drive at Mounts/<id> by UUID; idempotent.
func (m *Manager) Mount(ctx context.Context, rec *maitrev1.DriveRecord) error {
	dir := filepath.Join(m.Mounts, rec.GetDriveId())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if mounted(dir) {
		return nil
	}
	opts := "noatime"
	fstype := rec.GetFstype()
	if fstype == "ntfs" {
		fstype = "ntfs3"
	}
	args := []string{"-t", fstype, "-o", opts, "UUID=" + rec.GetDriveId(), dir}
	if _, err := m.run.Run(ctx, "mount", args...); err != nil {
		return fmt.Errorf("mount %s: %w", rec.GetName(), err)
	}
	return nil
}

// MountAll mounts every Drive of this Node; errors are collected, not fatal.
func (m *Manager) MountAll(ctx context.Context) ([]string, []error) {
	records, err := store.ListLive(ctx, m.st, KindDrive, func() *maitrev1.DriveRecord { return &maitrev1.DriveRecord{} })
	if err != nil {
		return nil, []error{err}
	}
	var dirs []string
	var errs []error
	for _, r := range records {
		if r.GetNodeId() != m.st.NodeID() {
			continue
		}
		if err := m.Mount(ctx, r); err != nil {
			errs = append(errs, err)
			continue
		}
		dirs = append(dirs, filepath.Join(m.Mounts, r.GetDriveId()))
	}
	return dirs, errs
}

// SetTags replaces a Drive's user tags and name.
func (m *Manager) SetTags(ctx context.Context, driveID string, tags []string, name string) (*maitrev1.Drive, error) {
	var rec maitrev1.DriveRecord
	if _, err := m.st.Get(ctx, KindDrive, driveID, &rec); err != nil {
		return nil, fmt.Errorf("no Drive %s", driveID)
	}
	rec.Tags = tags
	if name != "" {
		rec.Name = name
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
	for _, d := range drives {
		if d.GetDriveId() == driveID {
			return d, nil
		}
	}
	return nil, nil
}

// Forget unmounts a Drive and deletes its record; data stays on the device.
func (m *Manager) Forget(ctx context.Context, driveID, confirm string) error {
	var rec maitrev1.DriveRecord
	if _, err := m.st.Get(ctx, KindDrive, driveID, &rec); err != nil {
		return fmt.Errorf("no Drive %s", driveID)
	}
	if rec.GetOrigin() == OriginInternal {
		return errors.New("that is the Node's built-in storage; it cannot be removed")
	}
	if strings.TrimSpace(confirm) != rec.GetName() {
		return fmt.Errorf("to remove it, repeat exactly: %s", rec.GetName())
	}
	if _, err := m.st.Delete(ctx, KindDrive, driveID, false); err != nil {
		return err
	}
	if m.OnChange != nil {
		m.OnChange()
	}
	dir := filepath.Join(m.Mounts, driveID)
	if mounted(dir) {
		_, _ = m.run.Run(ctx, "umount", "-l", dir)
	}
	return nil
}

func listLive(ctx context.Context, st *store.Store) ([]*maitrev1.DriveRecord, error) {
	return store.ListLive(ctx, st, KindDrive, func() *maitrev1.DriveRecord { return &maitrev1.DriveRecord{} })
}

func orNone(s string) string {
	if s == "" {
		return "no filesystem"
	}
	return s
}

// mountedDirs, when set (tests), stands in for /proc/self/mounts.
var mountedDirs map[string]bool

func mounted(dir string) bool {
	if mountedDirs != nil {
		return mountedDirs[dir]
	}
	b, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == dir {
			return true
		}
	}
	return false
}
