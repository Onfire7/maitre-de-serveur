package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os/exec"
	"strings"
	"time"

	"connectrpc.com/connect/v2"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/ascii"
	"github.com/Onfire7/maitre-de-serveur/server/internal/history"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/installer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/recoverymode"
	"github.com/Onfire7/maitre-de-serveur/server/internal/tui"
)

// agentSource feeds the Node's screen from the running agent.
type agentSource struct {
	status   func() tui.Status
	metrics  func() *maitrev1.NodeMetrics
	history  *history.Store
	stateDir string
}

// History reads the stored metrics for the charts (decision 211).
func (a *agentSource) History(names []string, from, to time.Time, points int) ([]*maitrev1.MetricSeries, error) {
	if a.history == nil {
		return nil, errors.New("no metrics history")
	}
	return a.history.Query(names, from.UnixMilli(), to.UnixMilli(), points)
}

func (a *agentSource) Status() tui.Status { return a.status() }
func (a *agentSource) Metrics() *maitrev1.NodeMetrics {
	if a.metrics == nil {
		return nil
	}
	return a.metrics()
}
func (a *agentSource) Containers(ctx context.Context) ([]tui.Container, error) {
	return tui.PodmanContainers(ctx)
}
func (a *agentSource) Units() []string {
	units := []string{"maitre-agent.service"}
	for _, s := range a.status().Services {
		units = append(units, s+".service")
	}
	return units
}
func (a *agentSource) Logs(ctx context.Context, unit string, n int) ([]string, error) {
	return tui.JournalLogs(ctx, unit, n)
}
func (a *agentSource) Reboot() error { return exec.Command("systemctl", "reboot").Run() }

// RebootRecovery asks the next boot to enter recovery (decision 101).
func (a *agentSource) RebootRecovery() error {
	if err := recoverymode.Request(a.stateDir); err != nil {
		return err
	}
	return exec.Command("systemctl", "reboot").Run()
}

// runRecoveryScreen is the recovery session: networking off, one code on
// the screen, Enter restarts. Plain text: the keyboard is the only input.
func runRecoveryScreen(ctx context.Context, log *slog.Logger, stateDir, node, fleet string) error {
	if out, err := exec.Command("nmcli", "networking", "off").CombinedOutput(); err != nil {
		log.Warn("networking off failed", "err", err, "out", strings.TrimSpace(string(out)))
	}
	defer exec.Command("nmcli", "networking", "on").Run()
	code, err := recoverymode.NewCode(stateDir)
	if err != nil {
		return err
	}
	fmt.Printf("\033[H\033[2J\n%s\n\n  Recovery mode  ·  Node %s in Fleet %q  ·  networking is off\n\n", ascii.Banner(), node, fleet)
	fmt.Println("  This one-time code lets a new Maître Desktop join the Fleet.")
	fmt.Println("  It works for 15 minutes after this machine restarts normally.")
	fmt.Println()
	fmt.Print(ascii.Digits(code[:4]+" "+code[4:], "      "))
	fmt.Printf("\n\n      %s %s\n\n", code[:4], code[4:])
	fmt.Println("  In Maître Desktop choose \"Join with a code from a Node\" and enter it.")
	fmt.Println("  Press Enter to restart this machine now.")
	log.Info("recovery: code issued; waiting for Enter")
	done := make(chan struct{})
	go func() {
		var line string
		fmt.Scanln(&line)
		close(done)
	}()
	select {
	case <-ctx.Done():
		return nil
	case <-done:
	}
	return exec.Command("systemctl", "reboot").Run()
}

