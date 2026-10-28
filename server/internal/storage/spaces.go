package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/pool"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// KindSpace is the Record kind for Storage Spaces (decisions 121-128).
const KindSpace = "space"

var spaceIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$|^[a-z0-9]$`)

// Placement policies for new files in a Space's Pool.
const (
	PlacementLocalFirst      = "local_first"
	PlacementMostFree        = "most_free"
	PlacementMostFreePercent = "most_free_percent"
)

// Spaces lists the Fleet's Spaces, highest priority first.
func Spaces(ctx context.Context, st *store.Store) ([]*maitrev1.Space, error) {
	spaces, err := store.ListLive(ctx, st, KindSpace, func() *maitrev1.Space { return &maitrev1.Space{} })
	if err != nil {
		return nil, err
	}
	sort.SliceStable(spaces, func(i, j int) bool { return spaces[i].GetPriority() > spaces[j].GetPriority() })
	return spaces, nil
}

// ValidateSpace is what a Node accepts as a Space record (Console writes go
// through PutRecord): an id, a name, a known placement, and a rule when the
// Space is exclusive, since an exclusive Space with an empty rule would take
// every Drive from every other Space.
func ValidateSpace(b *maitrev1.Space) error {
	switch {
	case b.GetSpaceId() == "" || b.GetName() == "":
		return errors.New("a Space needs an id and a name")
	case !spaceIDRe.MatchString(b.GetSpaceId()):
		return errors.New("a Space id is lower-case letters, digits and dashes")
	case b.GetPlacement() != PlacementLocalFirst && b.GetPlacement() != PlacementMostFree && b.GetPlacement() != PlacementMostFreePercent:
		return errors.New("placement must be local_first, most_free or most_free_percent")
	case b.GetExclusive() && len(b.GetRequireTags()) == 0 && len(b.GetNodeIds()) == 0:
		return errors.New("an exclusive Space needs at least one required tag or Node; otherwise it would claim every Drive")
	case b.GetOwnerId() != 0 && OwnerOf(b) == 0:
		return fmt.Errorf("a Space's owner id is between %d and %d", MinOwnerID, MaxOwnerID)
	}
	return nil
}

// Owner ids a Space may have (decision 189): never a system account, and
// never root, which NFS would otherwise map a peer's root to.
const (
	MinOwnerID = 1000
	MaxOwnerID = 65533
)

// OwnerOf is a Space's owner id, 0 when it has none or one outside
// MinOwnerID..MaxOwnerID.
func OwnerOf(b *maitrev1.Space) uint32 {
	if id := b.GetOwnerId(); id >= MinOwnerID && id <= MaxOwnerID {
		return id
	}
	return 0
}

// matches reports whether a Space's rule selects a Drive. An empty rule
// selects every Drive.
func matches(b *maitrev1.Space, d *maitrev1.DriveRecord) bool {
	have := map[string]bool{}
	for _, t := range d.GetAutoTags() {
		have[t] = true
	}
	for _, t := range d.GetTags() {
		have[t] = true
	}
	for _, need := range b.GetRequireTags() {
		if !have[need] {
			return false
		}
	}
	if len(b.GetNodeIds()) > 0 {
		ok := false
		for _, n := range b.GetNodeIds() {
			if n == d.GetNodeId() {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// SpacesOf resolves a Drive's Spaces: if any exclusive Space matches, the
// highest-priority one alone; otherwise every Space whose rule matches. spaces
// must be sorted highest priority first (Spaces does that).
func SpacesOf(d *maitrev1.DriveRecord, spaces []*maitrev1.Space) []string {
	var out []string
	for _, b := range spaces {
		if !matches(b, d) {
			continue
		}
		if b.GetExclusive() {
			return []string{b.GetSpaceId()}
		}
		out = append(out, b.GetSpaceId())
	}
	sort.Strings(out)
	return out
}

// DefaultSpace returns the Space new Service volumes land in, or nil when the
// Fleet has none marked.
func DefaultSpace(spaces []*maitrev1.Space) *maitrev1.Space {
	var best *maitrev1.Space
	for _, b := range spaces {
		if b.GetDefaultForNew() && (best == nil || b.GetCreatedUnixMs() > best.GetCreatedUnixMs()) {
			best = b
		}
	}
	return best
}

// LocalBranches groups this Node's mounted Drives' Space folders by Space: the
// branch lists each Pool is assembled from. Every Space gets an entry, empty
// when no local Drive belongs to it. Folders are created as needed.
func (m *Manager) LocalBranches(ctx context.Context) (map[string][]string, error) {
	spaces, err := Spaces(ctx, m.st)
	if err != nil {
		return nil, err
	}
	records, err := store.ListLive(ctx, m.st, KindDrive, func() *maitrev1.DriveRecord { return &maitrev1.DriveRecord{} })
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, b := range spaces {
		out[b.GetSpaceId()] = nil
	}
	for _, r := range records {
		if r.GetNodeId() != m.st.NodeID() {
			continue
		}
		dir := filepath.Join(m.Mounts, r.GetDriveId())
		if !mounted(dir) {
			continue
		}
		for _, id := range SpacesOf(r, spaces) {
			branch := filepath.Join(dir, id)
			if err := os.MkdirAll(branch, 0o755); err != nil {
				continue
			}
			out[id] = append(out[id], branch)
		}
	}
	return out, nil
}

// Folders describes the top-level folders of a mounted Drive (decision 124:
// Space folders, folders of removed Spaces, and files that were there before
// the Drive was adopted). Hidden entries and lost+found are skipped.
func Folders(dir string, spaces []*maitrev1.Space, mine []string) []*maitrev1.DriveFolder {
	known := map[string]bool{}
	for _, b := range spaces {
		known[b.GetSpaceId()] = true
	}
	member := map[string]bool{}
	for _, id := range mine {
		member[id] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []*maitrev1.DriveFolder
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "lost+found" || name == "System Volume Information" || name == "$RECYCLE.BIN" {
			continue
		}
		f := &maitrev1.DriveFolder{Name: name, Kind: "unsorted"}
		switch {
		case e.IsDir() && member[name]:
			f.Kind, f.SpaceId = "space", name
		case e.IsDir() && (known[name] || isSpaceMarker(filepath.Join(dir, name))):
			f.Kind, f.SpaceId = "orphan", name
		}
		out = append(out, f)
	}
	return out
}

// spaceMarker names the file a Space folder carries so a folder of a Space that
// was later removed is still recognised as one (and not as pre-adoption
// files).
const spaceMarker = ".maitre-space"

func isSpaceMarker(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, spaceMarker)); err == nil {
		return true
	}
	_, err := os.Stat(filepath.Join(dir, ".maitre-bin")) // the marker's first name
	return err == nil
}

// MarkSpaceFolders writes the marker into each branch given.
func MarkSpaceFolders(branches []string) {
	for _, b := range branches {
		p := filepath.Join(b, spaceMarker)
		if _, err := os.Stat(p); err != nil {
			_ = os.WriteFile(p, []byte("This folder belongs to a Maître Storage Space.\n"), 0o644)
		}
	}
}

// MigrateTiers converts a Fleet's Tier records (the model before Spaces) once:
// each Tier becomes a Space with the same rule, the former "default" Tier
// becomes the Space marked default for new volumes, and on this Node's Drives
// the files that lived at the Drive root (the old layout used the root as the
// Pool branch) move into the default Space's folder. Returns whether anything
// was converted. Decision 121.
func (m *Manager) MigrateTiers(ctx context.Context) (bool, error) {
	// The old Tier message shares field numbers 1-7 with Space (id, name,
	// placement "local"|"spanning", tags, nodes, priority, created), so the
	// old records decode as Spaces.
	// Records of the short-lived kind "bin" (the same message under its
	// first name) move to kind "space" as they are.
	converted := false
	if bins, err := store.ListLive(ctx, m.st, "bin", func() *maitrev1.Space { return &maitrev1.Space{} }); err == nil && len(bins) > 0 {
		for _, b := range bins {
			if _, err := m.st.Get(ctx, KindSpace, b.GetSpaceId(), &maitrev1.Space{}); err == nil {
				continue
			}
			if _, err := m.st.Put(ctx, KindSpace, b.GetSpaceId(), b, true); err != nil {
				return false, err
			}
			converted = true
		}
		for _, b := range bins {
			_, _ = m.st.Delete(ctx, "bin", b.GetSpaceId(), true)
		}
	}
	tiers, err := store.ListLive(ctx, m.st, "tier", func() *maitrev1.Space { return &maitrev1.Space{} })
	if err != nil || len(tiers) == 0 {
		return converted, err
	}
	spaces, _ := Spaces(ctx, m.st)
	have := map[string]bool{}
	for _, b := range spaces {
		have[b.GetSpaceId()] = true
	}
	for _, t := range tiers {
		if have[t.GetSpaceId()] {
			continue
		}
		b := &maitrev1.Space{
			SpaceId: t.GetSpaceId(), Name: t.GetName(), Placement: PlacementLocalFirst, RequireTags: t.GetRequireTags(), NodeIds: t.GetNodeIds(),
			Priority: t.GetPriority(), CreatedUnixMs: t.GetCreatedUnixMs(), SpanNodes: t.GetPlacement() == "spanning", DefaultForNew: t.GetSpaceId() == "default",
		}
		if b.CreatedUnixMs == 0 {
			b.CreatedUnixMs = time.Now().UnixMilli()
		}
		if _, err := m.st.Put(ctx, KindSpace, b.SpaceId, b, true); err != nil {
			return false, err
		}
	}
	for _, t := range tiers {
		if _, err := m.st.Delete(ctx, "tier", t.GetSpaceId(), true); err != nil {
			return false, err
		}
	}
	return true, nil
}

// MoveRootFilesIntoSpace moves the top-level entries of each of this Node's
// Drives into <drive>/<spaceID>/ once (marker .maitre-layout), for Drives
// adopted under the old root-as-branch layout. Space folders, hidden entries
// and lost+found stay. Renames within the same filesystem: no data copied.
func (m *Manager) MoveRootFilesIntoSpace(ctx context.Context, spaceID string) error {
	records, err := store.ListLive(ctx, m.st, KindDrive, func() *maitrev1.DriveRecord { return &maitrev1.DriveRecord{} })
	if err != nil {
		return err
	}
	spaces, _ := Spaces(ctx, m.st)
	known := map[string]bool{}
	for _, b := range spaces {
		known[b.GetSpaceId()] = true
	}
	for _, r := range records {
		if r.GetNodeId() != m.st.NodeID() {
			continue
		}
		dir := filepath.Join(m.Mounts, r.GetDriveId())
		if !mounted(dir) {
			continue
		}
		marker := filepath.Join(dir, ".maitre-layout")
		if _, err := os.Stat(marker); err == nil {
			continue
		}
		if r.GetOrigin() == "adopted" {
			// Pre-adoption files stay where they are: the file manager
			// moves them (decision 122). Only mark the layout.
			_ = os.WriteFile(marker, []byte("spaces\n"), 0o644)
			continue
		}
		target := filepath.Join(dir, spaceID)
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") || name == "lost+found" || known[name] {
				continue
			}
			_ = os.Rename(filepath.Join(dir, name), filepath.Join(target, name))
		}
		_ = os.WriteFile(marker, []byte("spaces\n"), 0o644)
	}
	return nil
}

// PoolDir is where a Space's Pool is mounted on a Node.
func PoolDir(spaceID string) string { return filepath.Join(pool.PoolsDir, spaceID) }
