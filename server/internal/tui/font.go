package tui

// The console font with braille (decision 212). The Linux console draws
// from a bitmap font of at most 512 glyphs; its built-in one has 256 and no
// braille. The screen keeps those 256, so text looks as before, and adds
// the 256 braille patterns as the second half.

// consoleMode is what the screen found it is drawing on.
type consoleMode int

const (
	// consoleTerminal: not a Linux console; braille and the terminal's colours.
	consoleTerminal consoleMode = iota
	// consoleBraille: a Linux console now with braille and the screen's palette.
	consoleBraille
	// consoleBlocks: a Linux console whose font could not be changed; bar
	// charts of the console's own block characters, as before.
	consoleBlocks
)

// fontPitch is the rows each glyph takes in the kernel's font buffer,
// whatever its height.
const fontPitch = 32

// consoleFont is a font as the kernel hands it over: n glyphs of w by h,
// each fontPitch rows of (w+7)/8 bytes.
type consoleFont struct {
	w, h, n int
	data    []byte
}

// unipair maps a character to the glyph that draws it.
type unipair struct{ unicode, pos uint16 }

// aliases draw characters the font lacks with a close one it has.
var aliases = map[rune][]rune{
	'╭': {'┌'}, '╮': {'┐'}, '╰': {'└'}, '╯': {'┘'},
	'…': {'.'}, '–': {'-'}, '—': {'-'}, '‘': {'\''}, '’': {'\''}, '“': {'"'}, '”': {'"'},
	'●': {'•', '*'}, '▁': {'_'}, '�': {'?'},
}

// withBraille returns f with the braille patterns as glyphs 256..511 and
// the character map to match: f's own entries, braille, and aliases for
// what f cannot draw. False when f already has more than 256 glyphs.
func withBraille(f consoleFont, uni []unipair) (consoleFont, []unipair, bool) {
	if f.n > 256 || f.w < 2 || f.h < 4 || f.h > fontPitch {
		return f, nil, false
	}
	stride := (f.w + 7) / 8
	size := fontPitch * stride
	out := consoleFont{w: f.w, h: f.h, n: 512, data: make([]byte, 512*size)}
	copy(out.data, f.data[:f.n*size])
	for p := range 256 {
		copy(out.data[(256+p)*size:], brailleGlyph(uint8(p), f.w, f.h))
	}
	var m []unipair
	has := map[rune]uint16{}
	for _, u := range uni {
		if int(u.pos) < f.n {
			m = append(m, u)
			if _, ok := has[rune(u.unicode)]; !ok {
				has[rune(u.unicode)] = u.pos
			}
		}
	}
	for p := range 256 {
		m = append(m, unipair{uint16(0x2800 + p), uint16(256 + p)})
	}
	for r, alts := range aliases {
		if _, ok := has[r]; ok {
			continue
		}
		for _, a := range alts {
			if pos, ok := has[a]; ok {
				m = append(m, unipair{uint16(r), pos})
				break
			}
		}
	}
	return out, m, true
}

// hasBraille reports whether a console font of n glyphs and its map are
// already the screen's (set up before the agent restarted).
func hasBraille(n int, uni []unipair) bool {
	if n != 512 {
		return false
	}
	for _, u := range uni {
		if u.unicode == 0x28FF && u.pos == 511 {
			return true
		}
	}
	return false
}

// The palette the screen sets on the console (decision 212). With 512
// glyphs the console has 8 foreground colours, not 16: the bit that made
// a colour bright picks the glyph's half instead, so slots 8..15 repeat
// 0..7 and nothing may be faint, bright or bold. ANSI's own slot names
// still mean roughly what they say on any other terminal.
var consolePalette = [8][3]uint8{
	{0x00, 0x00, 0x00}, // 0 black: the background
	{0xe5, 0x48, 0x4d}, // 1 red
	{0x3e, 0xc9, 0xb0}, // 2 seafoam (green's slot)
	{0xf2, 0xc9, 0x4c}, // 3 yellow
	{0xf5, 0x92, 0x3e}, // 4 orange (blue's slot)
	{0x8a, 0x91, 0x99}, // 5 grey, for dim text (magenta's slot)
	{0x3e, 0xc9, 0xb0}, // 6 seafoam (cyan's slot)
	{0xf0, 0xf0, 0xf0}, // 7 white: plain text
}

// cmap is the 16-entry palette the kernel takes.
func cmap() []byte {
	out := make([]byte, 48)
	for i := range 16 {
		c := consolePalette[i%8]
		copy(out[3*i:], c[:])
	}
	return out
}
