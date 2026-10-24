package installer

import (
	"context"
	"os"
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

// FakeDisks is a disk list for tests: one installer stick and one target.
func FakeDisks(context.Context) ([]*maitrev1.Disk, error) {
	return []*maitrev1.Disk{
		{Path: "/dev/fake-usb", Model: "Installer USB", SizeBytes: 16 << 30, Removable: true, IsInstallerMedia: true},
		{Path: "/dev/fake-ssd", Model: "Fake SSD", SizeBytes: 500 << 30},
	}, nil
}
