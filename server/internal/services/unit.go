package services

import "strings"

// A unit under construction is a list of lines, each with where it came
// from (ADR 0010): the bundle's template, the Node, or a person's override.
// Setup shows the sources beside the rendered file.

const (
	srcTemplate = "template"
	srcAgent    = "agent"
	srcOverride = "override"
)

type line struct {
	text, source string
}

type unit []line

func (u unit) String() string {
	var b strings.Builder
	for _, l := range u {
		b.WriteString(l.text)
		b.WriteString("\n")
	}
	return b.String()
}

// Sources is one entry per line of String, as RenderedUnit wants it.
func (u unit) Sources() []string {
	out := make([]string, len(u))
	for i, l := range u {
		out[i] = l.source
	}
	return out
}

// fromText splits text into lines of one source; a trailing newline adds
// no empty line.
func fromText(text, source string) unit {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	var out unit
	for _, t := range strings.Split(text, "\n") {
		out = append(out, line{t, source})
	}
	return out
}

func agentLine(text string) line { return line{text, srcAgent} }

// containerRange is the index range of the [Container] group's lines,
// the header excluded: [from, to).
func (u unit) containerRange() (from, to int) {
	from, to = -1, len(u)
	for i, l := range u {
		t := strings.TrimSpace(l.text)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			if from >= 0 {
				return from, i
			}
			if t == "[Container]" {
				from = i + 1
			}
		}
	}
	if from < 0 {
		return 0, 0
	}
	return from, to
}

// filterContainer keeps the [Container] lines keep says to.
func (u unit) filterContainer(keep func(line) bool) unit {
	from, to := u.containerRange()
	out := append(unit(nil), u[:from]...)
	for _, l := range u[from:to] {
		if keep(l) {
			out = append(out, l)
		}
	}
	return append(out, u[to:]...)
}

// mapContainer rewrites [Container] lines; f's second result drops a line.
func (u unit) mapContainer(f func(line) (line, bool)) unit {
	from, to := u.containerRange()
	out := append(unit(nil), u[:from]...)
	for _, l := range u[from:to] {
		if nl, keep := f(l); keep {
			out = append(out, nl)
		}
	}
	return append(out, u[to:]...)
}

// appendContainer adds a line at the end of the [Container] group, before
// any blank line that ends it.
func (u unit) appendContainer(l line) unit {
	_, to := u.containerRange()
	for to > 0 && strings.TrimSpace(u[to-1].text) == "" {
		to--
	}
	out := append(unit(nil), u[:to]...)
	out = append(out, l)
	return append(out, u[to:]...)
}

// containerValues are the values of key in the [Container] group.
func (u unit) containerValues(key string) []string {
	from, to := u.containerRange()
	var out []string
	for _, l := range u[from:to] {
		if k, v, ok := keyValue(l.text); ok && k == key {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

func (u unit) hasContainerKey(key string) bool { return len(u.containerValues(key)) > 0 }

func (u unit) containerIs(key, value string) bool {
	for _, v := range u.containerValues(key) {
		if v == value {
			return true
		}
	}
	return false
}
