package pool

import (
	"reflect"
	"testing"
)

// Decision 201: each placement maps to a mergerfs create policy, and
// least_free_percent orders the branches fullest first.
func TestPlacements(t *testing.T) {
	for placement, want := range map[string]string{"local_first": "mfs", "most_free": "mfs", "most_free_percent": "pfrd", "least_free": "lfs", "least_free_percent": "ff", "": "mfs"} {
		if got := CreatePolicy(placement, false); got != want {
			t.Errorf("%q: %s, want %s", placement, got, want)
		}
	}
	if got := CreatePolicy("local_first", true); got != "ff" {
		t.Errorf("local_first with peers: %s", got)
	}
	share := map[string]float64{"/a": 0.5, "/b": 0.1, "/c": 0.9}
	got := ByFreeShare([]string{"/a", "/unknown", "/b", "/c"}, func(d string) (float64, bool) { f, ok := share[d]; return f, ok })
	if want := []string{"/b", "/a", "/c", "/unknown"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}
