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

// A new tag is pulled and pinned to its digest, the old image kept for
// Rollback, the unit carries it, and the Service restarts only if it was
// running. A reference that could break the unit file is refused.
func TestSetImage(t *testing.T) {
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
	os.MkdirAll(filepath.Join(pools, "general"), 0o755)
	units := filepath.Join(dir, "units")
	m := services.New(services.Config{UnitsDir: units, PoolsDir: pools, DefaultSpace: func(context.Context) string { return "general" }, HostPortBase: 20000}, st, run)
	if _, err := m.Install(ctx, catalog.SpecFor(idx.Find("uptime-kuma"), "", nil, "")); err != nil {
		t.Fatal(err)
	}
	var before maitrev1.ServiceSpec
	st.Get(ctx, services.KindService, "uptime-kuma", &before)

	for _, bad := range []string{"", "Nginx", "docker.io/louislam/uptime-kuma:2\nExecStartPre=/bin/sh", "docker.io/louislam/uptime-kuma:2 --privileged", "uptime-kuma"} {
		if _, err := m.SetImage(ctx, "uptime-kuma", bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	run.cmds = nil
	if _, err := m.SetImage(ctx, "uptime-kuma", "docker.io/louislam/uptime-kuma:2"); err != nil {
		t.Fatal(err)
	}
	want := "docker.io/louislam/uptime-kuma:2@sha256:" + strings.Repeat("c", 64)
	var after maitrev1.ServiceSpec
	st.Get(ctx, services.KindService, "uptime-kuma", &after)
	if after.GetImage() != want || after.GetPreviousImage() != before.GetImage() {
		t.Fatalf("image %q previous %q", after.GetImage(), after.GetPreviousImage())
	}
	unit, _ := os.ReadFile(filepath.Join(units, "uptime-kuma.container"))
	if !strings.Contains(string(unit), "Image="+want) {
		t.Fatalf("unit:\n%s", unit)
	}
	cmds := strings.Join(run.cmds, "\n")
	if !strings.Contains(cmds, "podman pull -q docker.io/louislam/uptime-kuma:2") || !strings.Contains(cmds, "systemctl try-restart --no-block uptime-kuma.service") {
		t.Fatalf("commands: %v", run.cmds)
	}
	// The same image again changes nothing.
	if _, err := m.SetImage(ctx, "uptime-kuma", want); err != nil {
		t.Fatal(err)
	}
	st.Get(ctx, services.KindService, "uptime-kuma", &after)
	if after.GetPreviousImage() != before.GetImage() {
		t.Fatalf("previous became %q", after.GetPreviousImage())
	}
}
