package services_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// fakeMover records the move and lets the test finish it.
type fakeMover struct {
	req  *maitrev1.MoveRequest
	done func(error)
}

func (f *fakeMover) MoveThen(_ context.Context, req *maitrev1.MoveRequest, done func(error)) (*maitrev1.Job, error) {
	f.req, f.done = req, done
	return &maitrev1.Job{JobId: "job-1", State: "queued", Request: req}, nil
}

func TestMoveVolumeStopsMovesRewritesRestarts(t *testing.T) {
	ctx := context.Background()
	idx, err := catalog.LoadDir("../../../catalog/entries")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	defer st.Close()
	dir := t.TempDir()
	run := &fakeRunner{state: "active"}
	pools := filepath.Join(dir, "pools")
	for _, sp := range []string{"general", "media"} {
		if err := os.MkdirAll(filepath.Join(pools, sp), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := services.New(services.Config{UnitsDir: filepath.Join(dir, "units"), PoolsDir: pools, DefaultSpace: func(context.Context) string { return "general" }, HostPortBase: 20000}, st, run)
	mover := &fakeMover{}
	m.Mover = mover
	if _, err := m.Install(ctx, catalog.SpecFor(idx.Find("jellyfin"), "", nil, "")); err != nil {
		t.Fatal(err)
	}
	run.cmds = nil

	// Refusals first: unknown volume, same Space, a Space with no Pool here.
	for _, c := range []struct{ vol, to string }{{"nope", "media"}, {"media", "general"}, {"media", "photos"}} {
		if _, err := m.MoveVolume(ctx, "jellyfin", c.vol, c.to); err == nil {
			t.Fatalf("expected a refusal for %+v", c)
		}
	}
	if len(run.cmds) > 0 && strings.Contains(strings.Join(run.cmds, "\n"), "systemctl stop") {
		t.Fatal("a refused move must not stop the Service")
	}

	id, err := m.MoveVolume(ctx, "jellyfin", "media", "media")
	if err != nil {
		t.Fatal(err)
	}
	if id != "job-1" || mover.req.GetFrom().GetSpaceId() != "general" || mover.req.GetToSpace() != "media" || mover.req.GetPath() != ".Services/jellyfin/media" || mover.req.GetToPath() != ".Services/jellyfin/media" {
		t.Fatalf("job %s, request %+v", id, mover.req)
	}
	joined := strings.Join(run.cmds, "\n")
	if !strings.Contains(joined, "systemctl stop jellyfin.service") || strings.Contains(joined, "systemctl start") {
		t.Fatalf("the Service stops before the move and is not started yet: %s", joined)
	}

	// The move finishes: the spec follows and the Service starts again.
	run.cmds = nil
	mover.done(nil)
	var spec maitrev1.ServiceSpec
	if _, err := st.Get(ctx, services.KindService, "jellyfin", &spec); err != nil {
		t.Fatal(err)
	}
	if spec.GetVolumeSpaces()["media"] != "media" || spec.GetVolumeSpaces()["config"] != "general" {
		t.Fatalf("volume_spaces after the move: %v", spec.GetVolumeSpaces())
	}
	unit, _ := os.ReadFile(filepath.Join(dir, "units", "jellyfin.container"))
	if !strings.Contains(string(unit), filepath.Join(pools, "media", ".Services", "jellyfin", "media")) {
		t.Fatalf("unit does not mount the new place:\n%s", unit)
	}
	if !strings.Contains(strings.Join(run.cmds, "\n"), "systemctl start --no-block jellyfin.service") {
		t.Fatalf("the Service restarts after the move: %v", run.cmds)
	}
}

func TestMoveVolumeFailureLeavesSpecAndRestarts(t *testing.T) {
	ctx := context.Background()
	idx, _ := catalog.LoadDir("../../../catalog/entries")
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	defer st.Close()
	dir := t.TempDir()
	run := &fakeRunner{state: "active"}
	pools := filepath.Join(dir, "pools")
	for _, sp := range []string{"general", "media"} {
		_ = os.MkdirAll(filepath.Join(pools, sp), 0o755)
	}
	m := services.New(services.Config{UnitsDir: filepath.Join(dir, "units"), PoolsDir: pools, DefaultSpace: func(context.Context) string { return "general" }, HostPortBase: 20000}, st, run)
	mover := &fakeMover{}
	m.Mover = mover
	if _, err := m.Install(ctx, catalog.SpecFor(idx.Find("jellyfin"), "", nil, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.MoveVolume(ctx, "jellyfin", "media", "media"); err != nil {
		t.Fatal(err)
	}
	run.cmds = nil
	mover.done(context.Canceled)
	var spec maitrev1.ServiceSpec
	_, _ = st.Get(ctx, services.KindService, "jellyfin", &spec)
	if spec.GetVolumeSpaces()["media"] != "general" {
		t.Fatalf("a failed move leaves the volume where it was: %v", spec.GetVolumeSpaces())
	}
	if !strings.Contains(strings.Join(run.cmds, "\n"), "systemctl start --no-block jellyfin.service") {
		t.Fatalf("the Service restarts on the old place: %v", run.cmds)
	}
}

// Decision 205: a Service installed before keeps its volume at
// <Space>/<Service>/<volume> until it moves; MoveVolume takes it from there
// to <Space>/.Services/<Service>/<volume> in the new Space.
func TestMoveVolumeFromTheOldPlace(t *testing.T) {
	ctx := context.Background()
	idx, err := catalog.LoadDir("../../../catalog/entries")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	defer st.Close()
	dir := t.TempDir()
	pools := filepath.Join(dir, "pools")
	for _, sp := range []string{"general", "media"} {
		_ = os.MkdirAll(filepath.Join(pools, sp), 0o755)
	}
	m := services.New(services.Config{UnitsDir: filepath.Join(dir, "units"), PoolsDir: pools, DefaultSpace: func(context.Context) string { return "general" }, HostPortBase: 20000}, st, &fakeRunner{state: "active"})
	mover := &fakeMover{}
	m.Mover = mover
	if _, err := m.Install(ctx, catalog.SpecFor(idx.Find("jellyfin"), "", nil, "")); err != nil {
		t.Fatal(err)
	}
	// As a Node before 205 left it: the data at the old place only.
	_ = os.RemoveAll(filepath.Join(pools, "general", ".Services"))
	_ = os.MkdirAll(filepath.Join(pools, "general", "jellyfin", "config"), 0o755)
	if _, err := m.MoveVolume(ctx, "jellyfin", "config", "media"); err != nil {
		t.Fatal(err)
	}
	if mover.req.GetPath() != "jellyfin/config" || mover.req.GetToPath() != ".Services/jellyfin/config" {
		t.Fatalf("request %+v", mover.req)
	}
}

// Decision 208: in a Space that spans Nodes, a Service's own folders live
// on this Node's own Drives of it (its local-only Pool), and a move to such
// a Space is refused when this Node has no Drive in it.
func TestOwnFoldersInASpanningSpace(t *testing.T) {
	ctx := context.Background()
	idx, err := catalog.LoadDir("../../../catalog/entries")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	defer st.Close()
	dir := t.TempDir()
	pools, shared := filepath.Join(dir, "pools"), filepath.Join(dir, "shared")
	for _, sp := range []string{"general", "media", "photos"} {
		_ = os.MkdirAll(filepath.Join(pools, sp), 0o755)
	}
	_ = os.MkdirAll(filepath.Join(shared, "media"), 0o755)
	local := func(_ context.Context, space string) (string, bool) {
		switch space {
		case "media":
			return filepath.Join(shared, "media"), true
		case "photos":
			return "", true // spans, but no Drive of it here
		}
		return "", false
	}
	run := &fakeRunner{state: "active"}
	m := services.New(services.Config{UnitsDir: filepath.Join(dir, "units"), PoolsDir: pools, DefaultSpace: func(context.Context) string { return "general" }, HostPortBase: 20000, LocalPool: local}, st, run)
	mover := &fakeMover{}
	m.Mover = mover
	spec := catalog.SpecFor(idx.Find("jellyfin"), "", nil, "")
	spec.VolumeSpaces = map[string]string{"config": "general", "cache": "media"}
	if _, err := m.Install(ctx, spec); err != nil {
		t.Fatal(err)
	}
	unit, _ := os.ReadFile(filepath.Join(dir, "units", "jellyfin.container"))
	if !strings.Contains(string(unit), "Volume="+filepath.Join(shared, "media", ".Services", "jellyfin", "cache")+":/cache") {
		t.Fatalf("unit:\n%s", unit)
	}
	if !strings.Contains(string(unit), "Volume="+filepath.Join(pools, "general", ".Services", "jellyfin", "config")+":/config") {
		t.Fatalf("a Space that does not span keeps its Pool:\n%s", unit)
	}
	run.cmds = nil
	if _, err := m.MoveVolume(ctx, "jellyfin", "config", "photos"); err == nil || !strings.Contains(err.Error(), "no Drive in Space photos") {
		t.Fatalf("moved into a spanning Space with no Drive here: %v", err)
	}
	if strings.Contains(strings.Join(run.cmds, "\n"), "systemctl stop") {
		t.Fatal("a refused move must not stop the Service")
	}
}
