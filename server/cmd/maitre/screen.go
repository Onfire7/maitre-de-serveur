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

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/ascii"
	"github.com/Onfire7/maitre-de-serveur/server/internal/history"
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
func runTUI(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	demo := fs.Bool("demo", false, "show the screen with sample data")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*demo {
		return errors.New("usage: maitre tui --demo (the real screen runs inside `maitre agent`)")
	}
	return tui.Run(context.Background(), demoSource{started: time.Now().Add(-37 * time.Minute)})
}

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
