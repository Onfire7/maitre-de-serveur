// Package outdated finds the Catalog's pinned images that have moved (plan
// C13): a tag whose digest is no longer the one pinned, since its publisher
// rebuilt it, and a newer tag in the same series. With Write, the entry's
// manifest.yaml is repinned in place, its comments and order kept.
package outdated

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"gopkg.in/yaml.v3"
)

// Registry is what the lookup asks of an image's registry.
type Registry interface {
	Digest(ctx context.Context, image string) (string, error)
	Tags(ctx context.Context, image string) ([]string, error)
}

// Image is one pinned image of an entry and what became of it.
type Image struct {
	// Component is empty for an entry's own image.
	Component string `json:"component,omitempty"`
	// Repo, Tag and Digest are as pinned.
	Repo   string `json:"repo"`
	Tag    string `json:"tag"`
	Digest string `json:"digest"`
	// Moved is the digest the tag names now, when it is not the pinned one.
	Moved string `json:"moved,omitempty"`
	// Newer is the newest tag in the same series, and NewerDigest its digest.
	Newer       string `json:"newer,omitempty"`
	NewerDigest string `json:"newer_digest,omitempty"`
	// NewerSeries is the newest tag of a later series (a new major
	// version): reported, never repinned, since moving to it is a person's
	// decision.
	NewerSeries string `json:"newer_series,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Pin is the reference the image is pinned to now, and Next the one it
// should be: the newer tag if there is one, else the moved digest.
func (i Image) Pin() string { return i.Repo + ":" + i.Tag + "@" + i.Digest }

func (i Image) Next() string {
	switch {
	case i.Newer != "":
		return i.Repo + ":" + i.Newer + "@" + i.NewerDigest
	case i.Moved != "":
		return i.Repo + ":" + i.Tag + "@" + i.Moved
	}
	return ""
}

// Entry is one Catalog entry's images.
type Entry struct {
	ID     string  `json:"id"`
	Images []Image `json:"images"`
	// Version is the entry's version: and NewVersion what it becomes when
	// its images move to a newer tag of the version it names.
	Version    string `json:"version,omitempty"`
	NewVersion string `json:"new_version,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Changed is whether anything in the entry would be repinned.
func (e Entry) Changed() bool {
	for _, im := range e.Images {
		if im.Next() != "" {
			return true
		}
	}
	return false
}

// manifest is the part of manifest.yaml that names images.
type manifest struct {
	Image      string `yaml:"image"`
	Version    string `yaml:"version"`
	Components []struct {
		Name  string `yaml:"name"`
		Image string `yaml:"image"`
	} `yaml:"components"`
}

// Check looks up every entry under dir (a Catalog's entries folder).
func Check(ctx context.Context, r Registry, dir string) ([]Entry, error) {
	ids, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, d := range ids {
		if !d.IsDir() {
			continue
		}
		out = append(out, checkEntry(ctx, r, filepath.Join(dir, d.Name())))
	}
	return out, nil
}

func checkEntry(ctx context.Context, r Registry, dir string) Entry {
	e := Entry{ID: filepath.Base(dir)}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	if err != nil {
		e.Error = err.Error()
		return e
	}
	var m manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		e.Error = err.Error()
		return e
	}
	e.Version = m.Version
	if m.Image != "" {
		e.Images = append(e.Images, checkImage(ctx, r, "", m.Image))
	}
	for _, c := range m.Components {
		e.Images = append(e.Images, checkImage(ctx, r, c.Name, c.Image))
	}
	// The entry's version follows the first image whose tag names it.
	for _, im := range e.Images {
		if im.Newer == "" {
			continue
		}
		if v, ok := followVersion(m.Version, im.Tag, im.Newer); ok {
			e.NewVersion = v
			break
		}
	}
	return e
}

func checkImage(ctx context.Context, r Registry, component, ref string) Image {
	im := Image{Component: component}
	repo, tag, digest, err := split(ref)
	if err != nil {
		im.Error = err.Error()
		return im
	}
	im.Repo, im.Tag, im.Digest = repo, tag, digest
	now, err := r.Digest(ctx, repo+":"+tag)
	if err != nil {
		im.Error = err.Error()
		return im
	}
	if now != digest {
		im.Moved = now
	}
	tags, err := r.Tags(ctx, repo+":"+tag)
	if err != nil {
		im.Error = err.Error()
		return im
	}
	im.Newer, im.NewerSeries = newer(tag, tags)
	if im.Newer != "" {
		if im.NewerDigest, err = r.Digest(ctx, repo+":"+im.Newer); err != nil {
			im.Error = err.Error()
			im.Newer = ""
		}
	}
	return im
}

// split reads repository:tag@sha256:...; every Catalog image is pinned so.
func split(ref string) (repo, tag, digest string, err error) {
	at := strings.LastIndex(ref, "@")
	if at < 0 {
		return "", "", "", fmt.Errorf("image %q is not pinned by digest", ref)
	}
	t, err := name.NewTag(ref[:at], name.StrictValidation)
	if err != nil {
		return "", "", "", fmt.Errorf("image %q: %w", ref, err)
	}
	if _, err := name.NewDigest(ref, name.StrictValidation); err != nil {
		return "", "", "", fmt.Errorf("image %q: %w", ref, err)
	}
	return ref[:at-len(t.TagStr())-1], t.TagStr(), ref[at+1:], nil
}

// version is a tag read as prefix, dotted numbers and suffix:
// "v3.3.5" is v + 3.3.5, "2026.10.1-java21" is 2026.10.1 + -java21,
// "14-vectorchord0.4.3" is 14 + -vectorchord0.4.3.
type version struct {
	prefix, suffix string
	nums           []int
	raw            string // the dotted numbers as written
}

