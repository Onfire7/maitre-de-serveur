package storage

import (
	"context"
	"sort"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// DefaultTier is the Tier every Drive falls into when no other matches.
const DefaultTier = "default"

// EnsureDefaultTier writes the "default" Tier record if the Fleet has none.
func EnsureDefaultTier(ctx context.Context, st *store.Store) error {
	if _, err := st.Get(ctx, KindTier, DefaultTier, &maitrev1.Tier{}); err == nil {
		return nil
	}
	_, err := st.Put(ctx, KindTier, DefaultTier, &maitrev1.Tier{
		TierId: DefaultTier, Name: "Default", Placement: "local", Priority: -1000, CreatedUnixMs: time.Now().UnixMilli(),
	}, true)
	return err
}

// Tiers lists the Fleet's Tiers, highest priority first.
func Tiers(ctx context.Context, st *store.Store) ([]*maitrev1.Tier, error) {
	tiers, err := store.ListLive(ctx, st, KindTier, func() *maitrev1.Tier { return &maitrev1.Tier{} })
	if err != nil {
		return nil, err
	}
	sort.SliceStable(tiers, func(i, j int) bool { return tiers[i].GetPriority() > tiers[j].GetPriority() })
	return tiers, nil
}

// TierOf resolves which Tier a Drive belongs to: the highest-priority Tier
// whose required tags are all on the Drive and whose Node list (if any)
// includes the Drive's Node; "default" otherwise.
func TierOf(d *maitrev1.DriveRecord, tiers []*maitrev1.Tier) string {
	have := map[string]bool{}
	for _, t := range d.GetAutoTags() {
		have[t] = true
	}
	for _, t := range d.GetTags() {
		have[t] = true
	}
	best, bestPrio := DefaultTier, int32(-1<<31)
	for _, t := range tiers {
		if t.GetTierId() == DefaultTier {
			continue
		}
		ok := true
		for _, need := range t.GetRequireTags() {
			if !have[need] {
				ok = false
				break
			}
		}
		if ok && len(t.GetNodeIds()) > 0 {
			ok = false
			for _, n := range t.GetNodeIds() {
				if n == d.GetNodeId() {
					ok = true
				}
			}
		}
		if ok && t.GetPriority() > bestPrio {
			best, bestPrio = t.GetTierId(), t.GetPriority()
		}
	}
	return best
}

// LocalBranches groups this Node's mounted Drives by Tier: the branch
// lists each Pool is assembled from.
func (m *Manager) LocalBranches(ctx context.Context) (map[string][]string, error) {
	tiers, err := Tiers(ctx, m.st)
	if err != nil {
		return nil, err
	}
	records, err := store.ListLive(ctx, m.st, KindDrive, func() *maitrev1.DriveRecord { return &maitrev1.DriveRecord{} })
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, t := range tiers {
		out[t.GetTierId()] = nil
	}
	for _, r := range records {
		if r.GetNodeId() != m.st.NodeID() {
			continue
		}
		dir := m.Mounts + "/" + r.GetDriveId()
		if !mounted(dir) {
			continue
		}
		tier := TierOf(r, tiers)
		out[tier] = append(out[tier], dir)
	}
	return out, nil
}
