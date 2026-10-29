// Package catalog reads Catalog entries (catalog/FORMAT.md) and builds or
// consumes the generated index the Console and agent use.
package catalog

import (
	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"

	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest is one entry's manifest.yaml.
type Manifest struct {
	ID             string   `yaml:"id" json:"id"`
	Name           string   `yaml:"name" json:"name"`
	Summary        string   `yaml:"summary" json:"summary"`
	Description    string   `yaml:"description" json:"description"`
	Icon           string   `yaml:"icon" json:"icon"`
	Category       string   `yaml:"category" json:"category"`
	Image          string   `yaml:"image" json:"image"`
	Version        string   `yaml:"version" json:"version"`
	Exposure       string   `yaml:"exposure" json:"exposure"`
	OwnLogin       bool     `yaml:"own_login" json:"own_login"`
	Infrastructure bool     `yaml:"infrastructure" json:"infrastructure"`
	HostPorts      []string `yaml:"host_ports" json:"host_ports,omitempty"`
	// PortForward (decision 115): the app is meant to be reached from the
	// internet at its host_ports (a game server), so the Console offers to
	// have the router forward them. Still off until the user turns it on.
	PortForward bool      `yaml:"port_forward" json:"port_forward,omitempty"`
	Web         *Web      `yaml:"web" json:"web,omitempty"`
	Volumes     []Volume  `yaml:"volumes" json:"volumes"`
	Settings    []Setting `yaml:"settings" json:"settings,omitempty"`
	Health      *Health   `yaml:"health" json:"health,omitempty"`
	// Quadlet is the [Container] template, filled from <id>.container. Empty
	// in an entry of several components, whose templates are theirs.
	Quadlet string `yaml:"-" json:"quadlet"`
	// Components, when given, make the entry a Service of several containers
	// (plan C8): one <name>.container template each, and no image or
	// <id>.container of the entry's own.
	Components []Component `yaml:"components" json:"components,omitempty"`
	// Network "internal" gives the Service's own network no route out; its
	// components marked egress still have one (plan C2).
	Network string `yaml:"network" json:"network,omitempty"`
	// Units are the bundle's own .volume and .network files, by file name.
	Units []Unit `yaml:"-" json:"units,omitempty"`
	// Source says where an entry converted or imported from elsewhere came
	// from; omitted for one written here by hand.
	Source *Source `yaml:"source" json:"source,omitempty"`
}

// Component is one container of an entry of several.
type Component struct {
	Name  string `yaml:"name" json:"name"`
	Image string `yaml:"image" json:"image"`
	// After are components to start first; WaitHealthy makes those that
	// wait for this one wait for its health check too.
	After       []string `yaml:"after" json:"after,omitempty"`
	WaitHealthy bool     `yaml:"wait_healthy" json:"wait_healthy,omitempty"`
	// Egress keeps a route out when the entry's network is internal.
	Egress bool `yaml:"egress" json:"egress,omitempty"`
	// Quadlet is the [Container] template, filled from <name>.container.
	Quadlet string `yaml:"-" json:"quadlet"`
}

// Unit is a bundle's .volume or .network file.
type Unit struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

// Source is where an entry came from (plan C8, C11).
type Source struct {
	From      string `yaml:"from" json:"from"`
	URL       string `yaml:"url" json:"url,omitempty"`
	Licence   string `yaml:"licence" json:"licence,omitempty"`
	Converted string `yaml:"converted" json:"converted,omitempty"`
}

// Web is the page the Console opens.
type Web struct {
	Port int    `yaml:"port" json:"port"`
	Path string `yaml:"path" json:"path"`
	// Component serves the page, in an entry of several components.
	Component string `yaml:"component" json:"component,omitempty"`
}

// Volume is a named data directory.
type Volume struct {
	Name string `yaml:"name" json:"name"`
	Path string `yaml:"path" json:"path"`
	Size string `yaml:"size" json:"size,omitempty"`
	// Shared suggests a folder the user picks, used by other Services too
	// (decision 175).
	Shared bool `yaml:"shared" json:"shared,omitempty"`
}

// Setting is asked at install time.
type Setting struct {
	Key     string `yaml:"key" json:"key"`
	Label   string `yaml:"label" json:"label"`
	Type    string `yaml:"type" json:"type"`
	Default string `yaml:"default" json:"default,omitempty"`
}

// Health is how the agent decides a Service is up.
type Health struct {
	HTTP string `yaml:"http" json:"http,omitempty"`
	Cmd  string `yaml:"cmd" json:"cmd,omitempty"`
}

// Index is the generated index.json.
type Index struct {
	Format  int        `json:"format"`
	Entries []Manifest `json:"entries"`
}

var (
	idRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,40}$`)
	digestRe = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
	portRe   = regexp.MustCompile(`^\d{1,5}/(tcp|udp)$`)
	// A volume or component name is also a Go template field
	// ({{ .Volumes.name }}), so it cannot hold a dash.
	fieldRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	compRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)
	unitRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*\.(volume|network)$`)
)

