package files

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// A Node with one Drive serving two Spaces; the Pools are plain directories
// here (no mergerfs), so locations are empty for Space scopes.
func setup(t *testing.T) (*Manager, context.Context, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "fleet.db"), "node-a")
	t.Cleanup(func() { st.Close() })
	for _, b := range []*maitrev1.Space{{SpaceId: "general", Name: "General", Placement: "local_first", DefaultForNew: true}, {SpaceId: "media", Name: "Media", Placement: "most_free"}} {
		st.Put(ctx, storage.KindSpace, b.SpaceId, b, true)
	}
	st.Put(ctx, storage.KindDrive, "d1", &maitrev1.DriveRecord{DriveId: "d1", NodeId: "node-a", Name: "Apollo", Origin: "adopted"}, false)
	m := New(st, slog.Default(), "node-a")
	m.Mounts, m.Pools, m.Remote = filepath.Join(dir, "drives"), filepath.Join(dir, "pools"), filepath.Join(dir, "remote")
	for _, p := range []string{"drives/d1/general", "drives/d1/media", "pools/general", "pools/media"} {
		os.MkdirAll(filepath.Join(dir, p), 0o755)
	}
	mountedForTest = map[string]bool{filepath.Join(dir, "drives/d1"): true, filepath.Join(dir, "pools/general"): true, filepath.Join(dir, "pools/media"): true}
	t.Cleanup(func() { mountedForTest = nil })
	// Pre-adoption files on the Drive root.
	os.MkdirAll(filepath.Join(dir, "drives/d1/Movies/Alien"), 0o755)
	os.WriteFile(filepath.Join(dir, "drives/d1/Movies/Alien/alien.mkv"), make([]byte, 5000), 0o644)
	os.WriteFile(filepath.Join(dir, "drives/d1/notes.txt"), []byte("hi"), 0o644)
	return m, ctx, dir
}

func TestListAndUsage(t *testing.T) {
	m, ctx, _ := setup(t)
	entries, err := m.ListDir(ctx, &maitrev1.Scope{DriveId: "d1"}, "/")
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.GetName())
	}
	if len(entries) != 4 || entries[0].GetName() != "general" || entries[3].GetName() != "notes.txt" || entries[3].GetSizeBytes() != 2 || entries[3].GetLocations()[0].GetDriveId() != "d1" {
		t.Fatalf("entries %v", names)
	}
	if _, err := m.ListDir(ctx, &maitrev1.Scope{DriveId: "d1"}, "../etc"); err == nil {
		t.Fatal("escaped the Drive")
	}
	u, _ := m.Usage(ctx, &maitrev1.Scope{DriveId: "d1"}, "Movies")
	if !u.GetInProgress() {
		t.Fatal("first usage call should be in progress")
	}
	deadline := time.Now().Add(3 * time.Second)
	for u.GetInProgress() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		u, _ = m.Usage(ctx, &maitrev1.Scope{DriveId: "d1"}, "Movies")
	}
	if u.GetBytes() != 5000 || u.GetFiles() != 1 {
		t.Fatalf("usage %v", u)
	}
}

func TestMoveIntoSpace(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("no rsync")
	}
	m, ctx, dir := setup(t)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go m.Run(runCtx)
	// Unsorted Movies on the Drive go into the Media Space, on that Drive.
	j, err := m.Move(ctx, &maitrev1.MoveRequest{From: &maitrev1.Scope{DriveId: "d1"}, Path: "Movies", ToSpace: "media", ToDriveId: "d1"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		jobs := m.Jobs()
		if jobs[0].GetState() == "done" || jobs[0].GetState() == "failed" {
			j = jobs[0]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if j.GetState() != "done" {
		t.Fatalf("job %v", j)
	}
	if _, err := os.Stat(filepath.Join(dir, "drives/d1/media/Movies/Alien/alien.mkv")); err != nil {
		t.Fatal("file not at destination")
	}
	if _, err := os.Stat(filepath.Join(dir, "drives/d1/Movies")); !os.IsNotExist(err) {
		t.Fatal("source still there")
	}
	// Refusals.
	if _, err := m.Move(ctx, &maitrev1.MoveRequest{From: &maitrev1.Scope{DriveId: "d1"}, Path: "/", ToSpace: "media"}); err == nil {
		t.Fatal("moved the top")
	}
	if _, err := m.Move(ctx, &maitrev1.MoveRequest{From: &maitrev1.Scope{DriveId: "d1"}, Path: "notes.txt", ToSpace: "nope"}); err == nil {
		t.Fatal("unknown space accepted")
	}
	if _, err := m.Move(ctx, &maitrev1.MoveRequest{From: &maitrev1.Scope{DriveId: "d1"}, Path: "notes.txt", ToSpace: "media", ToNodeId: "node-b"}); err == nil {
		t.Fatal("drive-scope move to another Node accepted")
	}
}

func TestMakeDir(t *testing.T) {
	m, ctx, dir := setup(t)
	media := &maitrev1.Scope{SpaceId: "media"}
	if err := m.MakeDir(ctx, media, "/Movies/"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "pools/media/Movies")); err != nil || !fi.IsDir() {
		t.Fatalf("no folder: %v", err)
	}
	if err := m.MakeDir(ctx, media, "Movies"); !errors.Is(err, ErrExists) {
		t.Fatalf("taken name: %v", err)
	}
	if err := m.MakeDir(ctx, media, "Shows/2026"); err == nil {
		t.Fatal("made a folder in a missing parent")
	}
	for _, bad := range []string{"", "/", "../escape", ".hidden", "Movies/.cache"} {
		if err := m.MakeDir(ctx, media, bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if err := m.MakeDir(ctx, &maitrev1.Scope{DriveId: "d1"}, "Movies/Aliens"); err != nil {
		t.Fatal(err)
	}
}

// Between folders of one Space on one Node the move is a rename: it needs
// no rsync and leaves nothing behind.
func TestMoveWithinSpace(t *testing.T) {
	m, ctx, dir := setup(t)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go m.Run(runCtx)
	os.MkdirAll(filepath.Join(dir, "pools/media/Inbox/Dune"), 0o755)
	os.WriteFile(filepath.Join(dir, "pools/media/Inbox/Dune/dune.mkv"), make([]byte, 700), 0o644)
	os.MkdirAll(filepath.Join(dir, "pools/media/Movies"), 0o755)
	if _, err := m.Move(ctx, &maitrev1.MoveRequest{From: &maitrev1.Scope{SpaceId: "media"}, Path: "Inbox/Dune", ToSpace: "media", ToPath: "Movies/Dune"}); err != nil {
		t.Fatal(err)
	}
	var j *maitrev1.Job
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if j = m.Jobs()[0]; j.GetState() == "done" || j.GetState() == "failed" {
			break
		}
	}
	if j.GetState() != "done" || j.GetBytesDone() != 700 {
		t.Fatalf("job %v", j)
	}
	if _, err := os.Stat(filepath.Join(dir, "pools/media/Movies/Dune/dune.mkv")); err != nil {
		t.Fatal("file not at destination")
	}
	if _, err := os.Stat(filepath.Join(dir, "pools/media/Inbox/Dune")); !os.IsNotExist(err) {
		t.Fatal("source still there")
	}
}
