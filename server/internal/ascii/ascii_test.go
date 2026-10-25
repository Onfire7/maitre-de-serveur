package ascii

import (
	"strings"
	"testing"
)

func TestDigits(t *testing.T) {
	got := Digits("10", "  ")
	lines := strings.Split(got, "\n")
	if len(lines) != 5 {
		t.Fatalf("lines: %d", len(lines))
	}
	if lines[0] != "   █   ███" || lines[4] != "  ███  ███" {
		t.Fatalf("rows:\n%s", got)
	}
	if strings.Count(Banner(), "\n") != 8 {
		t.Fatal("banner height")
	}
}
