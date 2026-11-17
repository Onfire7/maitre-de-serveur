package nodeservice

import (
	"context"
	"slices"
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
)

// A Node lists what its agent serves, so the Console offers only what
// works there (plan C5): every agent's features, and the ones it was
// given ("terminal", ADR 0011).
func TestNodeInfoListsTheAgentsFeatures(t *testing.T) {
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		extra []string
		want  []string
	}{
		{nil, []string{"components", "setup"}},
		{[]string{"terminal"}, []string{"components", "setup", "terminal"}},
	} {
		res, err := New(id, Info{Features: c.extra}, nil).GetNodeInfo(context.Background(), &maitrev1.GetNodeInfoRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.GetNode().GetFeatures(); !slices.Equal(got, c.want) {
			t.Fatalf("features %v, want %v", got, c.want)
		}
	}
}
