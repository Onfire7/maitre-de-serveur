// Package installer implements `maitre installer`: the mode the image runs in
// when booted from installer media. It advertises itself on mDNS, serves
// InstallerService to any device, shows a pairing code on the screen, and on
// Provision installs Maître Server to the chosen disk.
package installer

import (
	"context"
	"fmt"
	"github.com/Onfire7/maitre-de-serveur/server/internal/ascii"
	"log/slog"
	"sync"
	"time"

	"connectrpc.com/connect/v2"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/pairing"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/recsig"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Options configure an Installer.
type Options struct {
	Version string
	// ImageRef is the container image to install, e.g. ghcr.io/onfire7/maitre-server:1.
	ImageRef string
	// Source overrides where bootc reads it from (decision 76); empty means
	// the registry.
	Source string
	// InsecureRegistry is the host:port allowed without TLS, for the status.
	InsecureRegistry string
	// Runner performs the install; swapped for a fake in tests.
	Runner Runner
	// Reboot is called after a successful install.
	Reboot func() error
	// Disks lists candidate disks; defaults to the lsblk-based Disks.
	Disks func(ctx context.Context) ([]*maitrev1.Disk, error)
	// Backups finds Node backups on attached Drives; defaults to scanning
	// every partition of every non-installer disk. Nil in the fake runner.
	Backups func(ctx context.Context) []*maitrev1.Backup
	// Folders looks inside partitions for ListFolder (decision 262);
	// defaults to mounting them read-only with the real Disks.
	Folders FolderLister
}

// Installer is the InstallerService handler plus the screen state.
type Installer struct {
	// restoreDevice is the partition holding the backup chosen for restore.
	restoreDevice string
	backupCache   []*maitrev1.Backup
	backupScanned time.Time
	id            *identity.Identity
	opts          Options

	// browseMu orders folder listings before the unmount an install does.
	browseMu sync.RWMutex

	mu       sync.Mutex
	lastPeer string // fingerprint of the most recent caller, for the screen
	code     string
	prov     *maitrev1.Provisioning
	done     chan struct{}
}

var _ maitrev1connect.InstallerServiceHandler = (*Installer)(nil)

// New builds an Installer.
func New(id *identity.Identity, opts Options) *Installer {
	if opts.Runner == nil {
		opts.Runner = &BootcRunner{}
	}
	if opts.Disks == nil {
		opts.Disks = Disks
		if opts.Backups == nil {
			opts.Backups = ScanBackups
		}
		if opts.Folders == nil {
			opts.Folders = NewBrowser()
		}
	}
	return &Installer{
		id:   id,
		opts: opts,
		prov: &maitrev1.Provisioning{Phase: maitrev1.Provisioning_PHASE_IDLE},
		done: make(chan struct{}),
	}
}

// Done is closed once provisioning finished successfully and the machine is
// about to reboot.
func (in *Installer) Done() <-chan struct{} { return in.done }

// GetInstallerStatus implements InstallerService.
func (in *Installer) GetInstallerStatus(ctx context.Context, _ *maitrev1.GetInstallerStatusRequest) (*maitrev1.GetInstallerStatusResponse, error) {
	p, ok := peer.From(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "no client key")
	}
	code := pairing.Code(in.id.PublicKey(), p.PublicKey)
	in.mu.Lock()
	in.lastPeer, in.code = p.Fingerprint, code
	prov := cloneProv(in.prov)
	in.mu.Unlock()

	disks, err := in.opts.Disks(ctx)
	if err != nil {
		disks = nil
	}
	backups := in.backups(ctx)
	return &maitrev1.GetInstallerStatusResponse{Status: &maitrev1.InstallerStatus{
		Backups:          backups,
		InstallerId:      in.id.Fingerprint(),
		Version:          in.opts.Version,
		Addresses:        Addresses(),
		PairingCode:      code,
		Disks:            disks,
		Provisioning:     prov,
		MachineName:      MachineName(),
		Image:            in.opts.ImageRef,
		Source:           in.opts.Source,
		InsecureRegistry: in.opts.InsecureRegistry,
	}}, nil
}

