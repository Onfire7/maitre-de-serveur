package installer

import (
	"math"
	"strings"
	"testing"
)

func TestPullProgressParsesBars(t *testing.T) {
	p := newPullProgress(0)
	for _, l := range strings.Split(strings.TrimSpace(`
Trying to pull ghcr.io/onfire7/maitre-server:abc...
Getting image source signatures
Copying blob 5f70bf18a086 [--------------------------------------] 0.0b / 100.0MiB
Copying blob aaaaaaaaaaaa [--------------------------------------] 0.0b / 300.0MiB
Copying blob 5f70bf18a086 [=================>--------------------] 50.0MiB / 100.0MiB | 2.1 MiB/s
Copying blob aaaaaaaaaaaa [=========>----------------------------] 75.0MiB / 300.0MiB | 4.0 MiB/s
`), "\n") {
		p.line(l)
	}
	f, msg := p.fraction()
	if math.Abs(f-0.3125) > 0.001 || msg != "125 MiB of 400 MiB" {
		t.Fatalf("fraction %v %q", f, msg)
	}
	p.line("Copying blob 5f70bf18a086 done   |")
	p.line("Copying blob aaaaaaaaaaaa done   |")
	if f, _ := p.fraction(); f != 1 {
		t.Fatalf("after done: %v", f)
	}
}

func TestPullProgressUsesKnownTotal(t *testing.T) {
	p := newPullProgress(1 << 30)
	p.line("Copying blob 5f70bf18a086 [=>---] 256.0MiB / 512.0MiB")
	f, msg := p.fraction()
	if f != 0.25 || msg != "256 MiB of 1.0 GiB" {
		t.Fatalf("fraction %v %q", f, msg)
	}
	if parseSize("1.5", "GiB") != 1610612736 || parseSize("3.0", "KiB") != 3072 || parseSize("12.0", "b") != 12 {
		t.Fatal("parseSize")
	}
}
