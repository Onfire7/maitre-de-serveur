package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
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
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/tui"
	"github.com/Onfire7/maitre-de-serveur/server/internal/update"
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
// the screen, and the repairs a wedged Node needs (decisions 101, 325,
// 326). Plain text: the keyboard is the only input, and that is the point.
//
// Decision 326 settled what belongs here rather than on the network. A
// rescue boot has no networking, deliberately, so that nothing can drive
// it — which is exactly what earns it a shell: this menu is reachable only
// by someone standing at the machine with the network down. The dangerous
// remote half (diagnostics over the wire, command execution) lives in a
// debug build instead, and the regular server keeps the basic diagnostics
// that are not a security surface.
//
// The curated actions matter more than the shell. Each one that exists is
// a repair nobody has to improvise at a keyboard in a closet, and "reclaim
// image space" is the one that would have ended the incident of
// 2026-11-04 in two minutes.
func runRecoveryScreen(ctx context.Context, log *slog.Logger, stateDir, node, fleet string, rescue *rescueOps) error {
	// Networking is already off (the agent turns it off before anything
	// else, so a Service that systemd started cannot be pulling while
	// someone works here); this is belt and braces for a direct call.
	if out, err := exec.Command("nmcli", "networking", "off").CombinedOutput(); err != nil {
		log.Warn("networking off failed", "err", err, "out", strings.TrimSpace(string(out)))
	}
	defer exec.Command("nmcli", "networking", "on").Run()
	code, err := recoverymode.NewCode(stateDir)
	if err != nil {
		return err
	}
	draw := func(message string) {
		fmt.Printf("\033[H\033[2J\n%s\n\n  Rescue mode  ·  Node %s in Fleet %q  ·  networking is off\n\n", ascii.Banner(), node, fleet)
		fmt.Println("  This one-time code lets a new Maître Desktop join the Fleet.")
		fmt.Println("  It works for 15 minutes after this machine restarts normally.")
		fmt.Println()
		fmt.Print(ascii.Digits(code[:4]+" "+code[4:], "      "))
		fmt.Printf("\n\n      %s %s\n\n", code[:4], code[4:])
		fmt.Println("  In Maître Desktop choose \"Join with a code from a Node\" and enter it.")
		fmt.Println()
		fmt.Println("  Repairs, for a Node that cannot be reached any other way:")
		fmt.Println("    1  how much room the disks have left")
		fmt.Println("    2  reclaim image space (delete images no Service names)")
		fmt.Println("    3  what the health checks say")
		fmt.Println("    4  roll back to the previous image")
		fmt.Println("    5  a shell (nothing on the network can reach this)")
		fmt.Println("    r  restart this machine normally")
		if message != "" {
			fmt.Printf("\n%s\n", message)
		}
		fmt.Print("\n  Choose, or press Enter to restart: ")
	}
	log.Info("rescue: code issued; menu up")
	message := ""
	for {
		draw(message)
		choice := make(chan string, 1)
		go func() {
			var line string
			fmt.Scanln(&line)
			choice <- strings.ToLower(strings.TrimSpace(line))
		}()
		var in string
		select {
		case <-ctx.Done():
			return nil
		case in = <-choice:
		}
		switch in {
		case "":
			return exec.Command("systemctl", "reboot").Run()
		case "r":
			return exec.Command("systemctl", "reboot").Run()
		case "1":
			message = rescue.Space()
		case "2":
			message = rescue.Prune()
		case "3":
			message = rescue.Health()
		case "4":
			message = rescue.Rollback()
		case "5":
			// Decision 326: a shell, because physical presence with the
			// network down is the one place it costs nothing. Physical
			// access to an unencrypted disk is root anyway, so this takes
			// nothing that was not already there.
			log.Warn("rescue: dropping to a shell")
			sh := exec.Command("/bin/bash", "-l")
			sh.Stdin, sh.Stdout, sh.Stderr = os.Stdin, os.Stdout, os.Stderr
			_ = sh.Run()
			message = "  back from the shell."
		default:
			message = "  " + in + "? Choose 1-5, r, or Enter."
		}
		if message != "" {
			message += "\n  Press Enter to go on."
			var line string
			fmt.Print("\n  Press Enter to go on. ")
			fmt.Scanln(&line)
			message = ""
		}
	}
}

