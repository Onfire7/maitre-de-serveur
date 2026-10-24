package installer

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Plan is everything an install needs.
type Plan struct {
	ImageRef      string
	TargetDisk    string
	DataPartition bool
	Fleet         *maitrev1.Fleet
	NodeName      string
	Trusted       []*maitrev1.TrustedDevice
	// NodeIdentity becomes the installed Node's identity: the Installer's
	// key is carried over so the installer_id the Console paired with is the
	// node_id it sees afterwards.
	NodeIdentity *identity.Identity
}

// Progress reports install progress for the screen and the Console.
type Progress func(phase maitrev1.Provisioning_Phase, percent uint32, message string)

// Runner performs an install.
type Runner interface {
	Install(ctx context.Context, plan Plan, progress Progress) error
}

// Partition labels the agent mounts by.
const (
	ConfigLabel = "maitre-config"
	DataLabel   = "maitre-data"
	// RootSize leaves room for the config and data partitions after the OS.
	RootSize = "32G"
	// ConfigSize is tiny: it holds keys and the kilobyte-scale Fleet database.
	ConfigSize = "512M"
)

// BootcRunner installs with `bootc install to-disk` run from the target
// image via podman (bootc must run from the image it installs, design
// question 70), then adds the config partition and seeds it.
type BootcRunner struct {
	// Exec runs a command; swapped in tests.
	Exec func(ctx context.Context, stdout io.Writer, name string, args ...string) error
}