// Provision implements InstallerService.
func (in *Installer) Provision(ctx context.Context, req *maitrev1.ProvisionRequest) (*maitrev1.ProvisionResponse, error) {
	p, ok := peer.From(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "no client key")
	}
	if req.GetRestoreNodeId() != "" {
		var b *maitrev1.Backup
		for _, x := range in.backups(ctx) {
			if x.GetNodeId() == req.GetRestoreNodeId() {
				b = x
			}
		}
		if b == nil {
			return nil, connect.Errorf(connect.CodeInvalidArgument, "no backup of Node %s on any Drive", req.GetRestoreNodeId())
		}
		if b.GetDisk() == req.GetTargetDisk() {
			return nil, connect.NewError(connect.CodeFailedPrecondition, "the backup sits on the disk you chose to install to; pick another")
		}
		req.NodeName = b.GetNodeName()
		req.Fleet = &maitrev1.Fleet{FleetId: b.GetFleetId(), Name: b.GetFleetName()}
		in.mu.Lock()
		in.restoreDevice = b.GetDevice()
		in.mu.Unlock()
	}
	if req.GetFleet().GetFleetId() == "" || req.GetNodeName() == "" || req.GetTargetDisk() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "fleet, node_name and target_disk are required")
	}
	// The caller must be in the trust list it is installing, otherwise the
	// new Node would refuse the very device that set it up.
	callerIncluded := false
	for _, d := range req.GetTrustedDevices() {
		pub, err := identity.ParsePublicKeyPEM([]byte(d.GetPublicKeyPem()))
		if err != nil {
			return nil, connect.Errorf(connect.CodeInvalidArgument, "trusted device %q: %v", d.GetName(), err)
		}
		if identity.Fingerprint(pub) == p.Fingerprint {
			callerIncluded = true
		}
	}
	if !callerIncluded {
		return nil, connect.NewError(connect.CodeInvalidArgument, "trusted_devices must include the calling device")
	}
	// Decision 189: signed records are stored as they are; the caller
	// paired by code vouches for them, but each must be intact and the
	// caller must be among the Trusted Devices they carry.
	if recs := req.GetRecords(); len(recs) > 0 {
		callerSigned := false
		for _, r := range recs {
			if _, err := recsig.Verify(r); err != nil {
				return nil, connect.Errorf(connect.CodeInvalidArgument, "record %s %s: %v", r.GetKind(), r.GetId(), err)
			}
			callerSigned = callerSigned || (r.GetKind() == store.KindTrustedDevice && r.GetId() == p.Fingerprint && !r.GetDeleted())
		}
		if !callerSigned {
			return nil, connect.NewError(connect.CodeInvalidArgument, "records must include the calling device")
		}
	}
	disks, err := in.opts.Disks(ctx)
	if err != nil {
		return nil, connect.Errorf(connect.CodeInternal, "%v", err)
	}
	var target *maitrev1.Disk
	for _, d := range disks {
		if d.GetPath() == req.GetTargetDisk() {
			target = d
		}
	}
	if target == nil {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "no such disk %q", req.GetTargetDisk())
	}
	if target.GetIsInstallerMedia() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "cannot install onto the installer media")
	}

	in.mu.Lock()
	if in.prov.GetPhase() != maitrev1.Provisioning_PHASE_IDLE && in.prov.GetPhase() != maitrev1.Provisioning_PHASE_FAILED {
		in.mu.Unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, "provisioning already in progress")
	}
	in.prov = &maitrev1.Provisioning{Phase: maitrev1.Provisioning_PHASE_INSTALLING, Message: "starting"}
	in.mu.Unlock()

	go in.run(req)
	return &maitrev1.ProvisionResponse{}, nil
}

// ListFolder implements InstallerService (decision 262).
func (in *Installer) ListFolder(ctx context.Context, req *maitrev1.ListFolderRequest) (*maitrev1.ListFolderResponse, error) {
	if _, ok := peer.From(ctx); !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "no client key")
	}
	return in.Folder(ctx, req.GetDevice(), req.GetPath())
}