// plainStatusPage is the screen without a keyboard (stdout is not a
// terminal): the Status page, redrawn every two seconds.
func plainStatusPage(ctx context.Context, src tui.Source) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		s := src.Status()
		var b strings.Builder
		fmt.Fprintf(&b, "\n%s\n\n  Maître Server %s\n  ─────────────────────────────────────────────\n", ascii.Banner(), s.Version)
		fmt.Fprintf(&b, "  Node:          %s   in Fleet %q\n", s.Node, s.Fleet)
		if len(s.Addresses) == 0 {
			b.WriteString("  Network:       waiting for a cable and an address...\n")
		} else {
			fmt.Fprintf(&b, "  Network:       %s\n", strings.Join(s.Addresses, ", "))
		}
		fmt.Fprintf(&b, "  Discovery:     %s\n  Trusted:       %d device(s)\n", s.Discovery, s.Trusted)
		if len(s.Services) == 0 {
			b.WriteString("  Services:      none yet\n")
		} else {
			fmt.Fprintf(&b, "  Services:      %s\n", strings.Join(s.Services, ", "))
		}
		fmt.Fprintf(&b, "  Up:            %s\n\n  Open Maître Desktop on a computer on this network to manage it.\n", time.Since(s.Started).Round(time.Second))
		fmt.Print("\033[H\033[2J" + b.String())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// runTUI is `maitre tui --demo`: the Node's screen with made-up data, to
// work on it from a workstation terminal.
// installerSource feeds the Installer's screen (decision 261).
type installerSource struct {
	in         *installer.Installer
	id         *identity.Identity
	sourceNote string
}

func (s *installerSource) Status() tui.InstallerStatus {
	code, prov := s.in.Shown()
	return tui.InstallerStatus{Version: version, Machine: installer.MachineName(), Identity: s.id.Fingerprint(), Addresses: installer.Addresses(), Image: s.in.Options().ImageRef, Source: s.sourceNote, Code: code, Provisioning: prov}
}
func (s *installerSource) Disks(ctx context.Context) ([]*maitrev1.Disk, error) {
	return s.in.Options().Disks(ctx)
}
func (s *installerSource) Folder(ctx context.Context, device, dir string) (*maitrev1.ListFolderResponse, error) {
	r, err := s.in.Folder(ctx, device, dir)
	if ce := new(connect.Error); errors.As(err, &ce) {
		return nil, errors.New(ce.Message())
	}
	return r, err
}
func (s *installerSource) Logs(ctx context.Context, n int) ([]string, error) {
	return tui.JournalLogs(ctx, "maitre-installer.service", n)
}
func (s *installerSource) Reboot() error { return exec.Command("systemctl", "reboot").Run() }

func runTUI(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	demo := fs.Bool("demo", false, "show the screen with sample data")
	demoInstaller := fs.String("demo-installer", "", "show the Installer's screen with sample disks: waiting, pairing, installing or failed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *demoInstaller != "" {
		return tui.RunInstaller(context.Background(), demoInstallerSource(*demoInstaller))
	}
	if !*demo {
		return errors.New("usage: maitre tui --demo | --demo-installer <state> (the real screens run inside `maitre agent` and `maitre installer`)")
	}
	return tui.Run(context.Background(), demoSource{started: time.Now().Add(-37 * time.Minute)})
}

// demoInstallerSource shows the fake disks and folders in one state.
type demoInstallerSource string

func (d demoInstallerSource) Status() tui.InstallerStatus {
	st := tui.InstallerStatus{Version: version, Machine: "OptiPlex 7080", Identity: "7d92aeee61d9abbdd989ed787ee03d8e", Addresses: []string{"192.168.4.81"}, Image: "ghcr.io/onfire7/maitre-server:stable", Source: "this media already holds the current image; no download", Provisioning: &maitrev1.Provisioning{Phase: maitrev1.Provisioning_PHASE_IDLE}}
	switch d {
	case "pairing":
		st.Code = "482913"
	case "installing":
		st.Code = "482913"
		st.Provisioning = &maitrev1.Provisioning{Phase: maitrev1.Provisioning_PHASE_INSTALLING, Percent: 42, Message: "writing the image to /dev/nvme0n1"}
	case "failed":
		st.Code = "482913"
		st.Provisioning = &maitrev1.Provisioning{Phase: maitrev1.Provisioning_PHASE_FAILED, Message: "bootc install: no room for the config partition after the 32 GB root"}
	}
	return st
}
func (demoInstallerSource) Disks(ctx context.Context) ([]*maitrev1.Disk, error) {
	return installer.FakeDisks(ctx)
}
func (demoInstallerSource) Folder(ctx context.Context, device, dir string) (*maitrev1.ListFolderResponse, error) {
	disks, _ := installer.FakeDisks(ctx)
	for _, dk := range disks {
		for _, p := range dk.GetPartitions() {
			if p.GetDevice() == device {
				return installer.FakeFolders{}.List(ctx, p, dir)
			}
		}
	}
	return nil, fmt.Errorf("no partition %s", device)
}
func (demoInstallerSource) Logs(context.Context, int) ([]string, error) {
	return []string{"2026-10-31T10:02:11+00:00 maitre[812]: level=INFO msg=\"maitre installer\" image=ghcr.io/onfire7/maitre-server:stable", "2026-10-31T10:02:11+00:00 maitre[812]: level=INFO msg=listening addr=0.0.0.0:7443"}, nil
}
func (demoInstallerSource) Reboot() error { return tui.ErrQuit }

