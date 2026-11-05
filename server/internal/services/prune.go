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

// ImageStoreDir is storeDir for other packages: the metrics collector
// gauges the filesystem the images are on (decision 317), and after
// decision 319 that is not the root partition.
func (m *Manager) ImageStoreDir(ctx context.Context) string { return m.storeDir(ctx) }

// storeDir is where podman keeps its images, which decision 319 moves off
// the root partition. Asked of podman rather than assumed, so it follows —
// and cached, since a reading every ten seconds should not run `podman
// info` and the graphroot does not move while the Node is up.
func (m *Manager) storeDir(ctx context.Context) string {
	m.storeMu.Lock()
	defer m.storeMu.Unlock()
	if m.store_ != "" {
		return m.store_
	}
	m.store_ = "/var/lib/containers/storage"
	if out, err := m.run.Run(ctx, "podman", "info", "--format", "{{.Store.GraphRoot}}"); err == nil {
		if dir := strings.TrimSpace(out); dir != "" {
			m.store_ = dir
		}
	}
	return m.store_
}

// ListImages says what the image store holds and which Service, if any,
// names each one (decision 326's basic tier). Nothing could ask a Node
// this before, which is part of why a store that grew without bound went
// unnoticed for as long as it did.
func (m *Manager) ListImages(ctx context.Context) (*maitrev1.ListImagesResponse, error) {
	res := &maitrev1.ListImagesResponse{StoreDir: m.storeDir(ctx)}
	res.FreeBytes = m.freeBytes(res.GetStoreDir())
	res.TotalBytes = m.totalBytes(res.GetStoreDir())

	// Which Service names what, by the ID podman resolves its reference
	// to: a pinned digest may name an index or one platform's manifest,
	// and comparing the strings would mismatch half of them.
	type owner struct {
		service  string
		rollback bool
	}
	named := map[string]owner{}
	specs, err := m.mine(ctx)
	if err != nil {
		return nil, err
	}
	for _, spec := range specs {
		for _, h := range imageHolders(spec) {
			for rollback, ref := range map[bool]string{false: *h.image, true: *h.prev} {
				if ref == "" {
					continue
				}
				out, err := m.run.Run(ctx, "podman", "image", "inspect", "--format", "{{.Id}}", ref)
				if err != nil {
					continue
				}
				if id := strings.TrimSpace(out); id != "" {
					if cur, ok := named[id]; !ok || (cur.rollback && !rollback) {
						named[id] = owner{spec.GetName(), rollback}
					}
				}
			}
		}
	}

	out, err := m.run.Run(ctx, "podman", "images", "--all", "--format", "{{.Id}}\t{{.Repository}}:{{.Tag}}\t{{.Size}}\t{{.CreatedAt}}")
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(strings.TrimSpace(line), "\t")
		if len(f) < 2 || f[0] == "" {
			continue
		}
		img := &maitrev1.ListImagesResponse_Image{Id: f[0], Reference: f[1]}
		if len(f) > 2 {
			img.SizeBytes = podmanSize(f[2])
		}
		if o, ok := named[f[0]]; ok {
			img.Service, img.RollbackTarget = o.service, o.rollback
		}
		res.Images = append(res.Images, img)
	}
	return res, nil
}

// podmanSize reads podman's human size ("1.23 GB") back into bytes. Its
// --format has no raw size, and asking per image would be a subprocess
// each; an approximation is what a listing needs.
func podmanSize(s string) uint64 {
	var n float64
	var unit string
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%f %s", &n, &unit); err != nil {
		return 0
	}
	switch strings.ToUpper(unit) {
	case "KB":
		n *= 1e3
	case "MB":
		n *= 1e6
	case "GB":
		n *= 1e9
	case "TB":
		n *= 1e12
	}
	return uint64(n)
}
