package owner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fake(t *testing.T) (chowned map[string]int, acls map[string][]string) {
	t.Helper()
	chowned, acls = map[string]int{}, map[string][]string{}
	oc, oa := Chown, ACL
	Chown = func(p string, uid, gid int) error {
		if uid != gid {
			t.Fatalf("uid %d != gid %d", uid, gid)
		}
		chowned[p] = uid
		return nil
	}
	ACL = func(args ...string) error {
		acls[args[len(args)-1]] = args
		return nil
	}
	t.Cleanup(func() { Chown, ACL = oc, oa })
	return
}

func TestShareMakesAndWalks(t *testing.T) {
	chowned, acls := fake(t)
	root := t.TempDir()
	// A new folder two levels deep is made and owned at once.
	dir := filepath.Join(root, "Media", "Movies")
	os.MkdirAll(filepath.Join(root, "Media"), 0o755)
	if err := Share(context.Background(), dir, 61001, nil); err != nil {
		t.Fatal(err)
	}
	if chowned[dir] != 61001 || acls[dir] == nil {
		t.Fatalf("new folder not owned: %v %v", chowned, acls)
	}
	if fi, _ := os.Stat(dir); fi.Mode()&os.ModeSetgid == 0 || fi.Mode().Perm() != 0o775 {
		t.Fatalf("mode %v", fi.Mode())
	}
	// An existing tree is walked in the background, then marked.
	os.MkdirAll(filepath.Join(root, "Shows", "S1"), 0o755)
	os.WriteFile(filepath.Join(root, "Shows", "S1", "e1.mkv"), []byte("x"), 0o600)
	done := make(chan error, 1)
	if err := Share(context.Background(), filepath.Join(root, "Shows"), 61002, func(err error) { done <- err }); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("walk did not finish")
	}
	f := filepath.Join(root, "Shows", "S1", "e1.mkv")
	if chowned[f] != 61002 {
		t.Fatal("file in the tree not owned")
	}
	if fi, _ := os.Stat(f); fi.Mode().Perm() != 0o660 {
		t.Fatalf("file mode %v", fi.Mode().Perm())
	}
	// Marked: a second Share does not walk again.
	delete(chowned, f)
	Share(context.Background(), filepath.Join(root, "Shows"), 61002, func(error) { t.Fatal("walked again") })
	time.Sleep(50 * time.Millisecond)
	if _, ok := chowned[f]; ok {
		t.Fatal("walked again")
	}
}
