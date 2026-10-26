package storage

import (
	"context"
	"errors"
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

// KindBin is the Record kind for Storage Bins (decisions 121-128).
const KindBin = "bin"

var binIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$|^[a-z0-9]$`)

// Placement policies for new files in a Bin's Pool.
const (
	PlacementLocalFirst      = "local_first"
	PlacementMostFree        = "most_free"
	PlacementMostFreePercent = "most_free_percent"
)

// Bins lists the Fleet's Bins, highest priority first.
func Bins(ctx context.Context, st *store.Store) ([]*maitrev1.Bin, error) {
	bins, err := store.ListLive(ctx, st, KindBin, func() *maitrev1.Bin { return &maitrev1.Bin{} })
	if err != nil {
		return nil, err
	}
	sort.SliceStable(bins, func(i, j int) bool { return bins[i].GetPriority() > bins[j].GetPriority() })
	return bins, nil
}

// ValidateBin is what a Node accepts as a Bin record (Console writes go
// through PutRecord): an id, a name, a known placement, and a rule when the
// Bin is exclusive, since an exclusive Bin with an empty rule would take
// every Drive from every other Bin.
func ValidateBin(b *maitrev1.Bin) error {
	switch {
	case b.GetBinId() == "" || b.GetName() == "":
		return errors.New("a Bin needs an id and a name")
	case !binIDRe.MatchString(b.GetBinId()):
		return errors.New("a Bin id is lower-case letters, digits and dashes")
	case b.GetPlacement() != PlacementLocalFirst && b.GetPlacement() != PlacementMostFree && b.GetPlacement() != PlacementMostFreePercent:
		return errors.New("placement must be local_first, most_free or most_free_percent")
	case b.GetExclusive() && len(b.GetRequireTags()) == 0 && len(b.GetNodeIds()) == 0:
		return errors.New("an exclusive Bin needs at least one required tag or Node; otherwise it would claim every Drive")
	}
	return nil
}

// matches reports whether a Bin's rule selects a Drive. An empty rule
// selects every Drive.
func matches(b *maitrev1.Bin, d *maitrev1.DriveRecord) bool {
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

// BinsOf resolves a Drive's Bins: if any exclusive Bin matches, the
// highest-priority one alone; otherwise every Bin whose rule matches. bins
// must be sorted highest priority first (Bins does that).
func BinsOf(d *maitrev1.DriveRecord, bins []*maitrev1.Bin) []string {
	var out []string
	for _, b := range bins {
		if !matches(b, d) {
			continue
		}
		if b.GetExclusive() {
			return []string{b.GetBinId()}
		}
		out = append(out, b.GetBinId())
	}
	sort.Strings(out)
	return out
}

// DefaultBin returns the Bin new Service volumes land in, or nil when the
// Fleet has none marked.
func DefaultBin(bins []*maitrev1.Bin) *maitrev1.Bin {
	var best *maitrev1.Bin
	for _, b := range bins {
		if b.GetDefaultForNew() && (best == nil || b.GetCreatedUnixMs() > best.GetCreatedUnixMs()) {
			best = b
		}
	}
	return best
}

// LocalBranches groups this Node's mounted Drives' Bin folders by Bin: the
// branch lists each Pool is assembled from. Every Bin gets an entry, empty
// when no local Drive belongs to it. Folders are created as needed.
func (m *Manager) LocalBranches(ctx context.Context) (map[string][]string, error) {
	bins, err := Bins(ctx, m.st)
	if err != nil {
		return nil, err
	}
	records, err := store.ListLive(ctx, m.st, KindDrive, func() *maitrev1.DriveRecord { return &maitrev1.DriveRecord{} })
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, b := range bins {
		out[b.GetBinId()] = nil
	}
	for _, r := range records {
		if r.GetNodeId() != m.st.NodeID() {
			continue
		}
		dir := filepath.Join(m.Mounts, r.GetDriveId())
		if !mounted(dir) {
			continue
		}
		for _, id := range BinsOf(r, bins) {
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
// Bin folders, folders of removed Bins, and files that were there before
// the Drive was adopted). Hidden entries and lost+found are skipped.
func Folders(dir string, bins []*maitrev1.Bin, mine []string) []*maitrev1.DriveFolder {
	known := map[string]bool{}
	for _, b := range bins {
		known[b.GetBinId()] = true
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
			f.Kind, f.BinId = "bin", name
		case e.IsDir() && (known[name] || isBinMarker(filepath.Join(dir, name))):
			f.Kind, f.BinId = "orphan", name
		}
		out = append(out, f)
	}
	return out
}

// binMarker names the file a Bin folder carries so a folder of a Bin that
// was later removed is still recognised as one (and not as pre-adoption
// files).
const binMarker = ".maitre-bin"

func isBinMarker(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, binMarker))
	return err == nil
}

// MarkBinFolders writes the marker into each branch given.
func MarkBinFolders(branches []string) {
	for _, b := range branches {
		p := filepath.Join(b, binMarker)
		if _, err := os.Stat(p); err != nil {
			_ = os.WriteFile(p, []byte("This folder belongs to a Maître Storage Bin.\n"), 0o644)
		}
	}
}

// MigrateTiers converts a Fleet's Tier records (the model before Bins) once:
// each Tier becomes a Bin with the same rule, the former "default" Tier
// becomes the Bin marked default for new volumes, and on this Node's Drives
// the files that lived at the Drive root (the old layout used the root as the
// Pool branch) move into the default Bin's folder. Returns whether anything
// was converted. Decision 121.
func (m *Manager) MigrateTiers(ctx context.Context) (bool, error) {
	// The old Tier message shares field numbers 1-7 with Bin (id, name,
	// placement "local"|"spanning", tags, nodes, priority, created), so the
	// old records decode as Bins.
	tiers, err := store.ListLive(ctx, m.st, "tier", func() *maitrev1.Bin { return &maitrev1.Bin{} })
	if err != nil || len(tiers) == 0 {
		return false, err
	}
	bins, _ := Bins(ctx, m.st)
	have := map[string]bool{}
	for _, b := range bins {
		have[b.GetBinId()] = true
	}
	for _, t := range tiers {
		if have[t.GetBinId()] {
			continue
		}
		b := &maitrev1.Bin{
			BinId: t.GetBinId(), Name: t.GetName(), Placement: PlacementLocalFirst, RequireTags: t.GetRequireTags(), NodeIds: t.GetNodeIds(),
			Priority: t.GetPriority(), CreatedUnixMs: t.GetCreatedUnixMs(), SpanNodes: t.GetPlacement() == "spanning", DefaultForNew: t.GetBinId() == "default",
		}
		if b.CreatedUnixMs == 0 {
			b.CreatedUnixMs = time.Now().UnixMilli()
		}
		if _, err := m.st.Put(ctx, KindBin, b.BinId, b, true); err != nil {
			return false, err
		}
	}
	for _, t := range tiers {
		if _, err := m.st.Delete(ctx, "tier", t.GetBinId(), true); err != nil {
			return false, err
		}
	}
	return true, nil
}

// MoveRootFilesIntoBin moves the top-level entries of each of this Node's
// Drives into <drive>/<binID>/ once (marker .maitre-layout), for Drives
// adopted under the old root-as-branch layout. Bin folders, hidden entries
// and lost+found stay. Renames within the same filesystem: no data copied.
func (m *Manager) MoveRootFilesIntoBin(ctx context.Context, binID string) error {
	records, err := store.ListLive(ctx, m.st, KindDrive, func() *maitrev1.DriveRecord { return &maitrev1.DriveRecord{} })
	if err != nil {
		return err
	}
	bins, _ := Bins(ctx, m.st)
	known := map[string]bool{}
	for _, b := range bins {
		known[b.GetBinId()] = true
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
			_ = os.WriteFile(marker, []byte("bins\n"), 0o644)
			continue
		}
		target := filepath.Join(dir, binID)
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
		_ = os.WriteFile(marker, []byte("bins\n"), 0o644)
	}
	return nil
}

// PoolDir is where a Bin's Pool is mounted on a Node.
func PoolDir(binID string) string { return filepath.Join(pool.PoolsDir, binID) }
