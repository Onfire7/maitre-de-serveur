package tui

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Braille line charts (decision 212): each character is a 2 by 4 grid of
// dots, so a chart of histLen cells and 3 rows is 64 by 12 dots, drawn as
// a line rather than bars. The Linux console shows them once the screen
// has loaded its font (console_linux.go); any other terminal has them.

// brailleBit is the bit of dot (x, y) in a braille pattern, x 0..1 and y
// 0..3 from the top (Unicode's dot numbering: 1 2 3 7 down the left, 4 5 6
// 8 down the right).
func brailleBit(x, y int) uint8 {
	if y == 3 {
		return 1 << (6 + x)
	}
	return 1 << (3*x + y)
}

// brailleGlyph draws pattern p as a w by h bitmap, rows of (w+7)/8 bytes,
// most significant bit leftmost. Each dot is a square in its quarter of the
// cell with a one-pixel gap after it, so neighbouring dots read as a
// dotted line.
func brailleGlyph(p uint8, w, h int) []byte {
	stride := (w + 7) / 8
	out := make([]byte, stride*h)
	cw, ch := w/2, h/4
	dw, dh := max(1, cw-1), max(1, ch-1)
	for y := range 4 {
		for x := range 2 {
			if p&brailleBit(x, y) == 0 {
				continue
			}
			for py := y * ch; py < y*ch+dh; py++ {
				for px := x * cw; px < x*cw+dw; px++ {
					out[py*stride+px/8] |= 0x80 >> (px % 8)
				}
			}
		}
	}
	return out
}

// chartLevel picks a chart cell's colour by how high it reaches.
func chartLevel(v float64) lipgloss.Style {
	switch {
	case v >= 90:
		return chartRed
	case v >= 80:
		return chartOrange
	case v >= 60:
		return chartYellow
	}
	return chartCalm
}

// brailleChart draws percentages (0..100, NaN for none), two per cell, as
// a line rows high, between the gauges' edges. Consecutive readings are
// joined by a vertical run so the line has no breaks; a NaN breaks it.
// Each cell takes the colour of the highest reading it shows.
func brailleChart(cols []float64, rows int) []string {
	cells := (len(cols) + 1) / 2
	h := 4 * rows
	grid := make([][]uint8, rows)
	for r := range grid {
		grid[r] = make([]uint8, cells)
	}
	top := make([]float64, cells)
	for i := range top {
		top[i] = math.NaN()
	}
	prev := -1
	for x, v := range cols {
		if math.IsNaN(v) {
			prev = -1
			continue
		}
		y := int(math.Round(math.Max(0, math.Min(100, v)) / 100 * float64(h-1)))
		from, to := y, y
		if prev >= 0 {
			from, to = min(prev, y), max(prev, y)
			// The step is drawn in this column; the previous one keeps its dot.
			if prev < y {
				from = prev + 1
			} else if prev > y {
				to = prev - 1
			}
		}
		for d := from; d <= to; d++ {
			row := h - 1 - d
			grid[row/4][x/2] |= brailleBit(x%2, row%4)
		}
		if c := x / 2; math.IsNaN(top[c]) || v > top[c] {
			top[c] = v
		}
		prev = y
	}
	out := make([]string, rows)
	for r := range rows {
		var b strings.Builder
		b.WriteString(cap)
		for c := range cells {
			if grid[r][c] == 0 {
				b.WriteByte(' ')
				continue
			}
			b.WriteString(chartLevel(top[c]).Render(string(rune(0x2800 + int(grid[r][c])))))
		}
		b.WriteString(cap)
		out[r] = b.String()
	}
	return out
}
