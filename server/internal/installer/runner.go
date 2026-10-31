package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Onfire7/maitre-de-serveur/server/internal/backup"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/update"
)

// Plan is everything an install needs.
type Plan struct {
	ImageRef string
	// Source is the bootc --source-imgref (docker://..., oci:...); empty
	// means docker://ImageRef.
	Source        string
	TargetDisk    string
	DataPartition bool
	Fleet         *maitrev1.Fleet
	NodeName      string
	Trusted       []*maitrev1.TrustedDevice
	// Records are signed Records stored as they are (decision 189); when
	// set, Trusted and Nodes are not used.
	Records []*maitrev1.Record
	// Nodes are the Fleet's existing Nodes, introduced into the new database.
	Nodes []*maitrev1.Node
	// RestoreDevice and RestoreNodeID, when set, restore that Node's backup
	// from that partition instead of seeding a new identity (decision 102).
	RestoreDevice string
	RestoreNodeID string
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
	// RootSize leaves room for the config and data partitions after the OS;
	// a disk too small for it gets what it can spare (decision 269).
	RootSize = "32G"
	// MinRoot is the least the OS partition may be: the image is about 2 GB
	// installed, and updates keep a second copy.
	MinRoot = 8 << 30
	// ConfigSize is tiny: it holds keys and the kilobyte-scale Fleet database.
	ConfigSize = "512M"
)

// BootcRunner installs with `bootc install to-disk` run from the target
// image via podman (bootc must run from the image it installs, design
// question 70), then adds the config partition and seeds it.
type BootcRunner struct {
	// Exec runs a command; swapped in tests.
	Exec func(ctx context.Context, stdout io.Writer, name string, args ...string) error
	// Direct runs bootc from the live system instead of from a podman
	// container of the target image, with --source-imgref so the image
	// streams from the registry straight onto the disk. Used when netbooted
	// (decision 79): the live root is in RAM and cannot hold a podman pull.
	Direct bool
}

func (r *BootcRunner) exec(ctx context.Context, stdout io.Writer, name string, args ...string) error {
	if r.Exec != nil {
		return r.Exec(ctx, stdout, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	tail := &tailWriter{}
	// Every line goes to the journal too, so the machine's Log page and
	// journalctl have the whole story of a failed command (decision 265).
	journal := &progressWriter{onLine: func(l string) { slog.Info(name, "out", strings.TrimSpace(l)) }}
	cmd.Stdout = io.MultiWriter(stdout, tail, journal)
	cmd.Stderr = cmd.Stdout
	return tail.explain(cmd.Run())
}

// tailWriter keeps the end of a command's output, so a failure says more
// than "exit status 1" (sfdisk out of room, mkfs refusing a device).
type tailWriter struct{ buf []byte }

func (t *tailWriter) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > 8192 {
		t.buf = t.buf[len(t.buf)-8192:]
	}
	return len(p), nil
}

// OutputError is a failed command with the end of what it printed, for the
// Installer to show in full (decision 265).
type OutputError struct {
	Err   error
	Lines []string
}

func (e *OutputError) Error() string { return e.Err.Error() }
func (e *OutputError) Unwrap() error { return e.Err }

// Output returns the lines a failed command printed, if err carries them.
func Output(err error) []string {
	var oe *OutputError
	if errors.As(err, &oe) {
		return oe.Lines
	}
	return nil
}

// errorLine marks the line that names what went wrong, as bootc ("ERROR
// Installing to disk"), sfdisk ("Failed to add") and mkfs put it.
var errorLine = regexp.MustCompile(`(?i)\b(error|failed|cannot|can't|unable|denied|no such|not found|refused|invalid)\b`)

// explain adds three lines of output to err, ending at the last line that
// names an error rather than at the very end: bootc follows its error with a
// many-line dump of the command it ran, whose tail says nothing.
func (t *tailWriter) explain(err error) error {
	if err == nil {
		return nil
	}
	var lines []string
	for _, l := range strings.FieldsFunc(string(t.buf), func(r rune) bool { return r == '\n' || r == '\r' }) {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return err
	}
	end := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		if errorLine.MatchString(lines[i]) {
			end = i + 1
			break
		}
	}
	start := max(0, end-3)
	return &OutputError{Err: fmt.Errorf("%w: %s", err, strings.Join(lines[start:end], " / ")), Lines: lines}
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
	progress(maitrev1.Provisioning_PHASE_INSTALLING, 0, "releasing "+plan.TargetDisk)
	r.releaseDisk(ctx, plan.TargetDisk)
	if r.Direct {
		if err := r.installDirect(ctx, plan, progress); err != nil {
			return err
		}
		return r.configure(ctx, plan, progress)
	}
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
	root, err := r.rootSize(ctx, plan)
	if err != nil {
		return err
	}
	install := &progressWriter{onLine: func(l string) {
		progress(maitrev1.Provisioning_PHASE_INSTALLING, 60, strings.TrimSpace(l))
	}}
	args := []string{"run", "--rm", "--privileged", "--pid=host", "--ipc=host",
		"--security-opt", "label=type:unconfined_t",
		"-v", "/dev:/dev", "-v", "/var/lib/containers:/var/lib/containers",
		plan.ImageRef,
		"bootc", "install", "to-disk", "--wipe", "--filesystem", "xfs", "--root-size", root,
		"--target-imgref", plan.ImageRef, "--skip-fetch-check"}
	args = append(args, consoleKargs()...)
	if err := r.exec(ctx, install, "podman", append(args, plan.TargetDisk)...); err != nil {
		return fmt.Errorf("bootc install: %w", err)
	}
	return r.configure(ctx, plan, progress)
}

