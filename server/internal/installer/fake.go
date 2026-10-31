package installer

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// FakeRunner skips bootc and partitioning and seeds the config "partition"
// into Dir. Used by tests and by `maitre installer --fake-install DIR` so
// the whole pair-provision-agent path runs on a developer machine.
type FakeRunner struct {
	Dir   string
	Delay time.Duration
}

// Install implements Runner.
func (f *FakeRunner) Install(ctx context.Context, plan Plan, progress Progress) error {
	progress(maitrev1.Provisioning_PHASE_INSTALLING, 10, "pretending to download "+plan.ImageRef)
	select {
	case <-time.After(f.Delay):
	case <-ctx.Done():
		return ctx.Err()
	}
	progress(maitrev1.Provisioning_PHASE_CONFIGURING, 90, "writing the Node identity and Fleet")
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return err
	}
	return SeedConfig(ctx, f.Dir, plan)
}

// FakeDisks is a disk list for tests: one installer stick, an SSD that held
// Windows and a data disk.
func FakeDisks(context.Context) ([]*maitrev1.Disk, error) {
	return []*maitrev1.Disk{
		{Path: "/dev/fake-usb", Model: "Installer USB", SizeBytes: 16 << 30, Removable: true, IsInstallerMedia: true, Partitions: []*maitrev1.Partition{
			{Device: "/dev/fake-usb1", SizeBytes: 600 << 20, Fstype: "vfat", Label: "MAITRELIVE"},
		}},
		{Path: "/dev/fake-ssd", Model: "Fake SSD", SizeBytes: 500 << 30, Partitions: []*maitrev1.Partition{
			{Device: "/dev/fake-ssd1", SizeBytes: 100 << 20, Fstype: "vfat", Label: "EFI system partition", Browsable: true},
			{Device: "/dev/fake-ssd2", SizeBytes: 16 << 20, Label: "Microsoft reserved partition"},
			{Device: "/dev/fake-ssd3", SizeBytes: 499 << 30, Fstype: "ntfs", Label: "Windows", Browsable: true},
		}},
		{Path: "/dev/fake-hdd", Model: "Fake HDD", SizeBytes: 4 << 40, Partitions: []*maitrev1.Partition{
			{Device: "/dev/fake-hdd1", SizeBytes: 4 << 40, Fstype: "ext4", Label: "Media", Browsable: true},
		}},
	}, nil
}

// FakeFolders answers ListFolder from a made-up tree matching FakeDisks.
type FakeFolders struct{}

var fakeTree = map[string][]string{
	"/dev/fake-ssd1/":                    {"EFI/"},
	"/dev/fake-ssd1/EFI/":                {"Boot/", "Microsoft/"},
	"/dev/fake-ssd3/":                    {"Program Files/", "Users/", "Windows/", "pagefile.sys 8589934592", "hiberfil.sys 6871947673"},
	"/dev/fake-ssd3/Users/":              {"Public/", "sam/"},
	"/dev/fake-ssd3/Users/sam/":          {"Desktop/", "Documents/", "Pictures/", "NTUSER.DAT 3407872"},
	"/dev/fake-ssd3/Users/sam/Pictures/": {"Holiday 2025/", "IMG_0412.jpg 4194304", "IMG_0413.jpg 3984588"},
	"/dev/fake-hdd1/":                    {"lost+found/", "Films/", "Music/", "Photos/", "backup-2024.tar 21474836480"},
	"/dev/fake-hdd1/Films/":              {"Arrival (2016).mkv 8053063680", "Paddington 2 (2017).mkv 6442450944"},
}

// List implements FolderLister.
func (FakeFolders) List(_ context.Context, p *maitrev1.Partition, dir string) (*maitrev1.ListFolderResponse, error) {
	key := p.GetDevice() + strings.TrimSuffix(path.Clean("/"+dir), "/") + "/"
	names, ok := fakeTree[key]
	if !ok {
		if strings.Count(key, "/") > 3 {
			return &maitrev1.ListFolderResponse{TotalBytes: p.GetSizeBytes()}, nil
		}
		return nil, fmt.Errorf("no folder %s", dir)
	}
	resp := &maitrev1.ListFolderResponse{TotalBytes: p.GetSizeBytes(), UsedBytes: p.GetSizeBytes() / 3}
	when := time.Date(2025, 8, 14, 18, 20, 0, 0, time.UTC).UnixMilli()
	for _, n := range names {
		e := &maitrev1.FolderEntry{ModifiedUnixMs: when}
		if name, ok := strings.CutSuffix(n, "/"); ok {
			e.Name, e.Dir = name, true
		} else {
			// "name with spaces <bytes>"
			i := strings.LastIndex(n, " ")
			e.Name = n[:i]
			fmt.Sscan(n[i+1:], &e.SizeBytes)
		}
		resp.Entries = append(resp.Entries, e)
	}
	return resp, nil
}

// Close implements FolderLister.
func (FakeFolders) Close() {}