// Restart implements InstallerService (decision 270): reboots the machine,
// unless an install is under way. Open to any device on the network, as
// Provision is: a machine nothing is installed on yet loses nothing.
func (in *Installer) Restart(ctx context.Context, _ *maitrev1.RestartRequest) (*maitrev1.RestartResponse, error) {
	if _, ok := peer.From(ctx); !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "no client key")
	}
	if in.Installing() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "installing; the machine restarts by itself when done")
	}
	if in.opts.Reboot == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "this Installer cannot restart itself")
	}
	slog.Info("restart asked for over the network")
	go func() {
		// Let the answer reach the caller first.
		time.Sleep(time.Second)
		if err := in.opts.Reboot(); err != nil {
			slog.Error("restart", "err", err)
		}
	}()
	return &maitrev1.RestartResponse{}, nil
}

// Folder lists one folder of a partition, for ListFolder and the machine's
// own screen.
func (in *Installer) Folder(ctx context.Context, device, dir string) (*maitrev1.ListFolderResponse, error) {
	if in.opts.Folders == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "this Installer cannot look inside disks")
	}
	// Held while listing, so run's unmount waits for a listing under way
	// and every later one sees the install.
	in.browseMu.RLock()
	defer in.browseMu.RUnlock()
	if in.Installing() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "installing; disks cannot be opened now")
	}
	p, err := in.Partition(ctx, device)
	if err != nil {
		return nil, err
	}
	resp, err := in.opts.Folders.List(ctx, p, dir)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
	}
	return resp, nil
}

// Partition finds device among the partitions of the disks that are not
// the installer media; nothing else may be opened.
func (in *Installer) Partition(ctx context.Context, device string) (*maitrev1.Partition, error) {
	disks, err := in.opts.Disks(ctx)
	if err != nil {
		return nil, connect.Errorf(connect.CodeInternal, "%v", err)
	}
	for _, d := range disks {
		for _, p := range d.GetPartitions() {
			if p.GetDevice() != device {
				continue
			}
			if d.GetIsInstallerMedia() {
				return nil, connect.NewError(connect.CodeFailedPrecondition, "that is the installer media")
			}
			return p, nil
		}
	}
	return nil, connect.Errorf(connect.CodeInvalidArgument, "no partition %q on this machine's disks", device)
}

// Installing reports an install under way, when disks are not to be opened.
func (in *Installer) Installing() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	switch in.prov.GetPhase() {
	case maitrev1.Provisioning_PHASE_INSTALLING, maitrev1.Provisioning_PHASE_CONFIGURING, maitrev1.Provisioning_PHASE_REBOOTING:
		return true
	}
	return false
}

func (in *Installer) run(req *maitrev1.ProvisionRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	// Nothing looked at may stay mounted: bootc wipes the target disk.
	in.browseMu.Lock()
	if in.opts.Folders != nil {
		in.opts.Folders.Close()
	}
	in.browseMu.Unlock()
	progress := func(phase maitrev1.Provisioning_Phase, pct uint32, msg string) {
		in.mu.Lock()
		in.prov = &maitrev1.Provisioning{Phase: phase, Percent: pct, Message: msg}
		in.mu.Unlock()
	}
	fail := func(err error) {
		slog.Error("install failed", "err", err)
		in.mu.Lock()
		in.prov = &maitrev1.Provisioning{Phase: maitrev1.Provisioning_PHASE_FAILED, Message: err.Error(), Output: Output(err)}
		in.mu.Unlock()
	}
	err := in.opts.Runner.Install(ctx, Plan{
		ImageRef:      in.opts.ImageRef,
		Source:        in.opts.Source,
		TargetDisk:    req.GetTargetDisk(),
		DataPartition: req.GetDataPartition(),
		Fleet:         req.GetFleet(),
		NodeName:      req.GetNodeName(),
		Nodes:         req.GetNodes(),
		Trusted:       req.GetTrustedDevices(),
		Records:       req.GetRecords(),
		NodeIdentity:  in.id,
		RestoreDevice: in.restoreDevice,
		RestoreNodeID: req.GetRestoreNodeId(),
	}, progress)
	if err != nil {
		fail(err)
		return
	}
	progress(maitrev1.Provisioning_PHASE_REBOOTING, 100, "installed; rebooting")
	close(in.done)
	if in.opts.Reboot != nil {
		// Give the Console a moment to read the final status.
		time.Sleep(3 * time.Second)
		_ = in.opts.Reboot()
	}
}

