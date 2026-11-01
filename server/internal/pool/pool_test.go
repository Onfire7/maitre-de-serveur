package pool

import (
	"reflect"
	"strings"
	"testing"
)

// Decisions 201, 202: each fill maps to a mergerfs create policy; with
// local Drives preferred and peers present, the local ones come first in
// the fill's order and "ff" takes the first with room.
func TestPlan(t *testing.T) {
	stat := func(d string) (uint64, float64, bool) {
		s, ok := map[string][2]float64{"/a": {500, 0.5}, "/b": {900, 0.1}, "/c": {100, 0.9}, "/peer": {5000, 0.8}}[d]
		return uint64(s[0]), s[1], ok
	}
	all := []string{"/a", "/unknown", "/b", "/c", "/peer"}
	for _, c := range []struct {
		p      Placement
		local  int
		order  []string
		create string
	}{
		{Placement{Fill: "most_free"}, 4, all, "mfs"},
		{Placement{Fill: "most_free_percent"}, 4, all, "pfrd"},
		{Placement{Fill: "least_free"}, 4, all, "lfs"},
		{Placement{Fill: "least_free_percent"}, 4, []string{"/b", "/a", "/peer", "/c", "/unknown"}, "ff"},
		{Placement{Fill: "most_free", PreferLocal: true}, 4, []string{"/b", "/a", "/c", "/unknown", "/peer"}, "ff"},
		{Placement{Fill: "most_free_percent", PreferLocal: true}, 4, []string{"/c", "/a", "/b", "/unknown", "/peer"}, "ff"},
		{Placement{Fill: "least_free", PreferLocal: true}, 4, []string{"/c", "/a", "/b", "/unknown", "/peer"}, "ff"},
		{Placement{Fill: "least_free_percent", PreferLocal: true}, 4, []string{"/b", "/a", "/c", "/unknown", "/peer"}, "ff"},
		// Without peers, preferring local Drives changes nothing.
		{Placement{Fill: "most_free", PreferLocal: true}, 5, all, "mfs"},
	} {
		order, create := Plan(c.p, all, c.local, stat)
		if create != c.create || !reflect.DeepEqual(order, c.order) {
			t.Errorf("%+v: %v %s, want %v %s", c.p, order, create, c.order, c.create)
		}
	}
	if got := PlacementOf("local_first", false); got != (Placement{Fill: "most_free", PreferLocal: true}) {
		t.Errorf("local_first: %+v", got)
	}
	if got := PlacementOf("least_free", true); got != (Placement{Fill: "least_free", PreferLocal: true}) {
		t.Errorf("least_free: %+v", got)
	}
}

func TestBranches(t *testing.T) {
	got := Branches([]string{"/a/x", "/b/x", "/c/x"}, map[string]bool{"/b/x": true})
	if got != "/a/x:/b/x=NC:/c/x" {
		t.Fatalf("Branches = %q", got)
	}
}

// Decision 283: the Pool peers mount over NFS keeps the nodes NFS holds
// handles to, numbers inodes by path and reports a folder's times from the
// branch changed last; the Node's own full Pool does not need any of it.
func TestOptionsForExportedPool(t *testing.T) {
	own := Options("maitre-data", "ff", false)
	if strings.Contains(own, "never-forget-nodes") || strings.Contains(own, "func.getattr") || !strings.Contains(own, "category.create=ff,") {
		t.Fatalf("own Pool options %q", own)
	}
	shared := Options("maitre-shared-data", "ff", true)
	for _, want := range []string{"never-forget-nodes=true", "inodecalc=path-hash", "func.getattr=newest", "fsname=maitre-shared-data"} {
		if !strings.Contains(shared, want) {
			t.Fatalf("shared Pool options %q lack %s", shared, want)
		}
	}
}
