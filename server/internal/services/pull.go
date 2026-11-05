package services

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
)

// Everything a pull costs before it starts (decisions 315-321). A Service
// that cannot start used to re-download its image every five seconds, and
// the daily auto-update downloaded every image of every Service to learn a
// digit it could have asked for. Three guards, in the order they are
// cheapest: is this Service even ours to act on, is the digest already the
// one the tag names, and is there room for what comes down.

// PullFloor is the free space the image store wants before a pull. It is a
// floor and not a promise: most images are well under 2 GB, and a pull
// needs room for the compressed blobs and the extracted layers at once, so
// anything less than this is a pull that probably cannot finish. A Node
// below it prunes first (decision 316) and refuses if that was not enough,
// which is the difference between one clear error and three terabytes.
const PullFloor = 4 << 30

// applicable reports the error apply would give for a Service that is not
// this Node's to act on, before any network is used for it. apply refuses
// at the very end, which is correct and also far too late: Boulder spent
// about 5 GB a day downloading images for Services it would then refuse.
func (m *Manager) applicable(ctx context.Context, spec *maitrev1.ServiceSpec) error {
	// Ownership first, and on its own: it is the thing that was actually
	// wrong, it needs no signing configured to be true, and it is the one
	// the Fleet record states outright.
	if owner := spec.GetNodeId(); owner != "" && owner != m.store.NodeID() {
		return m.notOurs(spec)
	}
	rec, err := m.store.Raw(ctx, KindService, spec.GetName())
	if err != nil || rec.GetDeleted() || m.cfg.Authentic == nil || m.cfg.Authentic(rec) {
		return nil
	}
	return m.notOurs(spec)
}

// notOurs is the one message for a record this Node did not sign, so apply
// and the pull guards cannot drift apart.
func (m *Manager) notOurs(spec *maitrev1.ServiceSpec) error {
	// Decision 318: the common way here was another Node's Service, which
	// this Node should never have been acting on; say whose it is rather
	// than advise a repair that would not be the user's to make. A record
	// of this Node that is genuinely unsigned (from before 189) still asks
	// to be installed again.
	if owner := spec.GetNodeId(); owner != "" && owner != m.store.NodeID() {
		if len(owner) > 8 {
			owner = owner[:8]
		}
		return fmt.Errorf("%s belongs to another Node (%s), which runs and updates it", spec.GetName(), owner)
	}
	return fmt.Errorf("%s: its record is not signed by this Node; remove and install it again", spec.GetName())
}

// index is the part of an image index this needs: which manifest belongs to
// which platform.
type imageIndex struct {
	Manifests []struct {
		Digest   string `json:"digest"`
		Platform struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"platform"`
	} `json:"manifests"`
}

// remoteDigests asks the registry which digests a tag names now, fetching
// the manifest alone and not one layer. Two of them come back because a
// pinned digest may be either, and a check that assumes one kind mismatches
// every image of the other and pulls every time — an optimisation that
// looks right and does nothing:
//
//   - index is the digest of what the tag resolves to, which for a
//     multi-architecture tag is the image index. This is what a registry
//     and `skopeo inspect` call "the" digest.
//   - platform is the digest of the entry inside that index matching this
//     Node's os and architecture, which is what `podman image inspect`
//     reports and therefore what pins spec.Image.
//
// Measured on docker.io/library/alpine:3.20: index sha256:d9e853e8…,
// platform (linux/amd64) sha256:c64c687c…. An image with a single manifest
// has no index, so platform comes back empty and index is its own digest,
// which is also what gets pinned.
func (m *Manager) remoteDigests(ctx context.Context, tagRef string) (index, platform string, err error) {
	// A reference carrying both a tag and a digest is refused by the
	// tooling ("Docker references with both a tag and digest are currently
	// not supported"), so only the tag goes.
	tagRef, _, _ = strings.Cut(tagRef, "@")
	if tagRef == "" {
		return "", "", fmt.Errorf("no image reference")
	}
	ref := "docker://" + tagRef
	out, err := m.run.Run(ctx, "skopeo", "inspect", "--format", "{{.Digest}}", ref)
	if err != nil {
		return "", "", fmt.Errorf("inspect %s: %w", tagRef, err)
	}
	index = strings.TrimSpace(out)
	raw, err := m.run.Run(ctx, "skopeo", "inspect", "--raw", ref)
	if err != nil {
		return index, "", nil // the index digest alone is still worth having
	}
	var idx imageIndex
	if err := json.Unmarshal([]byte(raw), &idx); err != nil {
		return index, "", nil
	}
	for _, mf := range idx.Manifests {
		if mf.Platform.OS == runtime.GOOS && mf.Platform.Architecture == runtime.GOARCH {
			return index, mf.Digest, nil
		}
	}
	return index, "", nil
}

// pinnedIsCurrent reports whether the digest already pinned in ref is one
// the tag still names, so nothing need be pulled. Unknown counts as "not
// current": every failure here falls back to the pull that used to be the
// only way, so a registry that cannot be reached or tooling that is missing
// costs bandwidth, never correctness.
func (m *Manager) pinnedIsCurrent(ctx context.Context, ref string) bool {
	tagRef, pinned, ok := strings.Cut(ref, "@")
	if !ok || pinned == "" || tagRef == "" {
		return false
	}
	index, platform, err := m.remoteDigests(ctx, tagRef)
	if err != nil {
		return false
	}
	return pinned == index || (platform != "" && pinned == platform)
}

// ensureRoomToPull makes room for a pull or says why there is none
// (decisions 316 and 321). Below the floor it prunes what no Service of
// this Node names, which is decision 316's trigger: free space crossing a
// threshold, not a schedule, so a stopped Service keeps its image in the
// ordinary case. Rollback targets are kept even here, since decision 320
// wants them indefinitely and `maitre service prune -drop-rollback` is the
// deliberate way to give them up.
func (m *Manager) ensureRoomToPull(ctx context.Context) error {
	dir := m.storeDir(ctx)
	free := m.freeBytes(dir)
	if free == 0 || free >= PullFloor {
		// 0 means statfs could not answer, which is not a reason to refuse.
		return nil
	}
	if m.cfg.Log != nil {
		m.cfg.Log.Info("image store low on space; pruning before pulling", "store", dir, "free_bytes", free)
	}
	res, err := m.PruneImages(ctx, true)
	if err != nil {
		return fmt.Errorf("%s has %s free and a pull needs about %s; pruning it failed: %w",
			dir, iec(free), iec(PullFloor), err)
	}
	if free = m.freeBytes(dir); free >= PullFloor {
		return nil
	}
	return fmt.Errorf("%s has %s free and a pull needs about %s; pruning gave back %s, so there is nothing left to reclaim automatically: "+
		"remove a Service, add a Drive, or give up the Rollback images with `maitre service prune -drop-rollback`",
		dir, iec(free), iec(PullFloor), iec(res.GetFreeAfterBytes()-res.GetFreeBeforeBytes()))
}

// freeBytes measures through the Manager so a test can say what a
// filesystem has left without one.
func (m *Manager) freeBytes(dir string) uint64 {
	if m.cfg.FreeBytes != nil {
		return m.cfg.FreeBytes(dir)
	}
	return storage.FreeBytes(dir)
}

// iec is a byte count as a person reads it.
func iec(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}