// rescueOps are the repairs the rescue menu offers, each as the one line
// it prints. The agent supplies them; a nil field is a repair this build
// cannot do, which answers with so.
type rescueOps struct {
	Space    func() string
	Prune    func() string
	Health   func() string
	Rollback func() string
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

// rescueRepairs builds the rescue menu's actions (decisions 325, 326).
// Each answers in one line, and each is deliberately something a Node with
// no network and no terminal could not otherwise be made to do.
//
// The Manager here is a minimal one: a rescue boot has not reached the
// agent's own setup, and none of these repairs needs more than this Node's
// own Service records, podman and the filesystem.
func rescueRepairs(ctx context.Context, log *slog.Logger, st *store.Store) *rescueOps {
	mgr := services.New(services.Config{Log: log}, st, nil)
	gib := func(b uint64) float64 { return float64(b) / (1 << 30) }
	return &rescueOps{
		Space: func() string {
			dir := mgr.ImageStoreDir(ctx)
			return fmt.Sprintf("  root: %.1f GiB free. image store (%s): %.1f GiB free.",
				gib(storage.FreeBytes("/")), dir, gib(storage.FreeBytes(dir)))
		},
		Prune: func() string {
			// Decision 316, and the repair the incident of 2026-11-04
			// actually needed. Rollback targets are kept: decision 320
			// wants them, and this is not the place to decide otherwise.
			res, err := mgr.PruneImages(ctx, true)
			if err != nil {
				return "  could not prune: " + err.Error()
			}
			return fmt.Sprintf("  removed %d image(s), kept %d; %.1f GiB back in %s.",
				len(res.GetRemoved()), res.GetKept(),
				gib(res.GetFreeAfterBytes()-res.GetFreeBeforeBytes()), res.GetStoreDir())
		},
		Health: func() string {
			// The checks greenboot runs, run now and reported here: a full
			// root passes the required one, which is how this got so far.
			var out []string
			for _, check := range []string{
				"/etc/greenboot/check/required.d/10-maitre.sh",
				"/etc/greenboot/check/wanted.d/20-maitre-space.sh",
			} {
				b, err := exec.Command(check).CombinedOutput()
				line := strings.TrimSpace(string(b))
				if err != nil {
					line += " [failed: " + err.Error() + "]"
				}
				if line != "" {
					out = append(out, "  "+line)
				}
			}
			if len(out) == 0 {
				return "  the health checks said nothing."
			}
			return strings.Join(out, "\n")
		},
		Rollback: func() string {
			// The previous deployment, which ostree still has: the way back
			// from an image that boots badly.
			if err := update.New(nil, log).Rollback(ctx, false); err != nil {
				return "  could not queue a rollback: " + err.Error()
			}
			return "  the previous image will boot next; restart when ready."
		},
	}
}

// bootedImageSize is how much of the filesystem the running OS image
// takes, which decision 324 reserves 110% of for the next one. bootc
// knows which image is booted; podman knows how big it is. Zero when
// neither can say, and the reserve falls back to its constant.
func bootedImageSize(ctx context.Context, log *slog.Logger) int64 {
	out, err := exec.CommandContext(ctx, "bootc", "status", "--format", "json").Output()
	if err != nil {
		return 0
	}
	var status struct {
		Status struct {
			Booted struct {
				Image struct {
					Image struct {
						Image string `json:"image"`
					} `json:"image"`
				} `json:"image"`
			} `json:"booted"`
		} `json:"status"`
	}
	if err := json.Unmarshal(out, &status); err != nil {
		return 0
	}
	ref := status.Status.Booted.Image.Image.Image
	if ref == "" {
		return 0
	}
	b, err := exec.CommandContext(ctx, "podman", "image", "inspect", "--format", "{{.Size}}", ref).Output()
	if err != nil {
		// The booted image is in ostree's repository, not podman's store,
		// which is the usual case: the constant stands in.
		return 0
	}
	var n int64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &n); err != nil {
		return 0
	}
	log.Debug("booted image size", "image", ref, "bytes", n)
	return n
}
