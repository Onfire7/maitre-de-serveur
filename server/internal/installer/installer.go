// Package installer implements `maitre installer`: the mode the image runs in
// when booted from installer media. It advertises itself on mDNS, serves
// InstallerService to any device, shows a pairing code on the screen, and on
// Provision installs Maître Server to the chosen disk.
package installer

import (
	"context"
	"fmt"
	"github.com/Onfire7/maitre-de-serveur/server/internal/ascii"
	"sync"
	"time"

	"connectrpc.com/connect/v2"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/pairing"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
)

// Options configure an Installer.
type Options struct {
	Version string
	// ImageRef is the container image to install, e.g. ghcr.io/onfire7/maitre-server:1.
	ImageRef string
	// Runner performs the install; swapped for a fake in tests.
	Runner Runner
	// Reboot is called after a successful install.
	Reboot func() error
	// Disks lists candidate disks; defaults to the lsblk-based Disks.
	Disks func(ctx context.Context) ([]*maitrev1.Disk, error)
}

// Installer is the InstallerService handler plus the screen state.
type Installer struct {
	id   *identity.Identity
	opts Options

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
	return &maitrev1.GetInstallerStatusResponse{Status: &maitrev1.InstallerStatus{
		InstallerId:  in.id.Fingerprint(),
		Version:      in.opts.Version,
		Addresses:    Addresses(),
		PairingCode:  code,
		Disks:        disks,
		Provisioning: prov,
		MachineName:  MachineName(),
	}}, nil
}

// Provision implements InstallerService.
func (in *Installer) Provision(ctx context.Context, req *maitrev1.ProvisionRequest) (*maitrev1.ProvisionResponse, error) {
	p, ok := peer.From(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "no client key")
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

func (in *Installer) run(req *maitrev1.ProvisionRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	progress := func(phase maitrev1.Provisioning_Phase, pct uint32, msg string) {
		in.mu.Lock()
		in.prov = &maitrev1.Provisioning{Phase: phase, Percent: pct, Message: msg}
		in.mu.Unlock()
	}
	err := in.opts.Runner.Install(ctx, Plan{
		ImageRef:      in.opts.ImageRef,
		TargetDisk:    req.GetTargetDisk(),
		DataPartition: req.GetDataPartition(),
		Fleet:         req.GetFleet(),
		NodeName:      req.GetNodeName(),
		Trusted:       req.GetTrustedDevices(),
		NodeIdentity:  in.id,
	}, progress)
	if err != nil {
		progress(maitrev1.Provisioning_PHASE_FAILED, 0, err.Error())
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

// Screen returns the text shown on the machine's console (design question 2:
// plain text, read once).
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