// LoadEntry reads entries/<id>/manifest.yaml and <id>.container.
func LoadEntry(dir string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	if err := m.readUnits(dir); err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	return &m, nil
}

// readUnits fills the entry's templates from its folder: <id>.container
// for one container, or <name>.container per component, plus any .volume
// and .network files. A .container file no component names is refused, so
// a renamed component cannot leave its old template behind unnoticed.
func (m *Manifest) readUnits(dir string) error {
	read := func(name string) (string, error) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		return string(b), err
	}
	wanted := map[string]bool{}
	if len(m.Components) == 0 {
		q, err := read(m.ID + ".container")
		if err != nil {
			return err
		}
		m.Quadlet = q
		wanted[m.ID+".container"] = true
	}
	for i := range m.Components {
		c := &m.Components[i]
		q, err := read(c.Name + ".container")
		if err != nil {
			return fmt.Errorf("component %s: %w", c.Name, err)
		}
		c.Quadlet = q
		wanted[c.Name+".container"] = true
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		switch filepath.Ext(f.Name()) {
		case ".container":
			if !wanted[f.Name()] {
				return fmt.Errorf("%s is not the template of any component", f.Name())
			}
		case ".volume", ".network":
			body, err := read(f.Name())
			if err != nil {
				return err
			}
			m.Units = append(m.Units, Unit{Name: f.Name(), Body: body})
		case ".pod", ".kube", ".build", ".image":
			return fmt.Errorf("%s: a bundle holds .container, .volume and .network units only", f.Name())
		}
	}
	return nil
}

// templates are the entry's [Container] templates, for checks that apply
// to each.
func (m *Manifest) templates() []string {
	if len(m.Components) == 0 {
		return []string{m.Quadlet}
	}
	out := make([]string, len(m.Components))
	for i, c := range m.Components {
		out[i] = c.Quadlet
	}
	return out
}

