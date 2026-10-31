package installer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// Decision 262: the person setting up a machine can look inside its disks
// to tell them apart before choosing one to erase. Partitions are mounted
// read-only with journal replay off, so looking never writes to a disk;
// only names, sizes and dates are listed, never a file's contents.

// FolderLister looks inside partitions for ListFolder.
type FolderLister interface {
	List(ctx context.Context, p *maitrev1.Partition, dir string) (*maitrev1.ListFolderResponse, error)
	// Close unmounts everything; called before an install starts.
	Close()
}

// maxEntries is how many entries one folder listing carries.
const maxEntries = 1000

// mountOptions are read-only options that also keep the filesystem from
// replaying its journal, which plain ro does on ext4, XFS and btrfs.
var mountOptions = map[string]string{
	"ext2": "noload", "ext3": "noload", "ext4": "noload",
	"xfs":   "norecovery",
	"btrfs": "rescue=nologreplay",
	"ntfs":  "", "exfat": "", "vfat": "", "f2fs": "", "iso9660": "", "hfsplus": "", "udf": "",
}

// browsable reports whether ListFolder can look inside a filesystem type.
func browsable(fstype string) bool {
	_, ok := mountOptions[fstype]
	return ok
}

// Browser mounts partitions under Root as they are asked for and unmounts
// each once it has not been looked at for Idle.
type Browser struct {
	Root string
	Idle time.Duration
	// Mount and Unmount are swapped in tests.
	Mount   func(ctx context.Context, device, fstype, dir string) error
	Unmount func(dir string) error

	mu     sync.Mutex
	mounts map[string]*browseMount
}

type browseMount struct {
	dir   string
	timer *time.Timer
}

// NewBrowser mounts under /run/maitre-browse.
func NewBrowser() *Browser {
	return &Browser{Root: "/run/maitre-browse", Idle: 2 * time.Minute, Mount: mountReadOnly, Unmount: unmount}
}

func mountReadOnly(ctx context.Context, device, fstype, dir string) error {
	opts := "ro,nosuid,nodev,noexec,noatime"
	if extra := mountOptions[fstype]; extra != "" {
		opts += "," + extra
	}
	if fstype == "ntfs" {
		fstype = "ntfs3"
	}
	out, err := exec.CommandContext(ctx, "mount", "-t", fstype, "-o", opts, device, dir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cannot open %s: %s", device, firstLine(out, err))
	}
	return nil
}

func unmount(dir string) error {
	return exec.Command("umount", dir).Run()
}

func firstLine(out []byte, err error) string {
	if s := strings.TrimSpace(string(out)); s != "" {
		s, _, _ = strings.Cut(s, "\n")
		return strings.TrimPrefix(s, "mount: ")
	}
	return err.Error()
}

// List implements FolderLister.
func (b *Browser) List(ctx context.Context, p *maitrev1.Partition, dir string) (*maitrev1.ListFolderResponse, error) {
	if !p.GetBrowsable() {
		return nil, fmt.Errorf("%s holds no filesystem this Installer can open", p.GetDevice())
	}
	mnt, err := b.mounted(ctx, p)
	if err != nil {
		return nil, err
	}
	return listDir(mnt, dir)
}

func (b *Browser) mounted(ctx context.Context, p *maitrev1.Partition) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mounts == nil {
		b.mounts = map[string]*browseMount{}
	}
	dev := p.GetDevice()
	if m, ok := b.mounts[dev]; ok {
		m.timer.Reset(b.Idle)
		return m.dir, nil
	}
	dir := filepath.Join(b.Root, filepath.Base(dev))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := b.Mount(ctx, dev, p.GetFstype(), dir); err != nil {
		return "", err
	}
	m := &browseMount{dir: dir}
	m.timer = time.AfterFunc(b.Idle, func() { b.release(dev, m) })
	b.mounts[dev] = m
	return dir, nil
}

func (b *Browser) release(dev string, m *browseMount) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mounts[dev] != m {
		return
	}
	delete(b.mounts, dev)
	_ = b.Unmount(m.dir)
}

// Close implements FolderLister.
func (b *Browser) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for dev, m := range b.mounts {
		m.timer.Stop()
		_ = b.Unmount(m.dir)
		delete(b.mounts, dev)
	}
}

// cleanFolder turns a requested folder into a path os.Root accepts: "." for
// the top, no leading slash, no "..".
func cleanFolder(dir string) string {
	c := path.Clean("/" + dir)
	if c == "/" {
		return "."
	}
	return strings.TrimPrefix(c, "/")
}

// listDir lists dir inside root without leaving it: os.Root refuses ".."
// and symbolic links that point outside.
func listDir(root, dir string) (*maitrev1.ListFolderResponse, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := r.Open(cleanFolder(dir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no folder %s", dir)
		}
		return nil, err
	}
	defer f.Close()
	des, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	entries := make([]*maitrev1.FolderEntry, 0, len(des))
	for _, de := range des {
		e := &maitrev1.FolderEntry{Name: de.Name(), Dir: de.IsDir(), Link: de.Type()&fs.ModeSymlink != 0}
		if info, err := de.Info(); err == nil {
			e.ModifiedUnixMs = info.ModTime().UnixMilli()
			if info.Mode().IsRegular() {
				e.SizeBytes = uint64(info.Size())
			}
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	resp := &maitrev1.ListFolderResponse{}
	if len(entries) > maxEntries {
		resp.More = uint32(len(entries) - maxEntries)
		entries = entries[:maxEntries]
	}
	resp.Entries = entries
	resp.UsedBytes, resp.TotalBytes = fsUsage(root)
	return resp, nil
}

// partitions lists what lsblk sees inside a disk: its partitions, the
// volumes inside those (LVM, opened LUKS), or a filesystem on the whole
// disk. Swap and empty partitions are listed too, so every byte is
// accounted for.
func partitions(d lsblkDev, installerMedia bool) []*maitrev1.Partition {
	var out []*maitrev1.Partition
	var walk func(n lsblkDev)
	walk = func(n lsblkDev) {
		fstype := deref(n.FSType)
		if len(n.Children) == 0 || fstype != "" {
			label := deref(n.Label)
			if label == "" {
				label = deref(n.PartLabel)
			}
			out = append(out, &maitrev1.Partition{Device: n.Path, SizeBytes: n.Size, Fstype: fstype, Label: label, Browsable: !installerMedia && browsable(fstype)})
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	if deref(d.FSType) != "" {
		walk(d)
		return out
	}
	for _, c := range d.Children {
		walk(c)
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