// Shown is what the screen shows of the pairing: the code for the device
// that asked last, and the install's progress.
func (in *Installer) Shown() (code string, prov *maitrev1.Provisioning) {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.code, cloneProv(in.prov)
}

// Options returns what the Installer was built with.
func (in *Installer) Options() Options { return in.opts }

// Screen returns the text shown on the machine's console when it has no
// keyboard to drive the full screen (design question 2: plain text, read
// once).
func (in *Installer) Screen() string {
	in.mu.Lock()
	defer in.mu.Unlock()
	var b []byte
	w := func(f string, a ...any) { b = append(b, fmt.Sprintf(f+"\n", a...)...) }
	w("")
	w("%s", ascii.Banner())
	w("")
	w("  Maître Installer  %s", in.opts.Version)
	w("  ─────────────────────────────────────────────")
	addrs := Addresses()
	if len(addrs) == 0 {
		w("  Network:       waiting for a cable and an address...")
	} else {
		w("  Network:       %v", addrs)
	}
	w("  This machine:  %s", MachineName())
	w("  Identity:      %s", in.id.Fingerprint()[:16])
	w("")
	switch in.prov.GetPhase() {
	case maitrev1.Provisioning_PHASE_IDLE, maitrev1.Provisioning_PHASE_UNSPECIFIED:
		if in.code == "" {
			w("  Open Maître Desktop on a computer on this network.")
			w("  It will find this machine automatically.")
		} else {
			w("  Pairing code:")
			w("")
			w("%s", ascii.Digits(spaced(in.code), "      "))
			w("")
			w("  Confirm in Maître Desktop that it shows the same code (%s).", in.code)
		}
	case maitrev1.Provisioning_PHASE_FAILED:
		w("  Install FAILED: %s", in.prov.GetMessage())
		w("  Fix the problem in Maître Desktop and try again.")
	default:
		w("  Installing... %3d%%  %s", in.prov.GetPercent(), in.prov.GetMessage())
		w("  Do not power off.")
	}
	return string(b)
}

// spaced puts a gap in the middle of a six-digit code: "123 456".
func spaced(code string) string {
	if len(code) == 6 {
		return code[:3] + " " + code[3:]
	}
	return code
}

func cloneProv(p *maitrev1.Provisioning) *maitrev1.Provisioning {
	return &maitrev1.Provisioning{Phase: p.GetPhase(), Message: p.GetMessage(), Percent: p.GetPercent()}
}

// backups scans attached Drives for Node backups (decision 102), at most
// once a minute: it mounts partitions, which is slow.
func (in *Installer) backups(ctx context.Context) []*maitrev1.Backup {
	in.mu.Lock()
	if time.Since(in.backupScanned) < time.Minute {
		b := in.backupCache
		in.mu.Unlock()
		return b
	}
	in.mu.Unlock()
	// Like a listing: a scan under way finishes, and unmounts, before the
	// install starts, and none mounts anything while it runs (decision 268).
	in.browseMu.RLock()
	defer in.browseMu.RUnlock()
	if in.Installing() {
		in.mu.Lock()
		defer in.mu.Unlock()
		return in.backupCache
	}
	var found []*maitrev1.Backup
	if in.opts.Backups != nil {
		found = in.opts.Backups(ctx)
	}
	in.mu.Lock()
	in.backupCache, in.backupScanned = found, time.Now()
	in.mu.Unlock()
	return found
}
