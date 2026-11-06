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
	gone map[string]bool   // image ID -> removed by this prune
}

// names is what podman knows each image by. A pulled image has a tag and
// a digest reference both, which is why `podman rmi <id>` refuses almost
// every image in a real store.
var pruneNames = map[string][]string{
	"id-live":   {"docker.io/jellyfin/jellyfin:10.10.7", "docker.io/jellyfin/jellyfin@sha256:live"},
	"id-prev":   {"docker.io/jellyfin/jellyfin@sha256:prev"},
	"id-old1":   {"docker.io/jellyfin/jellyfin:10.10.6", "docker.io/jellyfin/jellyfin@sha256:old1"},
	"id-old2":   {"docker.io/jellyfin/jellyfin:10.10.5", "docker.io/jellyfin/jellyfin@sha256:old2"},
	"id-gone":   {"docker.io/docmost/docmost:0.9.1", "docker.io/docmost/docmost@sha256:gone"},
	"id-dangle": nil, // no name at all, so it goes by ID
}

func (p *pruneRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	p.cmds = append(p.cmds, name+" "+strings.Join(args, " "))
	switch {
	case name == "podman" && len(args) > 2 && args[0] == "image" && args[1] == "exists":
		if p.gone[args[len(args)-1]] {
			return "", errNoSuchImage
		}
		return "", nil
	case name == "podman" && len(args) > 0 && args[0] == "rmi":
		// podman refuses an ID that has more than one name, which is what
		// the prune has to avoid asking for.
		for id, names := range pruneNames {
			if len(names) > 1 && slicesContains(args[1:], id) {
				return "", errAmbiguousID
			}
		}
		for id, names := range pruneNames {
			for _, n := range names {
				if slicesContains(args[1:], n) {
					p.gone[id] = true
				}
			}
		}
		if slicesContains(args[1:], "id-dangle") {
			p.gone["id-dangle"] = true
		}
		return "", nil
	case name == "podman" && len(args) > 3 && args[0] == "image" && args[1] == "inspect" && args[2] == "--format" && strings.Contains(args[3], "RepoTags"):
		return strings.Join(pruneNames[args[len(args)-1]], "\n") + "\n", nil
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

var (
	errNoSuchImage = &podmanErr{"no such image"}
	errAmbiguousID = &podmanErr{"unable to delete image by ID with more than one tag; please force removal"}
)

type podmanErr struct{ msg string }

func (e *podmanErr) Error() string { return e.msg }

// removedIDs is which images the prune took, whatever names it used to
// take them by -- the question most of these tests are asking. removed()
// is for the one test that is asking about the names themselves.
func (p *pruneRunner) removedIDs() []string {
	var out []string
	for _, c := range p.removed() {
		args := strings.Fields(c)
		for id, names := range pruneNames {
			for _, n := range names {
				if slicesContains(args, n) && !slicesContains(out, id) {
					out = append(out, id)
				}
			}
		}
		for _, a := range args {
			if strings.HasPrefix(a, "id-") && !slicesContains(out, a) {
				out = append(out, a)
			}
		}
	}
	return out
}

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
	run := &pruneRunner{ids: map[string]string{}, gone: map[string]bool{}}
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
	got := run.removedIDs()
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

// An image with more than one name cannot be deleted by ID: podman
// refuses outright ("unable to delete image ... by ID with more than one
// tag ([docker.io/library/redis:7.4.12-alpine
// docker.io/library/redis@sha256:8da19c73...]); please force removal").
// A pulled image has a tag and a digest reference both, so that is almost
// every image there is -- on Avalon it was all of them, and the prune
// answered "removed 0" and freed nothing on a root at 96%. The names also
// cannot come from `podman images`, which prints tags and never the
// digest reference that makes the ID ambiguous.
func TestPruneRemovesByEveryNamePodmanKnows(t *testing.T) {
	ctx := context.Background()
	run := &pruneRunner{ids: map[string]string{}, gone: map[string]bool{}}
	spec := single("jellyfin", 8096)
	spec.NodeId = "node-a"
	spec.Image = "docker.io/jellyfin/jellyfin:10.10.7@sha256:live"
	run.ids[spec.GetImage()] = "id-live"

	mgr, store2 := managerOver(t, run)
	if _, err := store2.Put(ctx, services.KindService, spec.GetName(), spec, true); err != nil {
		t.Fatal(err)
	}

	res, err := mgr.PruneImages(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	// Tag and digest reference in one call, so the image goes rather than
	// being untagged down to one name and left behind.
	if !slicesContains(run.removed(), "docker.io/jellyfin/jellyfin:10.10.6 docker.io/jellyfin/jellyfin@sha256:old1") {
		t.Errorf("id-old1 was not removed by both its names: %v", run.removed())
	}
	for _, c := range run.cmds {
		if !strings.HasPrefix(c, "podman rmi ") {
			continue
		}
		for _, id := range []string{"id-old1", "id-old2", "id-gone"} {
			if strings.Contains(c, id) {
				t.Errorf("removed by ID, which podman refuses for a multi-named image: %q", c)
			}
		}
		if strings.Contains(c, "--force") || strings.Contains(c, "rmi -f") {
			t.Errorf("forced the removal, which would delete the containers holding the image: %q", c)
		}
	}
	// Reported under a name someone can recognise, not a hex ID.
	if !slicesContains(res.GetRemoved(), "docker.io/docmost/docmost:0.9.1") {
		t.Errorf("the removal was not reported by name: %v", res.GetRemoved())
	}
	// A dangling image has no name to remove it by and still goes by ID.
	if !slicesContains(run.removed(), "id-dangle") {
		t.Errorf("the dangling image was not removed: %v", run.removed())
	}
	// An image a Service names is never touched, by any of its names.
	for _, c := range run.removed() {
		if strings.Contains(c, "10.10.7") || strings.Contains(c, "sha256:live") {
			t.Errorf("prune removed an image a Service names: %q", c)
		}
	}
}

// Decision 320 is the default; dropping it is what gets the space back now.
func TestPruneCanDropRollbackTargets(t *testing.T) {
	ctx := context.Background()
	run := &pruneRunner{ids: map[string]string{}, gone: map[string]bool{}}
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
	got := run.removedIDs()
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
	run := &pruneRunner{ids: map[string]string{}, gone: map[string]bool{}}
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
	if !slicesContains(run.removedIDs(), "id-live") {
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
