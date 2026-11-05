package services

import (
	"context"
	"fmt"
	"strings"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// PruneImages deletes container images this Node's Services do not name
// (decision 316). Nothing in Maître has ever removed an image, so a Node
// holds every digest it has ever pulled, not just the two each Service
// pins, and a Node has no terminal: this is the only way the space comes
// back.
//
// What is kept is decided by reference, resolved through podman itself
// rather than by comparing digests: a pinned digest may name an image index
// or one platform's manifest, and matching the wrong kind would delete an
// image in use. Images are removed by the ID podman resolves a kept
// reference to, so the two views cannot disagree.
//
// keepRollback keeps each Service's previous_image, the target its Rollback
// needs (decision 320); without it those come back as space and Rollback
// has nowhere to go.
func (m *Manager) PruneImages(ctx context.Context, keepRollback bool) (*maitrev1.PruneImagesResponse, error) {
	specs, err := m.mine(ctx)
	if err != nil {
		return nil, err
	}
	keep := map[string]bool{} // image ID -> kept
	for _, spec := range specs {
		for _, h := range imageHolders(spec) {
			refs := []string{*h.image}
			if keepRollback && h.prev != nil {
				refs = append(refs, *h.prev)
			}
			for _, ref := range refs {
				if ref == "" {
					continue
				}
				// Local lookup: no registry is contacted, and a reference
				// this Node never pulled simply has no ID.
				out, err := m.run.Run(ctx, "podman", "image", "inspect", "--format", "{{.Id}}", ref)
				if err != nil {
					continue
				}
				if id := strings.TrimSpace(out); id != "" {
					keep[id] = true
				}
			}
		}
	}

	res := &maitrev1.PruneImagesResponse{StoreDir: m.storeDir(ctx), Kept: uint32(len(keep))}
	res.FreeBeforeBytes = m.freeBytes(res.GetStoreDir())

	out, err := m.run.Run(ctx, "podman", "images", "--all", "--format", "{{.Id}}\t{{.Repository}}:{{.Tag}}")
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		id, name, _ := strings.Cut(strings.TrimSpace(line), "\t")
		if id == "" || keep[id] || seen[id] {
			continue
		}
		seen[id] = true
		// A removal that fails is reported and the rest go on: an image a
		// container still holds is not worth refusing the whole prune for.
		if _, err := m.run.Run(ctx, "podman", "rmi", id); err != nil {
			if m.cfg.Log != nil {
				m.cfg.Log.Warn("prune image", "image", name, "err", err)
			}
			continue
		}
		if name == "" || strings.HasSuffix(name, ":<none>") {
			name = id
		}
		res.Removed = append(res.Removed, name)
	}
	// Layers left behind by a pull that died partway, which is how both
	// Nodes filled: these belong to no image at all.
	if _, err := m.run.Run(ctx, "podman", "image", "prune", "-f"); err != nil && m.cfg.Log != nil {
		m.cfg.Log.Warn("prune dangling layers", "err", err)
	}
	res.FreeAfterBytes = m.freeBytes(res.GetStoreDir())
	return res, nil
}

// storeDir is where podman keeps its images, which decision 319 moves off
// the root partition. Asked of podman rather than assumed, so it follows.
func (m *Manager) storeDir(ctx context.Context) string {
	out, err := m.run.Run(ctx, "podman", "info", "--format", "{{.Store.GraphRoot}}")
	if err == nil {
		if dir := strings.TrimSpace(out); dir != "" {
			return dir
		}
	}
	return "/var/lib/containers/storage"
}
