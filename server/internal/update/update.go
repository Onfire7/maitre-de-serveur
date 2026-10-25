// Package update drives bootc on a Node: report the booted, staged and
// rollback images, stage a new image (bootc switch), roll back, reboot.
// Decision 82: updates are staged; the Console decides whether to reboot
// right away.
package update

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// Runner executes bootc and systemctl; swapped for a fake in tests.
type Runner interface {
	// Run executes a command and returns its combined output.
	Run(ctx context.Context, name string, args ...string) (string, error)
	// Switch runs `bootc switch` streaming progress events (bootc
	// --progress-fd) to progress until the command exits.
	Switch(ctx context.Context, image string, progress func(Progress)) error
}

// Progress is one event from bootc's --progress-fd stream. Only the fields
// the Console shows are decoded.
type Progress struct {
	Type        string `json:"type"`
	Task        string `json:"task"`
	Description string `json:"description"`
	Bytes       uint64 `json:"bytes"`
	BytesTotal  uint64 `json:"bytes_total"`
	Steps       uint64 `json:"steps"`
	StepsTotal  uint64 `json:"steps_total"`
}

// Manager serialises update operations on one Node.
type Manager struct {
	run Runner
	log *slog.Logger
	// RegistriesDir receives insecure-registry drop-ins
	// (default /etc/containers/registries.conf.d).
	RegistriesDir string

	mu       sync.Mutex
	state    maitrev1.UpdateStatus_State
	message  string
	progress float64
	target   string
}

