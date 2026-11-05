package services_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
)

// pruneRunner answers podman's image queries with a store holding more than
// the Services pin: two superseded digests of jellyfin, an image of a Service
// that was removed, and a dangling layer.
type pruneRunner struct {
	cmds []string
	ids  map[string]string // reference -> image ID podman resolves it to
}

func (p *pruneRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	p.cmds = append(p.cmds, name+" "+strings.Join(args, " "))
	switch {
	case name == "podman" && len(args) > 2 && args[0] == "image" && args[1] == "inspect":
		if id, ok := p.ids[args[len(args)-1]]; ok {
			return id + "\n", nil
		}
		return "", errNoSuchImage
	case name == "podman" && len(args) > 0 && args[0] == "images":
		return strings.Join([]string{
			"id-live\tdocker.io/jellyfin/jellyfin:10.10.7",
			"id-prev\tdocker.io/jellyfin/jellyfin:10.10.7",
			"id-old1\tdocker.io/jellyfin/jellyfin:10.10.6",
			"id-old2\tdocker.io/jellyfin/jellyfin:10.10.5",
			"id-gone\tdocker.io/docmost/docmost:0.9.1",
			"id-dangle\t<none>:<none>",
		}, "\n"), nil
	case name == "podman" && len(args) > 1 && args[0] == "info":
		return "/var/lib/containers/storage\n", nil
	case name == "systemctl" && len(args) > 1 && args[0] == "show":
		return "LoadState=loaded\nActiveState=active\nSubState=running\nNRestarts=0\nResult=success\n", nil
	}
	return "", nil
}

var errNoSuchImage = &noSuchImage{}

type noSuchImage struct{}

func (*noSuchImage) Error() string { return "no such image" }

func (p *pruneRunner) removed() []string {
	var out []string
	for _, c := range p.cmds {
		if strings.HasPrefix(c, "podman rmi ") {
			out = append(out, strings.TrimPrefix(c, "podman rmi "))
		}
	}
	return out
}

// Decision 316: nothing has ever removed an image, so a Node holds every
// digest it has pulled, not the two each Service pins. The prune keeps what
// its own Services name and nothing else.
func TestPruneKeepsWhatThisNodesServicesName(t *testing.T) {
	ctx := context.Background()
	run := &pruneRunner{ids: map[string]string{}}
	spec := single("jellyfin", 8096)
	spec.NodeId = "node-a"
	spec.Image = "docker.io/jellyfin/jellyfin:10.10.7@sha256:live"
	spec.PreviousImage = "docker.io/jellyfin/jellyfin:10.10.7@sha256:prev"
	run.ids[spec.GetImage()] = "id-live"
	run.ids[spec.GetPreviousImage()] = "id-prev"

	mgr, store2 := managerOver(t, run)
	if _, err := store2.Put(ctx, services.KindService, spec.GetName(), spec, true); err != nil {
		t.Fatal(err)
	}

	res, err := mgr.PruneImages(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	got := run.removed()
	for _, keep := range []string{"id-live", "id-prev"} {
		if slicesContains(got, keep) {
			t.Errorf("prune removed %s, which a Service names", keep)
		}
	}
	for _, want := range []string{"id-old1", "id-old2", "id-gone", "id-dangle"} {
		if !slicesContains(got, want) {
			t.Errorf("prune kept %s, which nothing names", want)
		}
	}
	if res.GetKept() != 2 {
		t.Errorf("kept %d, want the 2 jellyfin pins", res.GetKept())
	}
	if !slicesContains(run.cmds, "podman image prune -f") {
		t.Error("dangling layers were not pruned")
	}
}

// Decision 320 is the default; dropping it is what gets the space back now.
func TestPruneCanDropRollbackTargets(t *testing.T) {
	ctx := context.Background()
	run := &pruneRunner{ids: map[string]string{}}
	spec := single("jellyfin", 8096)
	spec.NodeId = "node-a"
	spec.Image = "docker.io/jellyfin/jellyfin:10.10.7@sha256:live"
	spec.PreviousImage = "docker.io/jellyfin/jellyfin:10.10.7@sha256:prev"
	run.ids[spec.GetImage()] = "id-live"
	run.ids[spec.GetPreviousImage()] = "id-prev"

	mgr, store2 := managerOver(t, run)
	if _, err := store2.Put(ctx, services.KindService, spec.GetName(), spec, true); err != nil {
		t.Fatal(err)
	}

	if _, err := mgr.PruneImages(ctx, false); err != nil {
		t.Fatal(err)
	}
	got := run.removed()
	if !slicesContains(got, "id-prev") {
		t.Errorf("previous_image was kept with keepRollback off: %v", got)
	}
	if slicesContains(got, "id-live") {
		t.Errorf("prune removed the live image: %v", got)
	}
}

// Decision 318: Boulder pulled every one of Avalon's Services. It owns none,
// so none of what it holds is its to keep.
func TestPruneOnANodeThatOwnsNoServicesKeepsNothing(t *testing.T) {
	ctx := context.Background()
	run := &pruneRunner{ids: map[string]string{}}
	mgr, store2 := managerOver(t, run)
	theirs := single("jellyfin", 8096)
	theirs.NodeId, theirs.Address = "node-b", "10.90.0.50"
	theirs.Image = "docker.io/jellyfin/jellyfin:10.10.7@sha256:live"
	run.ids[theirs.GetImage()] = "id-live"
	if _, err := store2.Put(ctx, services.KindService, theirs.GetName(), theirs, true); err != nil {
		t.Fatal(err)
	}

	res, err := mgr.PruneImages(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.GetKept() != 0 {
		t.Errorf("kept %d images for Services this Node does not run", res.GetKept())
	}
	if !slicesContains(run.removed(), "id-live") {
		t.Error("another Node's image was kept")
	}
}

func slicesContains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