// installDirect is the netboot path: bootc on the live system pulls the
// image from the registry while writing it to the disk. bootc prints its own
// progress lines; they are relayed as the message.
func (r *BootcRunner) installDirect(ctx context.Context, plan Plan, progress Progress) error {
	progress(maitrev1.Provisioning_PHASE_INSTALLING, 0, "writing Maître Server to "+plan.TargetDisk)
	root, err := r.rootSize(ctx, plan)
	if err != nil {
		return err
	}
	install := &progressWriter{onLine: func(l string) {
		if l = strings.TrimSpace(l); l != "" {
			progress(maitrev1.Provisioning_PHASE_INSTALLING, 30, l)
		}
	}}
	src := plan.Source
	if src == "" {
		src = "docker://" + plan.ImageRef
	}
	args := []string{"install", "to-disk", "--wipe", "--filesystem", "xfs", "--root-size", root,
		"--source-imgref", src, "--target-imgref", plan.ImageRef, "--skip-fetch-check"}
	args = append(args, consoleKargs()...)
	if err := r.exec(ctx, install, "bootc", append(args, plan.TargetDisk)...); err != nil {
		return fmt.Errorf("bootc install: %w", err)
	}
	return nil
}

// releaseDisk lets go of whatever the live system holds on the target disk
// so bootc's wipe finds it free (decision 271): mounts, swap, LVM volume
// groups the kernel activated on finding their partitions, RAID arrays it
// assembled, and opened encrypted volumes. Innermost first, so a volume on
// an array on a partition is released in order. Best effort: a hold that
// stays makes bootc fail with the device's name, as before.
func (r *BootcRunner) releaseDisk(ctx context.Context, disk string) {
	var out strings.Builder
	if err := r.exec(ctx, &out, "lsblk", "-J", "-b", "-o", "PATH,TYPE,FSTYPE,MOUNTPOINTS", disk); err != nil {
		return
	}
	var parsed lsblkOut
	if json.Unmarshal([]byte(out.String()), &parsed) != nil {
		return
	}
	var release func(d lsblkDev)
	release = func(d lsblkDev) {
		for _, c := range d.Children {
			release(c)
		}
		for _, m := range d.Mountpoints {
			if m != nil && *m != "" && *m != "[SWAP]" {
				_ = r.exec(ctx, io.Discard, "umount", *m)
			}
		}
		fstype := deref(d.FSType)
		if fstype == "swap" {
			_ = r.exec(ctx, io.Discard, "swapoff", d.Path)
		}
		switch {
		case d.Type == "lvm":
			_ = r.exec(ctx, io.Discard, "lvchange", "-an", d.Path)
		case d.Type == "crypt":
			_ = r.exec(ctx, io.Discard, "cryptsetup", "close", d.Path)
		case strings.HasPrefix(d.Type, "raid"):
			_ = r.exec(ctx, io.Discard, "mdadm", "--stop", d.Path)
		}
		if fstype == "LVM2_member" {
			var vg strings.Builder
			if err := r.exec(ctx, &vg, "pvs", "--noheadings", "-o", "vg_name", d.Path); err == nil {
				if name := strings.TrimSpace(vg.String()); name != "" {
					_ = r.exec(ctx, io.Discard, "vgchange", "-an", name)
				}
			}
		}
	}
	for _, d := range parsed.Blockdevices {
		release(d)
	}
	_ = r.exec(ctx, io.Discard, "udevadm", "settle")
}

// rootSize is bootc's --root-size for the target: RootSize when the disk
// has room for it and the partitions added after, less on a small disk
// (a 29 GiB USB stick), and an error when even MinRoot does not fit.
func (r *BootcRunner) rootSize(ctx context.Context, plan Plan) (string, error) {
	var out strings.Builder
	if err := r.exec(ctx, &out, "lsblk", "-bdno", "SIZE", plan.TargetDisk); err != nil {
		return RootSize, nil
	}
	size, err := strconv.ParseInt(strings.TrimSpace(out.String()), 10, 64)
	if err != nil || size <= 0 {
		return RootSize, nil
	}
	return rootSizeFor(size, plan.DataPartition)
}

