package storage

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/pool"
)

// After a reboot the export list is unchanged; the NFS server must still be
// started, or peers cannot mount this Node's Pools (2026-10-27: a spanning
// Space showed only the anchor's files).
func TestWriteExportsStartsTheServerWhenUnchanged(t *testing.T) {
	ExportsFile = filepath.Join(t.TempDir(), "exports.d", "maitre.exports")
	mountedDirs = map[string]bool{filepath.Join(pool.SharedDir, "media"): true}
	t.Cleanup(func() { mountedDirs = nil })
	ctx := context.Background()
	hosts := []string{"192.168.4.74"}

	run := &fakeRunner{}
	if err := WriteExports(ctx, run, []string{"media"}, hosts); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(run.cmds, []string{"systemctl start --no-block nfs-server.service", "exportfs -ra"}) {
		t.Fatalf("first write: %q", run.cmds)
	}

	run = &fakeRunner{} // the Node rebooted; the list is the same
	if err := WriteExports(ctx, run, []string{"media"}, hosts); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(run.cmds, []string{"systemctl start --no-block nfs-server.service"}) {
		t.Fatalf("unchanged: %q", run.cmds)
	}

	run = &fakeRunner{} // nothing to export, and nothing was before
	mountedDirs = map[string]bool{}
	_ = WriteExports(ctx, run, []string{"media"}, hosts)
	run = &fakeRunner{}
	if err := WriteExports(ctx, run, []string{"media"}, hosts); err != nil {
		t.Fatal(err)
	}
	if len(run.cmds) != 0 {
		t.Fatalf("nothing exported: %q", run.cmds)
	}
}
