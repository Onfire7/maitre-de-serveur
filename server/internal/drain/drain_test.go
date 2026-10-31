package drain

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

type fakeServices struct{ log []string }

func (f *fakeServices) InSpace(_ context.Context, space string) []string {
	if space == "general" {
		return []string{"jellyfin", "uptime"}
	}
	return nil
}
func (f *fakeServices) Stop(_ context.Context, n string) error {
	f.log = append(f.log, "stop "+n)
	return nil
}
func (f *fakeServices) Start(_ context.Context, n string) error {
	f.log = append(f.log, "start "+n)
	return nil
}

// goCopy stands in for rsync.
func goCopy(_ context.Context, src, dst string, dir bool, progress func(uint64, string)) error {
	if !dir {
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		progress(uint64(len(b)), filepath.Base(src))
		return os.WriteFile(dst, b, 0o644)
	}
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
}

func write(t *testing.T, p, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) (*Manager, *store.Store, *fakeServices, map[string]uint64) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "a")
	t.Cleanup(func() { st.Close() })
	for _, b := range []*maitrev1.Space{
		{SpaceId: "general", Name: "General", Placement: storage.PlacementMostFree, DefaultForNew: true},
		{SpaceId: "media", Name: "Media", Placement: storage.PlacementMostFree, RequireTags: []string{"hdd"}},
	} {
		st.Put(ctx, storage.KindSpace, b.SpaceId, b, true)
	}
	for _, d := range []*maitrev1.DriveRecord{
		{DriveId: "d1", NodeId: "a", Name: "Aspen", AutoTags: []string{"hdd"}},
		{DriveId: "d2", NodeId: "a", Name: "Atlas", AutoTags: []string{"hdd"}},
		{DriveId: "d3", NodeId: "a", Name: "Apollo", AutoTags: []string{"ssd"}},
		{DriveId: "b1", NodeId: "b", Name: "Birch", AutoTags: []string{"hdd"}},
	} {
		st.Put(ctx, storage.KindDrive, d.DriveId, d, false)
	}
	m := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), "a")
	m.Mounts = t.TempDir()
	m.Remote = t.TempDir()
	m.StateFile = filepath.Join(t.TempDir(), "drains.json")
	m.Copy = goCopy
	free := map[string]uint64{"d1": 10 << 30, "d2": 100 << 30, "d3": 50 << 30}
	m.Free = func(dir string) uint64 { return free[filepath.Base(dir)] }
	m.Mounted = func(dir string) bool { _, ok := free[filepath.Base(dir)]; return ok }
	svcs := &fakeServices{}
	m.Services = svcs
	for _, d := range []string{"d1", "d2", "d3"} {
		os.MkdirAll(filepath.Join(m.Mounts, d), 0o755)
	}
	d1 := filepath.Join(m.Mounts, "d1")
	write(t, filepath.Join(d1, "general", ".maitre-space"), "")
	write(t, filepath.Join(d1, "general", "Docs", "a.txt"), "aaaa")
	write(t, filepath.Join(d1, "general", ".Services", "jellyfin", "config", "x.db"), "db")
	write(t, filepath.Join(d1, "media", ".maitre-space"), "")
	write(t, filepath.Join(d1, "media", "film.mkv"), "mmmmmmmm")
	write(t, filepath.Join(d1, "Downloads", "iso.img"), "ii")
	write(t, filepath.Join(d1, "old-photos", ".maitre-space"), "") // an Orphan Folder
	write(t, filepath.Join(d1, "old-photos", "p.jpg"), "jj")
	return m, st, svcs, free
}