var versionRe = regexp.MustCompile(`^([^0-9]*)([0-9]+(?:\.[0-9]+)*)(.*)$`)

func parse(tag string) (version, bool) {
	m := versionRe.FindStringSubmatch(tag)
	if m == nil {
		return version{}, false
	}
	v := version{prefix: m[1], suffix: m[3], raw: m[2]}
	for _, p := range strings.Split(m[2], ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return version{}, false
		}
		v.nums = append(v.nums, n)
	}
	return v, true
}

// sameShape is a tag of the same kind: the same prefix and suffix (so no
// -rc, -alpine or other variant creeps in) and as many numbers.
func (v version) sameShape(o version) bool {
	return v.prefix == o.prefix && v.suffix == o.suffix && len(v.nums) == len(o.nums)
}

func (v version) less(o version) bool {
	for i := range v.nums {
		if v.nums[i] != o.nums[i] {
			return v.nums[i] < o.nums[i]
		}
	}
	return false
}

// newer returns the newest tag of tag's series (the same shape and first
// number), when it is later than tag, and the newest of a later series. A
// tag of one number (postgres:16) is its own series: it only ever moves by
// digest.
func newer(tag string, tags []string) (series, later string) {
	cur, ok := parse(tag)
	if !ok {
		return "", ""
	}
	var same, beyond []version
	for _, t := range tags {
		v, ok := parse(t)
		if !ok || !cur.sameShape(v) || !cur.less(v) {
			continue
		}
		if v.nums[0] == cur.nums[0] {
			same = append(same, v)
		} else {
			beyond = append(beyond, v)
		}
	}
	newest := func(vs []version) string {
		if len(vs) == 0 {
			return ""
		}
		sort.Slice(vs, func(i, j int) bool { return vs[i].less(vs[j]) })
		v := vs[len(vs)-1]
		return v.prefix + v.raw + v.suffix
	}
	return newest(same), newest(beyond)
}

// followVersion is the entry's new version when it names the image's old
// tag: written as the tag's numbers ("3.3.5" for v3.3.5), with or without
// their leading zeros as the entry wrote it ("2026.5.0" for 2026.05.0).
func followVersion(entry, oldTag, newTag string) (string, bool) {
	o, ok1 := parse(oldTag)
	n, ok2 := parse(newTag)
	if !ok1 || !ok2 || entry == "" {
		return "", false
	}
	switch entry {
	case o.raw:
		return n.raw, true
	case plain(o.nums):
		return plain(n.nums), true
	}
	return "", false
}

func plain(nums []int) string {
	s := make([]string, len(nums))
	for i, n := range nums {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ".")
}

// Write repins the entry's manifest.yaml: each image that moved is replaced
// by its next pin, and the version follows. The file is edited as text, so
// nothing else in it changes.
func Write(dir string, e Entry) error {
	path := filepath.Join(dir, "manifest.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	s := string(raw)
	for _, im := range e.Images {
		if next := im.Next(); next != "" {
			s = strings.ReplaceAll(s, im.Pin(), next)
		}
	}
	if e.NewVersion != "" {
		s = replaceVersion(s, e.Version, e.NewVersion)
	}
	return os.WriteFile(path, []byte(s), 0o644)
}

var versionLine = regexp.MustCompile(`(?m)^version:[ \t]*(["']?)([^"'\n#]*?)(["']?)[ \t]*(#.*)?$`)

// replaceVersion rewrites the top-level version: line, keeping its quotes
// and comment.
func replaceVersion(s, old, next string) string {
	return versionLine.ReplaceAllStringFunc(s, func(line string) string {
		m := versionLine.FindStringSubmatch(line)
		if m[2] != old {
			return line
		}
		return strings.Replace(line, m[1]+old+m[3], m[1]+next+m[3], 1)
	})
}

// Markdown is the report a person reads, and the scheduled job's pull
// request says.
func Markdown(entries []Entry) string {
	var b strings.Builder
	var repinned, later, failed []string
	for _, e := range entries {
		if e.Error != "" {
			failed = append(failed, fmt.Sprintf("- %s: %s", e.ID, e.Error))
		}
		for _, im := range e.Images {
			what := e.ID
			if im.Component != "" {
				what += " (" + im.Component + ")"
			}
			switch {
			case im.Error != "":
				failed = append(failed, fmt.Sprintf("- %s, `%s:%s`: %s", what, im.Repo, im.Tag, im.Error))
			case im.Newer != "":
				repinned = append(repinned, fmt.Sprintf("- %s: `%s` %s to %s", what, im.Repo, im.Tag, im.Newer))
			case im.Moved != "":
				repinned = append(repinned, fmt.Sprintf("- %s: `%s:%s` was rebuilt (%s to %s)", what, im.Repo, im.Tag, short(im.Digest), short(im.Moved)))
			}
			if im.NewerSeries != "" {
				later = append(later, fmt.Sprintf("- %s: `%s` %s, after %s", what, im.Repo, im.NewerSeries, im.Tag))
			}
		}
		if e.NewVersion != "" {
			repinned = append(repinned, fmt.Sprintf("- %s: version %s to %s", e.ID, e.Version, e.NewVersion))
		}
	}
	section := func(head, lead string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&b, "## %s\n\n", head)
		if lead != "" {
			fmt.Fprintf(&b, "%s\n\n", lead)
		}
		b.WriteString(strings.Join(lines, "\n") + "\n\n")
	}
	section("Moved", "A newer tag in the same series, or the same tag rebuilt.", repinned)
	section("Later versions", "A new series, not repinned: moving to it is a person's decision, after reading its release notes.", later)
	section("Not looked up", "", failed)
	if b.Len() == 0 {
		return "Every image is current.\n"
	}
	return b.String()
}

func short(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}
