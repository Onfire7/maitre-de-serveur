package storage

import (
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

func TestTierOf(t *testing.T) {
	tiers := []*maitrev1.Tier{
		{TierId: "default", Priority: -1000},
		{TierId: "media", RequireTags: []string{"hdd", "media"}, Priority: 10},
		{TierId: "fast", RequireTags: []string{"ssd"}, Priority: 20},
		{TierId: "node-b-only", RequireTags: []string{"ssd"}, NodeIds: []string{"b"}, Priority: 30},
		{TierId: "empty-rule", Priority: 100},
	}
	cases := []struct {
		d    *maitrev1.DriveRecord
		want string
	}{
		{&maitrev1.DriveRecord{NodeId: "a", AutoTags: []string{"hdd", "large"}, Tags: []string{"media"}}, "media"},
		{&maitrev1.DriveRecord{NodeId: "a", AutoTags: []string{"hdd", "large"}}, "default"},
		{&maitrev1.DriveRecord{NodeId: "a", AutoTags: []string{"ssd", "nvme"}}, "fast"},
		{&maitrev1.DriveRecord{NodeId: "b", AutoTags: []string{"ssd"}}, "node-b-only"},
	}
	for _, c := range cases {
		if got := TierOf(c.d, tiers); got != c.want {
			t.Errorf("TierOf(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
