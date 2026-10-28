package services_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Minecraft's EULA turns on: the unit carries it, the Record keeps it,
// auto update follows when given and stays when not, and the Service
// restarts only if it was running (try-restart). A value with a line
// break, a bad name or a setting the template needs is refused, and
// nothing changes.
func TestSetSettings(t *testing.T) {
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
	if _, err := m.Install(ctx, catalog.SpecFor(idx.Find("minecraft"), "", nil, "")); err != nil {
		t.Fatal(err)
	}
	var before maitrev1.ServiceSpec
	st.Get(ctx, services.KindService, "minecraft", &before)
	unitBefore, _ := os.ReadFile(filepath.Join(units, "minecraft.container"))
	run.cmds = nil

	full := func(change map[string]string, drop ...string) map[string]string {
		out := map[string]string{}
		for k, v := range before.GetSettings() {
			out[k] = v
		}
		for k, v := range change {
			out[k] = v
		}
		for _, k := range drop {
			delete(out, k)
		}
		return out
	}
	for _, bad := range []*maitrev1.SetSettingsRequest{
		{Name: "minecraft", Settings: full(map[string]string{"MOTD": "hi\n[Service]\nExecStartPre=/bin/sh"})},
		{Name: "minecraft", Settings: full(map[string]string{"BAD KEY": "x"})},
		{Name: "minecraft", Settings: full(nil, "EULA")},
		{Name: "nope", Settings: full(nil)},
	} {
		if _, err := m.SetSettings(ctx, bad); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	var same maitrev1.ServiceSpec
	st.Get(ctx, services.KindService, "minecraft", &same)
	unitSame, _ := os.ReadFile(filepath.Join(units, "minecraft.container"))
	if !proto.Equal(&same, &before) || string(unitSame) != string(unitBefore) {
		t.Fatal("a refused change was kept")
	}

	on := true
	if _, err := m.SetSettings(ctx, &maitrev1.SetSettingsRequest{Name: "minecraft", Settings: full(map[string]string{"EULA": "true"}), AutoUpdate: &on}); err != nil {
		t.Fatal(err)
	}
	unit, _ := os.ReadFile(filepath.Join(units, "minecraft.container"))
	if !strings.Contains(string(unit), "Environment=EULA=true") {
		t.Fatalf("unit:\n%s", unit)
	}
	var after maitrev1.ServiceSpec
	st.Get(ctx, services.KindService, "minecraft", &after)
	if after.GetSettings()["EULA"] != "true" || !after.GetAutoUpdate() {
		t.Fatalf("record: %v", after.GetSettings())
	}
	if !strings.Contains(strings.Join(run.cmds, "\n"), "systemctl try-restart --no-block minecraft.service") {
		t.Fatalf("commands: %v", run.cmds)
	}
	// Auto update left out stays as it is.
	if _, err := m.SetSettings(ctx, &maitrev1.SetSettingsRequest{Name: "minecraft", Settings: full(map[string]string{"EULA": "true", "MOTD": "Welcome"})}); err != nil {
		t.Fatal(err)
	}
	st.Get(ctx, services.KindService, "minecraft", &after)
	if !after.GetAutoUpdate() || after.GetSettings()["MOTD"] != "Welcome" {
		t.Fatalf("record: %v auto %v", after.GetSettings(), after.GetAutoUpdate())
	}
}
