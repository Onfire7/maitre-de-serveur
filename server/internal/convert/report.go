package convert

import (
	"fmt"
	"strings"
)

// Report is what a conversion tells the person who ran it (plan C10), at
// three levels.
type Report struct {
	// MustFix stops the conversion: no bundle is written.
	MustFix []string `json:"must_fix,omitempty"`
	// NeedsPerson leaves the bundle unfinished: written, but not installable
	// until someone decides and removes the entry's unfinished list.
	NeedsPerson []string `json:"needs_person,omitempty"`
	// Changed lists what the conversion did differently from the Compose
	// file, for review.
	Changed []string `json:"changed,omitempty"`
	// Notes are what compose2quadlet itself said, and checks not run here.
	Notes []string `json:"notes,omitempty"`
}

func (r *Report) mustFix(format string, a ...any) {
	r.MustFix = append(r.MustFix, fmt.Sprintf(format, a...))
}

func (r *Report) needsPerson(format string, a ...any) {
	r.NeedsPerson = append(r.NeedsPerson, fmt.Sprintf(format, a...))
}

func (r *Report) changed(format string, a ...any) {
	r.Changed = append(r.Changed, fmt.Sprintf(format, a...))
}

func (r *Report) note(format string, a ...any) {
	n := fmt.Sprintf(format, a...)
	for _, x := range r.Notes {
		if x == n {
			return
		}
	}
	r.Notes = append(r.Notes, n)
}

// Markdown is CONVERSION.md, saved beside the bundle.
func (r *Report) Markdown(title, from string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Conversion of %s\n\nFrom `%s`, by `maitre catalog add`.\n", title, from)
	section := func(head, lead string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n## %s\n\n", head)
		if lead != "" {
			fmt.Fprintf(&b, "%s\n\n", lead)
		}
		for _, it := range items {
			fmt.Fprintf(&b, "- %s\n", it)
		}
	}
	section("Must fix", "The bundle was not written.", r.MustFix)
	section("Needs a person", "The entry is unfinished until these are decided and its `unfinished:` list is removed.", r.NeedsPerson)
	section("Changed for you", "Done differently from the Compose file; check they suit the app.", r.Changed)
	section("Notes", "", r.Notes)
	if len(r.MustFix)+len(r.NeedsPerson)+len(r.Changed)+len(r.Notes) == 0 {
		b.WriteString("\nNothing to report.\n")
	}
	return b.String()
}