func wait(t *testing.T, m *Manager, id string) *maitrev1.DrainJob {
	t.Helper()
	for i := 0; i < 200; i++ {
		for _, j := range m.Jobs() {
			if j.GetJobId() == id && j.GetState() != "queued" && j.GetState() != "running" {
				return j
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return nil
}

func TestPlanAndDrain(t *testing.T) {
	ctx := context.Background()
	m, st, svcs, _ := setup(t)
	changes := 0
	m.OnChange = func() { changes++ }
	var then *maitrev1.DrainJob
	m.Then = func(_ context.Context, j *maitrev1.DrainJob) error { then = j; return nil }

	// Loose files with no Space to go to block it.
	plan, err := m.Plan(ctx, "d1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blockers) != 1 || !strings.Contains(plan.Blockers[0], "Pick a Space") || !strings.Contains(plan.Blockers[0], "Downloads, old-photos") {
		t.Fatalf("blockers %v", plan.Blockers)
	}
	plan, err = m.Plan(ctx, "d1", "general", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blockers) != 0 || len(plan.Warnings) != 0 || len(plan.Moves) != 3 {
		t.Fatalf("plan %v", plan)
	}
	got := map[string]string{}
	for _, mv := range plan.Moves {
		got[mv.GetSpaceId()] = strings.Join(mv.GetToDriveIds(), ",") + ">" + mv.GetToSpace()
	}
	if got["general"] != "d2,d3>general" || got["media"] != "d2>media" || got[""] != "d2,d3>general" {
		t.Fatalf("moves %v", got)
	}
	if _, err := m.Start(ctx, "d1", "general", "Atlas", PurposeParity); err == nil || !strings.Contains(err.Error(), "repeat exactly: Aspen") {
		t.Fatalf("wrong name accepted: %v", err)
	}
	job, err := m.Start(ctx, "d1", "general", "Aspen", PurposeParity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(ctx, "d1", "general", "Aspen", PurposeParity); err == nil {
		t.Fatal("started twice")
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	go m.Run(runCtx)
	j := wait(t, m, job.GetJobId())
	if j.GetState() != "done" {
		t.Fatalf("job: %s %s", j.GetState(), j.GetMessage())
	}
	d1 := filepath.Join(m.Mounts, "d1")
	for _, gone := range []string{"general", "media", "Downloads", "old-photos"} {
		if _, err := os.Stat(filepath.Join(d1, gone)); err == nil {
			t.Errorf("%s still on the Drive", gone)
		}
	}
	// Media's only other Drive is d2; General's files went to the roomiest
	// Drive, d2; loose files landed under From Aspen/.
	for _, want := range []string{"d2/media/film.mkv", "d2/general/Docs/a.txt", "d2/general/.Services/jellyfin/config/x.db", "d2/general/From Aspen/Downloads/iso.img", "d2/general/From Aspen/old-photos/p.jpg"} {
		if _, err := os.Stat(filepath.Join(m.Mounts, want)); err != nil {
			t.Errorf("%s missing", want)
		}
	}
	if strings.Join(svcs.log, ",") != "stop jellyfin,start jellyfin" {
		t.Errorf("services: %v (uptime has no folder on the Drive, so it is left alone)", svcs.log)
	}
	if then == nil || then.GetPurpose() != PurposeParity {
		t.Errorf("Then not run: %v", then)
	}
	var rec maitrev1.DriveRecord
	st.Get(ctx, storage.KindDrive, "d1", &rec)
	if rec.GetDraining() || changes != 2 {
		t.Errorf("draining %v after %d changes", rec.GetDraining(), changes)
	}
	if j.GetBytesDone() != j.GetBytesTotal() || j.GetBytesTotal() != 18 {
		t.Errorf("bytes %d of %d", j.GetBytesDone(), j.GetBytesTotal())
	}
	if len(j.GetNotes()) != 2 {
		t.Errorf("notes %v", j.GetNotes())
	}
	// The jobs file brings it back finished.
	m2 := New(st, m.log, "a")
	m2.StateFile = m.StateFile
	m2.load()
	if jobs := m2.Jobs(); len(jobs) != 1 || jobs[0].GetState() != "done" || jobs[0].GetJobId() != job.GetJobId() {
		t.Fatalf("reloaded %v", jobs)
	}
}

func TestBlockersAndSpanning(t *testing.T) {
	ctx := context.Background()
	m, st, _, free := setup(t)
	// Media's other Drive is full: a non-Spanning Space blocks (246).
	free["d2"] = 1 << 20
	plan, _ := m.Plan(ctx, "d1", "general", nil)
	if len(plan.Blockers) != 1 || !strings.Contains(plan.Blockers[0], "Media's other Drives have") {
		t.Fatalf("blockers %v", plan.Blockers)
	}
	if _, err := m.Start(ctx, "d1", "general", "Aspen", ""); err == nil {
		t.Fatal("started with a blocker")
	}
	// A Spanning Space goes over the network when a peer's Pool is here (238).
	var media maitrev1.Space
	st.Get(ctx, storage.KindSpace, "media", &media)
	media.SpanNodes = true
	st.Put(ctx, storage.KindSpace, "media", &media, true)
	plan, _ = m.Plan(ctx, "d1", "general", nil)
	if len(plan.Blockers) != 1 || !strings.Contains(plan.Blockers[0], "no other Node's Pool") {
		t.Fatalf("blockers %v", plan.Blockers)
	}
	os.MkdirAll(filepath.Join(m.Remote, "b", "media"), 0o755)
	free["media"] = 1 << 40 // the remote mount, by base name in this fake
	was := m.Mounted
	m.Mounted = func(dir string) bool { return was(dir) || strings.HasSuffix(dir, filepath.Join("b", "media")) }
	plan, _ = m.Plan(ctx, "d1", "general", nil)
	if len(plan.Blockers) != 0 || len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "over the network") {
		t.Fatalf("plan %v", plan)
	}
	for _, mv := range plan.Moves {
		if mv.GetSpaceId() == "media" && mv.GetToNodeId() != "b" {
			t.Fatalf("media move %v", mv)
		}
	}
	// Media alone on the Drive, not spanning: blocked with the plain words.
	media.SpanNodes = false
	media.RequireTags = []string{"hdd", "big"}
	st.Put(ctx, storage.KindSpace, "media", &media, true)
	var d1 maitrev1.DriveRecord
	st.Get(ctx, storage.KindDrive, "d1", &d1)
	d1.Tags = []string{"big"}
	st.Put(ctx, storage.KindDrive, "d1", &d1, false)
	plan, _ = m.Plan(ctx, "d1", "general", nil)
	if len(plan.Blockers) != 1 || !strings.Contains(plan.Blockers[0], "only Drive of Media") {
		t.Fatalf("blockers %v", plan.Blockers)
	}
}

func TestSweepAfterRuleChange(t *testing.T) {
	ctx := context.Background()
	m, st, svcs, _ := setup(t)
	// d1 loses the tag Media requires; its Media files must move to d2.
	var d1 maitrev1.DriveRecord
	st.Get(ctx, storage.KindDrive, "d1", &d1)
	d1.AutoTags = []string{"ssd"}
	st.Put(ctx, storage.KindDrive, "d1", &d1, false)
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	go m.Run(runCtx)
	m.Sweep(ctx)
	jobs := m.Jobs()
	if len(jobs) != 1 || jobs[0].GetPurpose() != PurposeRule || strings.Join(jobs[0].GetSpaceIds(), ",") != "media" {
		t.Fatalf("jobs %v", jobs)
	}
	j := wait(t, m, jobs[0].GetJobId())
	if j.GetState() != "done" {
		t.Fatalf("job: %s %s", j.GetState(), j.GetMessage())
	}
	if _, err := os.Stat(filepath.Join(m.Mounts, "d2", "media", "film.mkv")); err != nil {
		t.Error("film did not move")
	}
	if _, err := os.Stat(filepath.Join(m.Mounts, "d1", "media")); err == nil {
		t.Error("media folder still on d1")
	}
	// General and the loose files were not touched, and no Service stopped.
	if _, err := os.Stat(filepath.Join(m.Mounts, "d1", "general", "Docs", "a.txt")); err != nil {
		t.Error("general moved too")
	}
	if len(svcs.log) != 0 {
		t.Errorf("services touched: %v", svcs.log)
	}
	// Nothing left to sweep.
	m.Sweep(ctx)
	if len(m.Jobs()) != 1 {
		t.Fatal("swept again")
	}
}

func TestCancel(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := setup(t)
	started := make(chan struct{})
	m.Copy = func(ctx context.Context, src, dst string, dir bool, p func(uint64, string)) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}
	job, err := m.Start(ctx, "d1", "general", "Aspen", "")
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	go m.Run(runCtx)
	<-started
	if err := m.Cancel(job.GetJobId()); err != nil {
		t.Fatal(err)
	}
	j := wait(t, m, job.GetJobId())
	if j.GetState() != "cancelled" {
		t.Fatalf("state %s", j.GetState())
	}
	if _, err := os.Stat(filepath.Join(m.Mounts, "d1", "general", "Docs", "a.txt")); err != nil {
		t.Error("source removed on cancel")
	}
}
