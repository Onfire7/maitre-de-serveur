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

// Decision 287: a Service is installed with any of the four kinds of
// access, checked as a change of Access is: a direct one needs a usable
// host, kept lower-case and not already another Service's; a non-private
// one has the remote side reconciled at once.
func TestInstallWithAccess(t *testing.T) {
	ctx := context.Background()
	idx, err := catalog.LoadDir("../../../catalog/entries")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	defer st.Close()
	dir := t.TempDir()
	pools := filepath.Join(dir, "pools")
	os.MkdirAll(filepath.Join(pools, "general"), 0o755)
	m := services.New(services.Config{UnitsDir: filepath.Join(dir, "units"), PoolsDir: pools, DefaultSpace: func(context.Context) string { return "general" }, HostPortBase: 20000}, st, &fakeRunner{state: "active"})
	reconciled := 0
	m.OnExposure = func() { reconciled++ }

	spec := catalog.SpecFor(idx.Find("uptime-kuma"), "", nil, "")
	spec.Exposure = "direct"
	spec.PublicHost = " Status.Example.COM "
	if _, err := m.Install(ctx, spec); err != nil {
		t.Fatal(err)
	}
	var got maitrev1.ServiceSpec
	st.Get(ctx, services.KindService, "uptime-kuma", &got)
	if got.GetExposure() != "direct" || got.GetPublicHost() != "status.example.com" {
		t.Fatalf("kept %q %q", got.GetExposure(), got.GetPublicHost())
	}
	if reconciled != 1 {
		t.Fatalf("reconciled %d times", reconciled)
	}

	for _, bad := range []struct{ exposure, host, want string }{
		{"direct", "", "full host name"},
		{"direct", "kuma.local", "full host name"},
		{"direct", "status.example.com", "already answers"},
		{"elsewhere", "", "must be private"},
	} {
		sp := catalog.SpecFor(idx.Find("memos"), "", nil, "")
		sp.Exposure, sp.PublicHost = bad.exposure, bad.host
		_, err := m.Install(ctx, sp)
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Fatalf("%v: %v", bad, err)
		}
	}
	// A private install touches nothing remote; a public one by itself does.
	sp := catalog.SpecFor(idx.Find("memos"), "", nil, "")
	sp.Exposure, sp.PublicHost = "public", "ignored.example.com"
	if _, err := m.Install(ctx, sp); err != nil {
		t.Fatal(err)
	}
	st.Get(ctx, services.KindService, "memos", &got)
	if got.GetPublicHost() != "" || reconciled != 2 {
		t.Fatalf("public: host %q, reconciled %d", got.GetPublicHost(), reconciled)
	}
}
