package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

type fakeSource struct{}

func (fakeSource) Status() Status {
	return Status{Version: "test", Node: "pxe", Fleet: "Dev", Addresses: []string{"192.168.4.80"}, Discovery: "announced as pxe", Trusted: 1, Services: []string{"uptime-kuma"}, Started: time.Now().Add(-90 * time.Second)}
}
func (fakeSource) Metrics() *maitrev1.NodeMetrics {
	return &maitrev1.NodeMetrics{CpuPercent: 12.5, MemoryTotalBytes: 8 << 30, MemoryUsedBytes: 3 << 30, PoolTotalBytes: 100 << 30, PoolUsedBytes: 20 << 30, Load_1M: 0.42}
}
func (fakeSource) Containers(context.Context) ([]Container, error) {
	return []Container{{Name: "uptime-kuma", State: "running", CPUPct: 1.5, MemBytes: 80 << 20, Image: "louislam/uptime-kuma:1"}, {Name: "jellyfin", State: "exited", CPUPct: 0, MemBytes: 0, Image: "jellyfin/jellyfin:10"}}, nil
}
func (fakeSource) Units() []string { return []string{"maitre-agent.service", "uptime-kuma.service"} }
func (fakeSource) Logs(_ context.Context, unit string, _ int) ([]string, error) {
	return []string{"line one of " + unit, "line two"}, nil
}
func (fakeSource) Reboot() error { return nil }

func TestPagesRender(t *testing.T) {
	m := newModel(fakeSource{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	v := m.View()
	for _, want := range []string{"Node", "test", "pxe", "192.168.4.80", "CPU", "12.5%", "3 GiB of   8 GiB", "1 Status", "██╔╝╚██╗"} {
		if !strings.Contains(v, want) {
			t.Errorf("status page lacks %q:\n%s", want, v)
		}
	}
	rows, _ := fakeSource{}.Containers(context.Background())
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m.Update(containersMsg{rows: rows})
	v = m.View()
	if !strings.Contains(v, "uptime-kuma") || !strings.Contains(v, "CPU ▼") {
		t.Errorf("containers page:\n%s", v)
	}
	// Sorted by CPU descending: uptime-kuma first.
	if strings.Index(v, "uptime-kuma") > strings.Index(v, "jellyfin") {
		t.Errorf("sort order wrong:\n%s", v)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m.Update(logsMsg{unit: "maitre-agent.service", lines: []string{"hello journal"}})
	if v := m.View(); !strings.Contains(v, "hello journal") || !strings.Contains(v, "maitre-agent") {
		t.Errorf("logs page:\n%s", v)
	}
	// r only works on the Status page.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if v := m.View(); strings.Contains(v, "Reboot this Node now?") {
		t.Errorf("reboot offered off the Status page")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if v := m.View(); !strings.Contains(v, "Reboot this Node now?") {
		t.Errorf("no reboot confirmation:\n%s", v)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if v := m.View(); strings.Contains(v, "Reboot this Node now?") {
		t.Errorf("confirmation not dismissed")
	}
}

func TestParseBytes(t *testing.T) {
	for in, want := range map[string]uint64{"12.3MB": 12300000, "1.5GiB": 1610612736, "512kB": 512000, "0B": 0} {
		if got := parseBytes(in); got != want {
			t.Errorf("parseBytes(%q) = %d, want %d", in, got, want)
		}
	}
}