// Validate checks the rules in FORMAT.md.
func (m *Manifest) Validate() error {
	if err := m.validateComponents(); err != nil {
		return err
	}
	switch {
	case !idRe.MatchString(m.ID):
		return fmt.Errorf("bad id %q", m.ID)
	case m.Name == "" || m.Summary == "":
		return fmt.Errorf("name and summary are required")
	case m.Exposure != "private" && m.Exposure != "remote":
		return fmt.Errorf("exposure must be private or remote, got %q", m.Exposure)
	case m.Network != "" && m.Network != "internal":
		return fmt.Errorf("network must be internal or left out, got %q", m.Network)
	case m.Infrastructure && len(m.HostPorts) == 0:
		return fmt.Errorf("an infrastructure entry must declare host_ports")
	case m.PortForward && len(m.HostPorts) == 0:
		return fmt.Errorf("port_forward needs host_ports")
	case m.PortForward && m.Infrastructure:
		return fmt.Errorf("network infrastructure cannot be port forwarded")
	}
	for _, p := range m.HostPorts {
		if !portRe.MatchString(p) {
			return fmt.Errorf("bad host port %q", p)
		}
	}
	seen := map[string]bool{}
	for _, v := range m.Volumes {
		if !fieldRe.MatchString(v.Name) || !strings.HasPrefix(v.Path, "/") || seen[v.Name] {
			return fmt.Errorf("bad volume %+v: a name of lowercase letters, digits and underscores, and an absolute path", v)
		}
		seen[v.Name] = true
		used := false
		for _, q := range m.templates() {
			used = used || strings.Contains(q, "{{ .Volumes."+v.Name+" }}")
		}
		if !used {
			return fmt.Errorf("volume %q is not used by any quadlet template", v.Name)
		}
	}
	for _, s := range m.Settings {
		switch s.Type {
		case "text", "number", "password", "timezone", "bool":
		default:
			return fmt.Errorf("setting %q has unknown type %q", s.Key, s.Type)
		}
		if s.Default == Generate && s.Type != "password" {
			return fmt.Errorf("setting %q: only a password can be generated", s.Key)
		}
	}
	if m.Web != nil && (m.Web.Port <= 0 || m.Web.Port > 65535) {
		return fmt.Errorf("web.port out of range")
	}
	if m.Source != nil {
		switch m.Source.From {
		case "compose", "casaos", "hand":
		default:
			return fmt.Errorf("source.from must be compose, casaos or hand, got %q", m.Source.From)
		}
	}
	return nil
}

// validateComponents checks the entry's container definitions: one image and
// template, or components each with theirs, every image pinned by digest.
func (m *Manifest) validateComponents() error {
	if len(m.Components) == 0 {
		switch {
		case !digestRe.MatchString(m.Image):
			return fmt.Errorf("image must be pinned by @sha256 digest")
		case !strings.Contains(m.Quadlet, "[Container]"):
			return fmt.Errorf("quadlet template needs a [Container] section")
		case m.Web != nil && m.Web.Component != "":
			return fmt.Errorf("web.component names a component, and the entry has none")
		case len(m.Units) > 0:
			return fmt.Errorf(".volume and .network units need components")
		case m.Network != "":
			return fmt.Errorf("network: internal needs components")
		}
		return nil
	}
	if m.Image != "" {
		return fmt.Errorf("an entry of components has no image of its own; each component names its image")
	}
	names := map[string]bool{}
	for _, c := range m.Components {
		switch {
		case !compRe.MatchString(c.Name):
			return fmt.Errorf("component name %q: lowercase letters, digits and dashes", c.Name)
		case names[c.Name]:
			return fmt.Errorf("two components named %q", c.Name)
		case !digestRe.MatchString(c.Image):
			return fmt.Errorf("component %s: image must be pinned by @sha256 digest", c.Name)
		case !strings.Contains(c.Quadlet, "[Container]"):
			return fmt.Errorf("component %s: quadlet template needs a [Container] section", c.Name)
		}
		names[c.Name] = true
	}
	for _, c := range m.Components {
		for _, a := range c.After {
			if !names[a] {
				return fmt.Errorf("component %s starts after %q, which is not a component", c.Name, a)
			}
		}
	}
	if err := noCycle(m.Components); err != nil {
		return err
	}
	if m.Web != nil && !names[m.Web.Component] {
		return fmt.Errorf("web.component must name the component that serves the page")
	}
	for _, u := range m.Units {
		if !unitRe.MatchString(u.Name) {
			return fmt.Errorf("unit %q: name it <name>.volume or <name>.network", u.Name)
		}
	}
	return nil
}

// noCycle refuses components that wait for each other in a ring, which
// systemd would break by starting one of them anyway.
func noCycle(cs []Component) error {
	after := map[string][]string{}
	for _, c := range cs {
		after[c.Name] = c.After
	}
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var visit func(string) error
	visit = func(n string) error {
		switch state[n] {
		case visiting:
			return fmt.Errorf("components wait for each other in a ring through %q", n)
		case done:
			return nil
		}
		state[n] = visiting
		for _, a := range after[n] {
			if err := visit(a); err != nil {
				return err
			}
		}
		state[n] = done
		return nil
	}
	for _, c := range cs {
		if err := visit(c.Name); err != nil {
			return err
		}
	}
	return nil
}

