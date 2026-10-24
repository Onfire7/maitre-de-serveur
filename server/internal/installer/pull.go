package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/creack/pty"
)

// podman only draws progress bars on a terminal, so the pull runs on a
// pseudo-terminal and the bars are parsed. Each layer's bar looks like
//
//	Copying blob 5f70bf18a086 [====>------] 12.3MiB / 69.6MiB | 2.1 MiB/s
//
// and ends as "... done" or "... skipped: already exists".
var blobBar = regexp.MustCompile(`Copying blob (\S+) .*?([0-9.]+)\s*([KMGT]?i?B|b) / ([0-9.]+)\s*([KMGT]?i?B|b)`)
var blobDone = regexp.MustCompile(`Copying blob (\S+) (done|skipped)`)

// pullProgress turns podman pull output into a 0..1 fraction. total is the
// compressed size of all layers when known in advance (skopeo), otherwise the
// sum of the bar totals seen so far, which can make the fraction dip when a
// new layer's bar appears; callers should only ever report increases.
type pullProgress struct {
	mu      sync.Mutex
	total   int64
	current map[string]int64
	sizes   map[string]int64
}

func newPullProgress(total int64) *pullProgress {
	return &pullProgress{total: total, current: map[string]int64{}, sizes: map[string]int64{}}
}

func (p *pullProgress) line(l string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if m := blobBar.FindStringSubmatch(l); m != nil {
		cur := parseSize(m[2], m[3])
		size := parseSize(m[4], m[5])
		p.current[m[1]] = cur
		p.sizes[m[1]] = size
		return
	}
	if m := blobDone.FindStringSubmatch(l); m != nil {
		if size, ok := p.sizes[m[1]]; ok {
			p.current[m[1]] = size
		}
	}
}

// fraction returns progress in 0..1 and a human summary like "312 MiB of 1.1 GiB".
func (p *pullProgress) fraction() (float64, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var cur, known int64
	for id, c := range p.current {
		cur += c
		known += p.sizes[id]
	}
	total := p.total
	if total <= 0 {
		total = known
	}
	if total <= 0 {
		return 0, ""
	}
	if cur > total {
		cur = total
	}
	return float64(cur) / float64(total), fmt.Sprintf("%s of %s", humanBytes(cur), humanBytes(total))
}

func parseSize(num, unit string) int64 {
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	switch strings.ToUpper(strings.TrimSuffix(strings.TrimSuffix(unit, "B"), "i")) {
	case "K":
		f *= 1 << 10
	case "M":
		f *= 1 << 20
	case "G":
		f *= 1 << 30
	case "T":
		f *= 1 << 40
	}
	return int64(f)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%.0f KiB", float64(n)/(1<<10))
	}
}

// imageSize asks the registry for the compressed size of the image's layers.
// Zero when skopeo is missing or the registry does not say.
func imageSize(ctx context.Context, ref string) int64 {
	out, err := exec.CommandContext(ctx, "skopeo", "inspect", "docker://"+ref).Output()
	if err != nil {
		return 0
	}
	var v struct {
		LayersData []struct{ Size int64 } `json:"LayersData"`
	}
	if json.Unmarshal(out, &v) != nil {
		return 0
	}
	var total int64
	for _, l := range v.LayersData {
		total += l.Size
	}
	return total
}

// execTTY runs a command on a pseudo-terminal, streaming its output to w, so
// tools that only show progress interactively show it here too.
func execTTY(ctx context.Context, w io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 200})
	if err != nil {
		return err
	}
	defer f.Close()
	_, _ = io.Copy(w, f) // ends with EIO when the child closes the pty; that is normal
	return cmd.Wait()
}