// rootSizeFor is rootSize's arithmetic. bootc puts 1 MiB of BIOS boot and
// a 512 MiB ESP before the root; the config partition follows it, then the
// data partition if wanted, which is given at least 2 GiB to be worth having.
func rootSizeFor(disk int64, data bool) (string, error) {
	reserve := int64(1<<30) + 512<<20
	if data {
		reserve += 2 << 30
	}
	root := disk - reserve
	if root >= 32<<30 {
		return RootSize, nil
	}
	if root < MinRoot {
		return "", fmt.Errorf("the disk is too small: %.1f GiB; Maître needs at least %d GiB%s", float64(disk)/(1<<30), (MinRoot+reserve)>>30, map[bool]string{true: " with a data partition", false: ""}[data])
	}
	return fmt.Sprintf("%dM", root>>20), nil
}

// consoleKargs passes the Installer's own console= arguments on to the
// installed system: a VM keeps its serial log, a laptop with no serial port
// gets none (agetty crashes on a ttyS0 that is not there).
func consoleKargs() []string {
	var out []string
	b, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return []string{"--karg", "console=tty0"}
	}
	for _, f := range strings.Fields(string(b)) {
		if strings.HasPrefix(f, "console=") {
			out = append(out, "--karg", f)
		}
	}
	if len(out) == 0 {
		out = []string{"--karg", "console=tty0"}
	}
	return out
}

// configure adds the config (and optional data) partition after bootc has
// laid out the disk, and seeds the Node's configuration.
func (r *BootcRunner) configure(ctx context.Context, plan Plan, progress Progress) error {
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
	// sfdisk asks the kernel to re-read the table, which it refuses while
	// anything on the disk is still held open (bootc's mounts can linger
	// briefly when it ran on this host). partx adds the new partitions one by
	// one instead, which works regardless.
	_ = r.exec(ctx, io.Discard, "partx", "-u", plan.TargetDisk)
	if err := r.exec(ctx, io.Discard, "udevadm", "settle"); err != nil {
		return fmt.Errorf("udevadm settle: %w", err)
	}
	parts, err := r.partitionsByName(ctx, plan.TargetDisk)
	if err != nil {
		return err
	}
	cfgDev, ok := parts[ConfigLabel]
	if !ok {
		return fmt.Errorf("config partition not found after partitioning (have %v)", parts)
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
	if plan.RestoreDevice != "" {
		progress(maitrev1.Provisioning_PHASE_CONFIGURING, 92, "restoring the Node from its backup on "+plan.RestoreDevice)
		if err := backup.RestoreFromDevice(ctx, plan.RestoreDevice, plan.RestoreNodeID, mnt); err != nil {
			return err
		}
	} else if err := SeedConfig(ctx, mnt, plan); err != nil {
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
	st.SignWith(plan.NodeIdentity)
	self := plan.NodeIdentity.Fingerprint()
	seeded := map[string]bool{}
	for _, r := range plan.Records {
		if r.GetKind() == store.KindNode && r.GetId() == self {
			continue // written below, by this Node
		}
		if r.GetOwnerNodeId() == "" {
			// Signed by the Console for a new Fleet: nobody stored it yet.
			r = proto.Clone(r).(*maitrev1.Record)
			r.OwnerNodeId, r.Version, r.UpdatedUnixMs = self, 1, time.Now().UnixMilli()
		}
		if err := st.Seed(ctx, r); err != nil {
			return fmt.Errorf("record %s %s: %w", r.GetKind(), r.GetId(), err)
		}
		seeded[r.GetKind()+"/"+r.GetId()] = true
	}
	now := time.Now().UnixMilli()
	fleet := plan.Fleet
	if fleet.GetCreatedUnixMs() == 0 {
		fleet = &maitrev1.Fleet{FleetId: fleet.GetFleetId(), Name: fleet.GetName(), CreatedUnixMs: now}
	}
	if !seeded[store.KindFleet+"/"+fleet.GetFleetId()] {
		if _, err := st.Put(ctx, store.KindFleet, fleet.GetFleetId(), fleet, true); err != nil {
			return err
		}
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
	if len(plan.Records) > 0 {
		return nil
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
	for _, n := range plan.Nodes {
		if n.GetNodeId() == plan.NodeIdentity.Fingerprint() {
			continue
		}
		if _, err := st.Introduce(ctx, n); err != nil {
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
	if !update.LANHost(hostPort) {
		return fmt.Errorf("refusing to pull from %s without TLS: only IP addresses, localhost and .local names may", hostPort)
	}
	dir := "/etc/containers/registries.conf.d"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	conf := fmt.Sprintf("[[registry]]\nlocation = %q\ninsecure = true\n", hostPort)
	return os.WriteFile(filepath.Join(dir, "99-maitre-insecure.conf"), []byte(conf), 0o644)
}