type demoSource struct{ started time.Time }

func (d demoSource) Status() tui.Status {
	return tui.Status{Version: version, Node: "demo", Fleet: "Home", Addresses: []string{"192.168.4.80"}, Discovery: "announced as demo", Trusted: 2, Services: []string{"jellyfin", "uptime-kuma"}, Started: d.started}
}
func (demoSource) Metrics() *maitrev1.NodeMetrics {
	cpu := 20 + 60*math.Abs(math.Sin(float64(time.Now().Unix())/7))
	return &maitrev1.NodeMetrics{CpuPercent: cpu, MemoryTotalBytes: 16 << 30, MemoryUsedBytes: 5 << 30, PoolTotalBytes: 2 << 40, PoolUsedBytes: 700 << 30, NetRxBytesPerSec: 1.2e6, NetTxBytesPerSec: 3.4e5, TemperatureC: 48, Load_1M: 0.71}
}

// History makes up a day: busy evenings, a quiet night, a gap where the
// Node was off.
func (demoSource) History(names []string, from, to time.Time, _ int) ([]*maitrev1.MetricSeries, error) {
	step := int64(10_000)
	if to.Sub(from) > 6*time.Hour {
		step = 60_000
	}
	start := from.UnixMilli() - from.UnixMilli()%step
	var out []*maitrev1.MetricSeries
	for _, name := range names {
		s := &maitrev1.MetricSeries{Name: name, StartUnixMs: start, StepMs: step}
		for t := start; t < to.UnixMilli(); t += step {
			h := float64(t%86_400_000) / 3_600_000
			v := 25 + 20*math.Sin(h/24*2*math.Pi) + 10*math.Abs(math.Sin(float64(t)/4e6))
			if name == "node/memory" {
				v = 32 + 3*math.Sin(h/5)
			}
			if age := time.Since(time.UnixMilli(t)); age > 3*time.Hour && age < 4*time.Hour {
				v = math.NaN()
			}
			s.Avg, s.Max = append(s.Avg, v), append(s.Max, v)
		}
		out = append(out, s)
	}
	return out, nil
}

func (demoSource) Containers(context.Context) ([]tui.Container, error) {
	return []tui.Container{
		{Name: "jellyfin", State: "running", Status: "Up 2 hours (healthy)", Image: "docker.io/jellyfin/jellyfin:10.10.7", CPUPct: 18.2, MemBytes: 900 << 20},
		{Name: "uptime-kuma", State: "running", Status: "Up 2 hours", Image: "docker.io/louislam/uptime-kuma:1", CPUPct: 0.4, MemBytes: 120 << 20, Restarts: 1},
		{Name: "adguard-home", State: "exited", Status: "Exited (1) 5 minutes ago", Image: "docker.io/adguard/adguardhome:v0.107", Restarts: 3},
	}, nil
}
func (demoSource) Units() []string {
	return []string{"maitre-agent.service", "jellyfin.service", "uptime-kuma.service", "adguard-home.service"}
}
func (demoSource) Logs(_ context.Context, unit string, _ int) ([]string, error) {
	var out []string
	for i := 1; i <= 40; i++ {
		out = append(out, fmt.Sprintf("2026-10-25T08:%02d:00-0500 demo %s: sample log line %d", i, unit, i))
	}
	return out, nil
}
func (demoSource) Reboot() error         { return tui.ErrQuit }
func (demoSource) RebootRecovery() error { return tui.ErrQuit }