// New builds a Manager. A nil Runner means the real one.
func New(run Runner, log *slog.Logger) *Manager {
	if run == nil {
		run = ExecRunner{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Manager{run: run, log: log, RegistriesDir: "/etc/containers/registries.conf.d", state: maitrev1.UpdateStatus_STATE_IDLE}
}

// Status reports bootc's view plus any update in flight.
func (m *Manager) Status(ctx context.Context) (*maitrev1.UpdateStatus, error) {
	out, err := m.run.Run(ctx, "bootc", "status", "--format", "json")
	st := &maitrev1.UpdateStatus{Supported: true}
	if err != nil {
		return nil, fmt.Errorf("bootc status: %w", err)
	}
	if err := ParseStatus(strings.NewReader(out), st); err != nil {
		return nil, err
	}
	m.mu.Lock()
	st.State, st.Message, st.Progress, st.Target = m.state, m.message, m.progress, m.target
	m.mu.Unlock()
	return st, nil
}

// Busy reports whether an update is running.
func (m *Manager) Busy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state == maitrev1.UpdateStatus_STATE_UPDATING || m.state == maitrev1.UpdateStatus_STATE_REBOOTING
}

// ErrBusy is returned when an update is already running.
var ErrBusy = errors.New("an update is already in progress")

// Start stages image in the background. When reboot is set the Node reboots
// once staging succeeds. insecure marks the image's registry host as plain
// HTTP / untrusted TLS before pulling.
func (m *Manager) Start(image string, reboot, insecure bool) error {
	image = strings.TrimSpace(image)
	if image == "" {
		return errors.New("image reference is empty")
	}
	m.mu.Lock()
	if m.state == maitrev1.UpdateStatus_STATE_UPDATING || m.state == maitrev1.UpdateStatus_STATE_REBOOTING {
		m.mu.Unlock()
		return ErrBusy
	}
	m.state, m.message, m.progress, m.target = maitrev1.UpdateStatus_STATE_UPDATING, "starting", 0, image
	m.mu.Unlock()
	go m.stage(image, reboot, insecure)
	return nil
}

func (m *Manager) set(state maitrev1.UpdateStatus_State, msg string, progress float64) {
	m.mu.Lock()
	m.state, m.message, m.progress = state, msg, progress
	m.mu.Unlock()
}

func (m *Manager) stage(image string, reboot, insecure bool) {
	ctx := context.Background()
	if insecure {
		if err := m.allowInsecure(image); err != nil {
			m.set(maitrev1.UpdateStatus_STATE_FAILED, err.Error(), 0)
			return
		}
	} else {
		_ = os.Remove(filepath.Join(m.RegistriesDir, InsecureDropIn))
	}
	if !refRE.MatchString(image) {
		m.set(maitrev1.UpdateStatus_STATE_FAILED, fmt.Sprintf("%q is not an image reference", image), 0)
		return
	}
	m.log.Info("update: staging", "image", image, "reboot", reboot)
	err := m.run.Switch(ctx, image, func(p Progress) {
		var frac float64
		switch {
		case p.BytesTotal > 0:
			frac = float64(p.Bytes) / float64(p.BytesTotal)
		case p.StepsTotal > 0:
			frac = float64(p.Steps) / float64(p.StepsTotal)
		}
		msg := p.Description
		if msg == "" {
			msg = p.Task
		}
		if p.BytesTotal > 0 {
			msg = fmt.Sprintf("%s (%s of %s)", msg, humanBytes(p.Bytes), humanBytes(p.BytesTotal))
		}
		m.set(maitrev1.UpdateStatus_STATE_UPDATING, msg, frac)
	})
	if err != nil {
		m.log.Error("update failed", "image", image, "err", err)
		m.set(maitrev1.UpdateStatus_STATE_FAILED, err.Error(), 0)
		return
	}
	if !reboot {
		m.log.Info("update: staged, reboot pending", "image", image)
		m.set(maitrev1.UpdateStatus_STATE_IDLE, "staged; reboot to apply", 1)
		return
	}
	m.set(maitrev1.UpdateStatus_STATE_REBOOTING, "staged; rebooting", 1)
	if err := m.Reboot(ctx); err != nil {
		m.set(maitrev1.UpdateStatus_STATE_FAILED, "staged, but reboot failed: "+err.Error(), 1)
	}
}

// Rollback queues the previous image for the next boot.
func (m *Manager) Rollback(ctx context.Context, reboot bool) error {
	if m.Busy() {
		return ErrBusy
	}
	if _, err := m.run.Run(ctx, "bootc", "rollback"); err != nil {
		return fmt.Errorf("bootc rollback: %w", err)
	}
	m.set(maitrev1.UpdateStatus_STATE_IDLE, "rollback queued; reboot to apply", 0)
	if reboot {
		m.set(maitrev1.UpdateStatus_STATE_REBOOTING, "rollback queued; rebooting", 0)
		return m.Reboot(ctx)
	}
	return nil
}

// Reboot restarts the Node.
func (m *Manager) Reboot(ctx context.Context) error {
	m.log.Info("rebooting")
	if _, err := m.run.Run(ctx, "systemctl", "reboot"); err != nil {
		return fmt.Errorf("reboot: %w", err)
	}
	return nil
}

// allowInsecure writes a containers-registries.conf drop-in marking the
// image's registry host as insecure, so bootc (via skopeo) accepts plain
// HTTP from a local Depot.
func (m *Manager) allowInsecure(image string) error {
	host := RegistryHost(image)
	if host == "" {
		return fmt.Errorf("cannot find a registry host in %q", image)
	}
	if !LANHost(host) {
		return fmt.Errorf("refusing to mark %s insecure: only IP addresses, localhost and .local names may skip TLS", host)
	}
	if err := os.MkdirAll(m.RegistriesDir, 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf("# Written by maitre: Depot %s speaks plain HTTP on the LAN. Replaced on every update.\n[[registry]]\nlocation = %q\ninsecure = true\n", host, host)
	return os.WriteFile(filepath.Join(m.RegistriesDir, InsecureDropIn), []byte(body), 0o644)
}

// InsecureDropIn is the single registries.conf.d file an insecure Depot
// occupies, so there is never more than one such host and a secure update
// removes it.
const InsecureDropIn = "maitre-depot.conf"

// LANHost reports whether host (host or host:port) is an IP literal,
// localhost, or a .local name: the only registries allowed without TLS.
func LANHost(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" || strings.HasSuffix(h, ".local") {
		return true
	}
	return net.ParseIP(h) != nil
}

// refRE is a conservative image reference: registry, path, tag and/or digest.
var refRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)*(:[A-Za-z0-9_][A-Za-z0-9._-]{0,127})?(@sha256:[a-f0-9]{64})?$`)

// ValidRef reports whether s looks like an image reference and cannot be
// mistaken for a command-line option or a template fragment.
func ValidRef(s string) bool { return refRE.MatchString(s) }

// RegistryHost returns the registry part of an image reference, or "" when
// the first path component is not a host (plain docker.io short names).
func RegistryHost(image string) string {
	image = strings.TrimPrefix(image, "docker://")
	first, _, ok := strings.Cut(image, "/")
	if !ok {
		return ""
	}
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first
	}
	return ""
}

// bootcStatus mirrors the parts of `bootc status --format json` we read.
type bootcStatus struct {
	Status struct {
		Booted         *bootcDeployment `json:"booted"`
		Staged         *bootcDeployment `json:"staged"`
		Rollback       *bootcDeployment `json:"rollback"`
		RollbackQueued bool             `json:"rollbackQueued"`
	} `json:"status"`
}

type bootcDeployment struct {
	Image *struct {
		Image struct {
			Image string `json:"image"`
		} `json:"image"`
		Version     string `json:"version"`
		Timestamp   string `json:"timestamp"`
		ImageDigest string `json:"imageDigest"`
	} `json:"image"`
}

func (d *bootcDeployment) proto() *maitrev1.ImageDeployment {
	if d == nil || d.Image == nil {
		return nil
	}
	return &maitrev1.ImageDeployment{
		Image:     d.Image.Image.Image,
		Digest:    d.Image.ImageDigest,
		Version:   d.Image.Version,
		Timestamp: d.Image.Timestamp,
	}
}

// ParseStatus fills the deployment fields of st from bootc status JSON.
func ParseStatus(r io.Reader, st *maitrev1.UpdateStatus) error {
	var bs bootcStatus
	if err := json.NewDecoder(r).Decode(&bs); err != nil {
		return fmt.Errorf("bootc status: %w", err)
	}
	st.Booted = bs.Status.Booted.proto()
	st.Staged = bs.Status.Staged.proto()
	st.Rollback = bs.Status.Rollback.proto()
	st.RollbackQueued = bs.Status.RollbackQueued
	return nil
}

// ExecRunner runs the real commands.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Switch implements Runner: `bootc switch --progress-fd 3 <image>` with the
// progress pipe passed as fd 3.
func (ExecRunner) Switch(ctx context.Context, image string, progress func(Progress)) error {
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "bootc", "switch", "--quiet", "--progress-fd", "3", "--", image)
	cmd.ExtraFiles = []*os.File{pw}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return err
	}
	pw.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ReadProgress(pr, progress)
	}()
	werr := cmd.Wait()
	pr.Close()
	<-done
	if werr != nil {
		return fmt.Errorf("bootc switch %s: %w: %s", image, werr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// ReadProgress decodes newline-delimited JSON progress events until EOF.
func ReadProgress(r io.Reader, fn func(Progress)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var p Progress
		if json.Unmarshal(sc.Bytes(), &p) == nil && fn != nil {
			fn(p)
		}
	}
}

func humanBytes(n uint64) string {
	const unit = 1024
	switch {
	case n >= unit*unit*unit:
		return fmt.Sprintf("%.1f GiB", float64(n)/(unit*unit*unit))
	case n >= unit*unit:
		return fmt.Sprintf("%.0f MiB", float64(n)/(unit*unit))
	default:
		return fmt.Sprintf("%.0f KiB", float64(n)/unit)
	}
}
