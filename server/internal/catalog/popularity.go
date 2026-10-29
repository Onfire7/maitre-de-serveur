package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// PopularityFile sits beside a Catalog's entries/ folder (P2): the stars
// of each entry's project, kept out of the manifests so a weekly update
// does not change every entry.
const PopularityFile = "popularity.json"

// Popularity is popularity.json.
type Popularity struct {
	// Checked is the day the stars were looked up.
	Checked string         `json:"checked"`
	Stars   map[string]int `json:"stars"`
}

// PopularityPath is where the Catalog whose entries are in entriesDir keeps
// its popularity.json.
func PopularityPath(entriesDir string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(entriesDir)), PopularityFile)
}

// ReadPopularity reads it; a Catalog without one has none.
func ReadPopularity(entriesDir string) (*Popularity, error) {
	raw, err := os.ReadFile(PopularityPath(entriesDir))
	if errors.Is(err, os.ErrNotExist) {
		return &Popularity{Stars: map[string]int{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var p Popularity
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", PopularityFile, err)
	}
	if p.Stars == nil {
		p.Stars = map[string]int{}
	}
	return &p, nil
}

// WritePopularity writes it, ids in order.
func WritePopularity(entriesDir string, p *Popularity) error {
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(PopularityPath(entriesDir), append(raw, '\n'), 0o644)
}

func (idx *Index) applyPopularity(entriesDir string) error {
	p, err := ReadPopularity(entriesDir)
	if err != nil {
		return err
	}
	for i := range idx.Entries {
		idx.Entries[i].Stars = p.Stars[idx.Entries[i].ID]
	}
	return nil
}

// projectHosts are the hosts whose stars Maître can read.
var projectHosts = map[string]bool{"github.com": true, "codeberg.org": true, "gitlab.com": true}

// ProjectRepo splits a project URL into its host and owner/repository:
// https://github.com/home-assistant/core is github.com and
// home-assistant/core. Only hosts whose stars can be read are accepted.
func ProjectRepo(project string) (host, repo string, err error) {
	u, err := url.Parse(project)
	if err != nil || u.Scheme != "https" || !projectHosts[u.Host] {
		return "", "", fmt.Errorf("project %q: an https:// address on github.com, codeberg.org or gitlab.com", project)
	}
	path := strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" || (u.Host != "gitlab.com" && len(parts) != 2) {
		return "", "", fmt.Errorf("project %q: name the repository, as https://%s/owner/repository", project, u.Host)
	}
	return u.Host, path, nil
}

// ProjectFromSource is the project an image's
// org.opencontainers.image.source label names, normalised, or "" when it
// names no repository on a host whose stars can be read, or names one that
// only packages the app.
func ProjectFromSource(label string) string {
	u, err := url.Parse(strings.TrimSpace(label))
	if err != nil || !projectHosts[u.Host] {
		return ""
	}
	parts := strings.Split(strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	// A repository that packages the app (linuxserver/docker-syncthing,
	// jellyfin/jellyfin-packaging) is not the app's own.
	repo := strings.ToLower(parts[1])
	if strings.HasPrefix(repo, "docker-") || strings.HasSuffix(repo, "-docker") || strings.HasSuffix(repo, "-packaging") {
		return ""
	}
	p := "https://" + u.Host + "/" + parts[0] + "/" + parts[1]
	if _, _, err := ProjectRepo(p); err != nil {
		return ""
	}
	return p
}
