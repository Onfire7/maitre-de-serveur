package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// PodmanContainers lists containers with their load, via podman ps and
// podman stats. Containers carrying the Maître Service label are named by
// their Service.
func PodmanContainers(ctx context.Context) ([]Container, error) {
	out, err := exec.CommandContext(ctx, "podman", "ps", "-a", "--format", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("podman ps: %w", err)
	}
	var ps []struct {
		Names    []string          `json:"Names"`
		State    string            `json:"State"`
		Status   string            `json:"Status"`
		Image    string            `json:"Image"`
		Labels   map[string]string `json:"Labels"`
		Restarts uint32            `json:"Restarts"`
	}
	if err := json.Unmarshal(out, &ps); err != nil {
		return nil, fmt.Errorf("podman ps: %w", err)
	}
	stats := map[string]struct {
		cpu, memPct float64
		mem         uint64
	}{}
	if sout, err := exec.CommandContext(ctx, "podman", "stats", "--no-stream", "--format", "json").Output(); err == nil {
		var st []struct {
			Name     string `json:"Name"`
			CPUPerc  string `json:"CPUPerc"`
			MemPerc  string `json:"MemPerc"`
			MemUsage string `json:"MemUsage"`
		}
		if json.Unmarshal(sout, &st) == nil {
			for _, s := range st {
				stats[s.Name] = struct {
					cpu, memPct float64
					mem         uint64
				}{pct(s.CPUPerc), pct(s.MemPerc), parseBytes(strings.SplitN(s.MemUsage, "/", 2)[0])}
			}
		}
	}
	var rows []Container
	for _, c := range ps {
		name := ""
		if len(c.Names) > 0 {
			name = c.Names[0]
		}
		r := Container{Name: name, Service: c.Labels["dev.maitre.service"], State: c.State, Status: c.Status, Image: c.Image, Restarts: c.Restarts}
		if r.Service != "" {
			r.Name = r.Service
		}
		if s, ok := stats[name]; ok {
			r.CPUPct, r.MemPct, r.MemBytes = s.cpu, s.memPct, s.mem
		}
		rows = append(rows, r)
	}
	return rows, nil
}

func pct(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "%"), 64)
	return f
}

// parseBytes reads podman's "12.3MB", "1.2GiB", "512kB" style sizes.
func parseBytes(s string) uint64 {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	f, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0
	}
	unit := strings.ToLower(strings.TrimSpace(s[i:]))
	mult := map[string]float64{"b": 1, "kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12, "kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40}[unit]
	if mult == 0 {
		mult = 1
	}
	return uint64(f * mult)
}

// JournalLogs returns the last n lines of a systemd unit's journal.
func JournalLogs(ctx context.Context, unit string, n int) ([]string, error) {
	out, err := exec.CommandContext(ctx, "journalctl", "-u", unit, "-n", strconv.Itoa(n), "--no-pager", "-o", "short-iso").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("journalctl: %w: %s", err, strings.TrimSpace(string(out)))
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return []string{"(no log lines yet)"}, nil
	}
	return lines, nil
}
