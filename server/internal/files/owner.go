package files

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/Onfire7/maitre-de-serveur/server/internal/owner"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
)

// spaceOf names the Space a path on this Node belongs to: under a Pool,
// or under a Drive's folder for a Space; "" for a Drive's own root files.
func (m *Manager) spaceOf(path string) string {
	if r, err := filepath.Rel(m.Pools, path); err == nil && !strings.HasPrefix(r, "..") && r != "." {
		return strings.Split(r, string(filepath.Separator))[0]
	}
	if r, err := filepath.Rel(m.Mounts, path); err == nil && !strings.HasPrefix(r, "..") {
		parts := strings.Split(r, string(filepath.Separator))
		if len(parts) >= 2 {
			spaces, _ := storage.Spaces(context.Background(), m.st)
			for _, s := range spaces {
				if s.GetSpaceId() == parts[1] {
					return parts[1]
				}
			}
		}
	}
	return ""
}

// ownerOf is a Space's owner id (decision 182), 0 when it has none.
func (m *Manager) ownerOf(space string) uint32 {
	if space == "" {
		return 0
	}
	spaces, _ := storage.Spaces(context.Background(), m.st)
	for _, s := range spaces {
		if s.GetSpaceId() == space {
			return storage.OwnerOf(s)
		}
	}
	return 0
}

// own gives a new file or folder its Space's owner (decision 173); ownTree
// does the same for everything under a folder (unzipped, moved in).
func (m *Manager) own(path string) {
	if err := owner.Apply(path, m.ownerOf(m.spaceOf(path))); err != nil {
		m.log.Warn("owner", "path", path, "err", err)
	}
}

func (m *Manager) ownTree(path string) {
	if err := owner.Tree(path, m.ownerOf(m.spaceOf(path))); err != nil {
		m.log.Warn("owner", "path", path, "err", err)
	}
}

// isSpace reports whether id names a Space of the Fleet.
func (m *Manager) isSpace(ctx context.Context, id string) bool {
	spaces, _ := storage.Spaces(ctx, m.st)
	for _, s := range spaces {
		if s.GetSpaceId() == id {
			return true
		}
	}
	return false
}
