// Package catalog reads Catalog entries (catalog/FORMAT.md) and builds or
// consumes the generated index the Console and agent use.
package catalog

import (
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
	ID             string    `yaml:"id" json:"id"`
	Name           string    `yaml:"name" json:"name"`
	Summary        string    `yaml:"summary" json:"summary"`
	Description    string    `yaml:"description" json:"description"`
	Icon           string    `yaml:"icon" json:"icon"`
	Category       string    `yaml:"category" json:"category"`
	Image          string    `yaml:"image" json:"image"`
	Version        string    `yaml:"version" json:"version"`
	Exposure       string    `yaml:"exposure" json:"exposure"`
	OwnLogin       bool      `yaml:"own_login" json:"own_login"`
	Infrastructure bool      `yaml:"infrastructure" json:"infrastructure"`
	HostPorts      []string  `yaml:"host_ports" json:"host_ports,omitempty"`
	Web            *Web      `yaml:"web" json:"web,omitempty"`
	Volumes        []Volume  `yaml:"volumes" json:"volumes"`
	Settings       []Setting `yaml:"settings" json:"settings,omitempty"`
	Health         *Health   `yaml:"health" json:"health,omitempty"`
	// Quadlet is the [Container] template, filled from <id>.container.
	Quadlet string `yaml:"-" json:"quadlet"`
}

// Web is the page the Console opens.
type Web struct {
	Port int    `yaml:"port" json:"port"`
	Path string `yaml:"path" json:"path"`
}

// Volume is a named data directory.
type Volume struct {
	Name string `yaml:"name" json:"name"`
	Path string `yaml:"path" json:"path"`
	Size string `yaml:"size" json:"size,omitempty"`
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
	q, err := os.ReadFile(filepath.Join(dir, m.ID+".container"))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	m.Quadlet = string(q)
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	return &m, nil
}

// Validate checks the rules in FORMAT.md.
func (m *Manifest) Validate() error {
	switch {
	case !idRe.MatchString(m.ID):
		return fmt.Errorf("bad id %q", m.ID)
	case m.Name == "" || m.Summary == "":
		return fmt.Errorf("name and summary are required")
	case !digestRe.MatchString(m.Image):
		return fmt.Errorf("image must be pinned by @sha256 digest")
	case m.Exposure != "private" && m.Exposure != "remote":
		return fmt.Errorf("exposure must be private or remote, got %q", m.Exposure)
	case !strings.Contains(m.Quadlet, "[Container]"):
		return fmt.Errorf("quadlet template needs a [Container] section")
	case m.Infrastructure && len(m.HostPorts) == 0:
		return fmt.Errorf("an infrastructure entry must declare host_ports")
	}
	for _, p := range m.HostPorts {
		if !portRe.MatchString(p) {
			return fmt.Errorf("bad host port %q", p)
		}
	}
	seen := map[string]bool{}
	for _, v := range m.Volumes {
		if !idRe.MatchString(v.Name) || !strings.HasPrefix(v.Path, "/") || seen[v.Name] {
			return fmt.Errorf("bad volume %+v", v)
		}
		seen[v.Name] = true
		if !strings.Contains(m.Quadlet, "{{ .Volumes."+v.Name+" }}") {
			return fmt.Errorf("volume %q is not used by the quadlet template", v.Name)
		}
	}
	for _, s := range m.Settings {
		switch s.Type {
		case "text", "number", "password", "timezone", "bool":
		default:
			return fmt.Errorf("setting %q has unknown type %q", s.Key, s.Type)
		}
	}
	if m.Web != nil && (m.Web.Port <= 0 || m.Web.Port > 65535) {
		return fmt.Errorf("web.port out of range")
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
