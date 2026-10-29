package tui

import (
	"math"
	"strings"
	"testing"
)

func TestBrailleGlyph(t *testing.T) {
	// Dot 1 (top left) and dot 8 (bottom right) of an 8x16 cell: 3x3
	// squares in the 4x4 quarters.
	g := brailleGlyph(0x01|0x80, 8, 16)
	want := []string{"11100000", "11100000", "11100000", "00000000"}
	for r, w := range want {
		if got := bits(g[r]); got != w {
			t.Fatalf("row %d %s, want %s", r, got, w)
		}
	}
	for r := 12; r < 15; r++ {
		if got := bits(g[r]); got != "00001110" {
			t.Fatalf("row %d %s", r, got)
		}
	}
	if bits(g[15]) != "00000000" || bits(g[6]) != "00000000" {
		t.Fatal("dots where none were asked for")
	}
	// Wider than 8: two bytes a row.
	// Dot 4 of a 12x24 cell: pixels 6..10 of the first row.
	if g := brailleGlyph(0x08, 12, 24); len(g) != 2*24 || g[0] != 0x03 || g[1] != 0xe0 {
		t.Fatalf("12x24 % x", g[:4])
	}
}

func bits(b byte) string {
	var s strings.Builder
	for i := 7; i >= 0; i-- {
		if b&(1<<i) != 0 {
			s.WriteByte('1')
		} else {
			s.WriteByte('0')
		}
	}
	return s.String()
}

func TestWithBraille(t *testing.T) {
	size := fontPitch
	base := consoleFont{w: 8, h: 16, n: 256, data: make([]byte, 256*size)}
	base.data['A'*size] = 0x42
	uni := []unipair{{'A', 'A'}, {'┌', 0xDA}, {'.', '.'}, {'•', 7}}
	f, m, ok := withBraille(base, uni)
	if !ok || f.n != 512 || len(f.data) != 512*size || f.data['A'*size] != 0x42 {
		t.Fatalf("font %v %d", ok, f.n)
	}
	if f.data[(256+0xFF)*size] != 0xEE {
		t.Fatalf("full braille glyph row 0 % x", f.data[(256+0xFF)*size])
	}
	pos := map[rune]uint16{}
	for _, u := range m {
		pos[rune(u.unicode)] = u.pos
	}
	if pos['A'] != 'A' || pos['⠀'] != 256 || pos['⣿'] != 511 || pos['╭'] != 0xDA || pos['…'] != '.' || pos['●'] != 7 {
		t.Fatalf("map %v", pos)
	}
	if _, ok := pos['—']; ok {
		t.Fatal("aliased to a character the font lacks")
	}
	if !hasBraille(f.n, m) || hasBraille(base.n, uni) {
		t.Fatal("hasBraille")
	}
	if _, _, ok := withBraille(consoleFont{w: 8, h: 16, n: 512}, nil); ok {
		t.Fatal("took a font with no room")
	}
}

func TestPaletteRepeats(t *testing.T) {
	c := cmap()
	if len(c) != 48 || string(c[:24]) != string(c[24:]) || c[3*6+1] != 0xc9 {
		t.Fatalf("% x", c)
	}
}

func TestBrailleChart(t *testing.T) {
	// A rise from 0 to 100 and back, a gap, then a flat 50.
	cols := []float64{0, 100, 0, math.NaN(), 50, 50}
	rows := brailleChart(cols, 3)
	if len(rows) != 3 {
		t.Fatal(rows)
	}
	plain := make([]string, 3)
	for i, r := range rows {
		plain[i] = []string{stripANSI(r)}[0]
	}
	// Column 0 (0%) is the bottom dot; column 1 climbs from the next dot to
	// the top; column 2 comes down from the dot below the top; column 3 is
	// empty; columns 4 and 5 sit at 50% (6 dots up of 0..11).
	want := []string{"│⢸⡆ │", "│⢸⡇⠒│", "│⡸⡇ │"}
	for i := range want {
		if plain[i] != want[i] {
			t.Errorf("row %d %q, want %q\n%s", i, plain[i], want[i], strings.Join(plain, "\n"))
		}
	}
	// Colour by the highest reading in the cell: 100 is red.
	if !strings.Contains(rows[0], chartRed.Render("⢸")) {
		t.Errorf("no red at the top: %q", rows[0])
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			esc = true
		case esc && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			esc = false
		case !esc:
			b.WriteRune(r)
		}
	}
	return b.String()
}
