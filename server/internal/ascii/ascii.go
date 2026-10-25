// Package ascii draws the few pieces of text art the machine's console shows
// (decision 72: plain text, with art for the moments that matter: the end of
// startup and the pairing code). Only block and box-drawing characters that
// the Linux console font has.
package ascii

import "strings"

// Banner is the Maître word mark, 9 lines: a drawn circumflex over the I,
// wider than the letter, then the word.
func Banner() string {
	return strings.Join([]string{
		`                    ▄▄╗`,
		`                  ▄█▀▀█▄║`,
		`                 ▀▀╝   ▀▀╝`,
		` ███╗   ███╗ █████╗ ██╗████████╗██████╗ ███████╗`,
		` ████╗ ████║██╔══██╗██║╚══██╔══╝██╔══██╗██╔════╝`,
		` ██╔████╔██║███████║██║   ██║   ██████╔╝█████╗  `,
		` ██║╚██╔╝██║██╔══██║██║   ██║   ██╔══██╗██╔══╝  `,
		` ██║ ╚═╝ ██║██║  ██║██║   ██║   ██║  ██║███████╗`,
		` ╚═╝     ╚═╝╚═╝  ╚═╝╚═╝   ╚═╝   ╚═╝  ╚═╝╚══════╝`,
	}, "\n")
}

var digits = map[rune][5]string{
	'0': {"███", "█ █", "█ █", "█ █", "███"},
	'1': {" █ ", "██ ", " █ ", " █ ", "███"},
	'2': {"███", "  █", "███", "█  ", "███"},
	'3': {"███", "  █", "███", "  █", "███"},
	'4': {"█ █", "█ █", "███", "  █", "  █"},
	'5': {"███", "█  ", "███", "  █", "███"},
	'6': {"███", "█  ", "███", "█ █", "███"},
	'7': {"███", "  █", "  █", "  █", "  █"},
	'8': {"███", "█ █", "███", "█ █", "███"},
	'9': {"███", "█ █", "███", "  █", "███"},
	' ': {"  ", "  ", "  ", "  ", "  "},
	'-': {"   ", "   ", "███", "   ", "   "},
}

// Digits renders s (digits, spaces and dashes) five lines tall, indented by
// indent. Other characters are skipped.
func Digits(s string, indent string) string {
	var rows [5]strings.Builder
	for _, r := range s {
		glyph, ok := digits[r]
		if !ok {
			continue
		}
		for i := range rows {
			if rows[i].Len() > 0 {
				rows[i].WriteString("  ")
			}
			rows[i].WriteString(glyph[i])
		}
	}
	out := make([]string, 5)
	for i := range rows {
		out[i] = indent + rows[i].String()
	}
	return strings.Join(out, "\n")
}