// LoadDir reads every entry under entriesDir.
func LoadDir(entriesDir string) (*Index, error) {
	dirs, err := os.ReadDir(entriesDir)
	if err != nil {
		return nil, err
	}
	idx := &Index{Format: 1}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		m, err := LoadEntry(filepath.Join(entriesDir, d.Name()))
		if err != nil {
			return nil, err
		}
		if m.ID != d.Name() {
			return nil, fmt.Errorf("%s: id %q does not match directory", d.Name(), m.ID)
		}
		idx.Entries = append(idx.Entries, *m)
	}
	sort.Slice(idx.Entries, func(i, j int) bool { return idx.Entries[i].ID < idx.Entries[j].ID })
	return idx, nil
}

// ParseIndex decodes index.json.
func ParseIndex(raw []byte) (*Index, error) {
	var idx Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, err
	}
	if idx.Format != 1 {
		return nil, fmt.Errorf("unsupported catalog index format %d", idx.Format)
	}
	for i := range idx.Entries {
		if err := idx.Entries[i].Validate(); err != nil {
			return nil, err
		}
	}
	return &idx, nil
}

// Find returns the entry with id.
func (idx *Index) Find(id string) *Manifest {
	for i := range idx.Entries {
		if idx.Entries[i].ID == id {
			return &idx.Entries[i]
		}
	}
	return nil
}

// Generate as a setting's default makes the Console (and SpecFor) fill it
// with a fresh password at install (plan C7).
const Generate = "generate"

// GeneratePassword is 32 letters and digits, about 190 bits: letters and
// digits only, since apps such as Immich take nothing else in a database
// password. The Console's generatePassword makes the same kind.
func GeneratePassword() string {
	const abc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, 0, 32)
	buf := make([]byte, 48)
	for len(out) < 32 {
		if _, err := rand.Read(buf); err != nil {
			panic(err) // crypto/rand does not fail on supported systems
		}
		for _, b := range buf {
			// Rejection sampling keeps every character equally likely.
			if b < 248 && len(out) < 32 {
				out = append(out, abc[int(b)%len(abc)])
			}
		}
	}
	return string(out)
}

// SpecFor turns an entry plus the user's choices into the ServiceSpec the
// Node runs (decision 78). The Console does the same in TypeScript.
func SpecFor(e *Manifest, name string, settings map[string]string, exposure string) *maitrev1.ServiceSpec {
	if name == "" {
		name = e.ID
	}
	if exposure == "" {
		exposure = e.Exposure
	}
	merged := map[string]string{}
	for _, s := range e.Settings {
		merged[s.Key] = s.Default
		if s.Default == Generate {
			merged[s.Key] = GeneratePassword()
		}
	}
	for k, v := range settings {
		merged[k] = v
	}
	spec := &maitrev1.ServiceSpec{
		Name:            name,
		CatalogId:       e.ID,
		Image:           e.Image,
		Settings:        merged,
		Exposure:        exposure,
		Quadlet:         e.Quadlet,
		Infrastructure:  e.Infrastructure,
		NetworkInternal: e.Network == "internal",
	}
	for _, c := range e.Components {
		spec.Components = append(spec.Components, &maitrev1.ServiceComponent{Name: c.Name, Image: c.Image, Quadlet: c.Quadlet, After: c.After, WaitHealthy: c.WaitHealthy, Egress: c.Egress})
	}
	for _, u := range e.Units {
		spec.Units = append(spec.Units, &maitrev1.ServiceUnit{Name: u.Name, Body: u.Body})
	}
	if e.Web != nil {
		spec.WebPort = uint32(e.Web.Port)
		spec.WebPath = e.Web.Path
		spec.WebComponent = e.Web.Component
	}
	if e.Health != nil {
		spec.HealthHttp = e.Health.HTTP
	}
	for _, v := range e.Volumes {
		spec.Volumes = append(spec.Volumes, &maitrev1.ServiceVolume{Name: v.Name, Path: v.Path, Size: v.Size, Shared: v.Shared})
	}
	return spec
}