func (r *BootcRunner) exec(ctx context.Context, stdout io.Writer, name string, args ...string) error {
	if r.Exec != nil {
		return r.Exec(ctx, stdout, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stdout
	return cmd.Run()
}

// execPull is exec on a pseudo-terminal, so podman draws progress bars.
func (r *BootcRunner) execPull(ctx context.Context, stdout io.Writer, name string, args ...string) error {
	if r.Exec != nil {
		return r.Exec(ctx, stdout, name, args...)
	}
	return execTTY(ctx, stdout, name, args...)
}

// Install implements Runner.
func (r *BootcRunner) Install(ctx context.Context, plan Plan, progress Progress) error {
	progress(maitrev1.Provisioning_PHASE_INSTALLING, 0, "downloading Maître Server")
	pp := newPullProgress(imageSize(ctx, plan.ImageRef))
	var last uint32
	pull := &progressWriter{onLine: func(l string) {
		pp.line(l)
		f, msg := pp.fraction()
		if pct := uint32(f * 50); pct > last {
			last = pct
			progress(maitrev1.Provisioning_PHASE_INSTALLING, pct, "downloading Maître Server ("+msg+")")
		}
	}}
	if err := r.execPull(ctx, pull, "podman", "pull", plan.ImageRef); err != nil {
		return fmt.Errorf("download %s: %w", plan.ImageRef, err)
	}

	progress(maitrev1.Provisioning_PHASE_INSTALLING, 50, "writing the OS to "+plan.TargetDisk)
	install := &progressWriter{onLine: func(l string) {
		progress(maitrev1.Provisioning_PHASE_INSTALLING, 60, strings.TrimSpace(l))
	}}
	if err := r.exec(ctx, install, "podman", "run", "--rm", "--privileged", "--pid=host", "--ipc=host",
		"--security-opt", "label=type:unconfined_t",
		"-v", "/dev:/dev", "-v", "/var/lib/containers:/var/lib/containers",
		plan.ImageRef,
		"bootc", "install", "to-disk", "--wipe", "--filesystem", "xfs", "--root-size", RootSize,
		"--target-imgref", plan.ImageRef, "--skip-fetch-check",
		"--karg", "console=tty0", "--karg", "console=ttyS0,115200n8",
		plan.TargetDisk); err != nil {
		return fmt.Errorf("bootc install: %w", err)
	}

	progress(maitrev1.Provisioning_PHASE_CONFIGURING, 80, "creating the config partition")
	// Append a config partition and, if wanted, a data partition taking the
	// rest. sfdisk's append mode leaves the bootc partitions untouched.
	script := fmt.Sprintf("size=%s, type=0FC63DAF-8483-4772-8E79-3D69D8477DE4, name=%s\n", ConfigSize, ConfigLabel)
	if plan.DataPartition {
		script += fmt.Sprintf("type=0FC63DAF-8483-4772-8E79-3D69D8477DE4, name=%s\n", DataLabel)
	}
	if err := r.sfdiskAppend(ctx, plan.TargetDisk, script); err != nil {
		return err
	}
	if err := r.exec(ctx, io.Discard, "udevadm", "settle"); err != nil {
		return fmt.Errorf("udevadm settle: %w", err)
	}
	parts, err := r.partitionsByName(ctx, plan.TargetDisk)
	if err != nil {
		return err
	}
	cfgDev, ok := parts[ConfigLabel]
	if !ok {
		return fmt.Errorf("config partition not found after partitioning")
	}
	if err := r.exec(ctx, io.Discard, "mkfs.ext4", "-q", "-F", "-L", ConfigLabel, cfgDev); err != nil {
		return fmt.Errorf("format config partition: %w", err)
	}
	if plan.DataPartition {
		if dataDev, ok := parts[DataLabel]; ok {
			progress(maitrev1.Provisioning_PHASE_CONFIGURING, 85, "formatting the data partition")
			if err := r.exec(ctx, io.Discard, "mkfs.ext4", "-q", "-F", "-L", DataLabel, dataDev); err != nil {
				return fmt.Errorf("format data partition: %w", err)
			}
		}
	}

	progress(maitrev1.Provisioning_PHASE_CONFIGURING, 90, "writing the Node identity and Fleet")
	mnt, err := os.MkdirTemp("", "maitre-config-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mnt)
	if err := r.exec(ctx, io.Discard, "mount", cfgDev, mnt); err != nil {
		return fmt.Errorf("mount config partition: %w", err)
	}
	defer r.exec(context.Background(), io.Discard, "umount", mnt)
	if err := SeedConfig(ctx, mnt, plan); err != nil {
		return err
	}
	if err := r.exec(ctx, io.Discard, "sync"); err != nil {
		return err
	}
	return nil
}

func (r *BootcRunner) sfdiskAppend(ctx context.Context, disk, script string) error {
	tmp, err := os.CreateTemp("", "maitre-sfdisk-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(script); err != nil {
		return err
	}
	tmp.Close()
	// sfdisk reads the script from stdin; route it through sh so the Exec
	// hook stays a plain argv for tests.
	return r.exec(ctx, io.Discard, "sh", "-c", fmt.Sprintf("sfdisk --append --no-reread %s < %s", disk, tmp.Name()))
}

// partitionsByName maps GPT partition names to device paths for disk.
func (r *BootcRunner) partitionsByName(ctx context.Context, disk string) (map[string]string, error) {
	var out strings.Builder
	if err := r.exec(ctx, &out, "lsblk", "-ln", "-o", "PATH,PARTLABEL", disk); err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	m := map[string]string{}
	for _, line := range strings.Split(out.String(), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			m[f[1]] = f[0]
		}
	}
	return m, nil
}

// SeedConfig writes the installed Node's identity and initial Fleet database
// into the (mounted) config partition. Layout:
//
//	node/identity.key, node/identity.pub   the Node key (= the Installer's)
//	fleet.db                               Records: fleet, this node, trusted devices
func SeedConfig(ctx context.Context, dir string, plan Plan) error {
	if err := plan.NodeIdentity.Save(filepath.Join(dir, "node")); err != nil {
		return fmt.Errorf("save node identity: %w", err)
	}
	st, err := store.Open(filepath.Join(dir, "fleet.db"), plan.NodeIdentity.Fingerprint())
	if err != nil {
		return err
	}
	defer st.Close()
	now := time.Now().UnixMilli()
	fleet := plan.Fleet
	if fleet.GetCreatedUnixMs() == 0 {
		fleet = &maitrev1.Fleet{FleetId: fleet.GetFleetId(), Name: fleet.GetName(), CreatedUnixMs: now}
	}
	if _, err := st.Put(ctx, store.KindFleet, fleet.GetFleetId(), fleet, true); err != nil {
		return err
	}
	node := &maitrev1.Node{
		NodeId:            plan.NodeIdentity.Fingerprint(),
		Name:              plan.NodeName,
		PublicKeyPem:      string(plan.NodeIdentity.PublicKeyPEM()),
		ProvisionedUnixMs: now,
	}
	if _, err := st.Put(ctx, store.KindNode, node.NodeId, node, false); err != nil {
		return err
	}
	for _, d := range plan.Trusted {
		pub, err := identity.ParsePublicKeyPEM([]byte(d.GetPublicKeyPem()))
		if err != nil {
			return fmt.Errorf("trusted device %q: %w", d.GetName(), err)
		}
		rec := &maitrev1.TrustedDevice{
			DeviceId:     identity.Fingerprint(pub),
			Name:         d.GetName(),
			PublicKeyPem: d.GetPublicKeyPem(),
			RecoveryOnly: d.GetRecoveryOnly(),
			AddedUnixMs:  now,
		}
		if _, err := st.Put(ctx, store.KindTrustedDevice, rec.DeviceId, rec, true); err != nil {
			return err
		}
	}
	return nil
}

// progressWriter calls onLine for each line written.
type progressWriter struct {
	onLine func(string)
	buf    []byte
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexAny(string(w.buf), "\r\n")
		if i < 0 {
			return len(p), nil
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		if strings.TrimSpace(line) != "" {
			w.onLine(line)
		}
	}
}

// AllowInsecureRegistry tells podman and bootc to pull from host:port over
// plain HTTP or with an untrusted certificate. Only the CI boot test uses it,
// to install from a throwaway registry on the QEMU host.
func AllowInsecureRegistry(hostPort string) error {
	dir := "/etc/containers/registries.conf.d"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	conf := fmt.Sprintf("[[registry]]\nlocation = %q\ninsecure = true\n", hostPort)
	return os.WriteFile(filepath.Join(dir, "99-maitre-insecure.conf"), []byte(conf), 0o644)
}
