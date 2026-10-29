package catalogsync_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/Onfire7/maitre-de-serveur/server/internal/catalogsync"
)

func commit(t *testing.T, wt *git.Worktree, dir, file, body string) string {
	t.Helper()
	os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0o755)
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	wt.Add(file)
	h, err := wt.Commit("change "+file, &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@example.invalid", When: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	return h.String()
}

// The clone follows its branch: new commits arrive, and anything changed in
// the clone itself is put back.
func TestSyncFollowsTheBranch(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	repo, err := git.PlainInit(src, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, _ := repo.Worktree()
	first := commit(t, wt, src, "entries/a/manifest.yaml", "id: a\n")
	clone := filepath.Join(t.TempDir(), "c")
	got, err := catalogsync.Sync(ctx, src, "", clone)
	if err != nil || got != first {
		t.Fatalf("clone: %s %v", got, err)
	}
	second := commit(t, wt, src, "entries/b/manifest.yaml", "id: b\n")
	os.WriteFile(filepath.Join(clone, "entries/a/manifest.yaml"), []byte("edited here\n"), 0o644)
	os.WriteFile(filepath.Join(clone, "stray"), []byte("x"), 0o644)
	got, err = catalogsync.Sync(ctx, src, "", clone)
	if err != nil || got != second {
		t.Fatalf("sync: %s %v", got, err)
	}
	if b, _ := os.ReadFile(filepath.Join(clone, "entries/a/manifest.yaml")); string(b) != "id: a\n" {
		t.Fatalf("local edit kept: %q", b)
	}
	if _, err := os.Stat(filepath.Join(clone, "entries/b/manifest.yaml")); err != nil {
		t.Fatal("new entry not fetched")
	}
	if _, err := os.Stat(filepath.Join(clone, "stray")); !os.IsNotExist(err) {
		t.Fatal("untracked file kept")
	}
}
