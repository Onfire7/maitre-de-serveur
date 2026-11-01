package services_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
	"github.com/Onfire7/maitre-de-serveur/server/internal/quadlet"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/timezone"
)

// fakeRunner records commands and answers systemctl show with a scripted state.
type fakeRunner struct {
	cmds  []string
	state string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.cmds = append(f.cmds, name+" "+strings.Join(args, " "))
	if name == "systemctl" && len(args) > 1 && args[0] == "show" {
		return "ActiveState=" + f.state + "\nSubState=running\nNRestarts=2\nResult=success\nExecMainStartTimestamp=Fri 2026-10-24 10:00:00 UTC\n", nil
	}
	if name == "podman" && len(args) > 1 && args[0] == "image" && args[1] == "inspect" {
		return "sha256:" + strings.Repeat("c", 64) + "\n", nil
	}
	if name == "journalctl" {
		return "l1\nl2\n", nil
	}
	if name == "nmcli" && args[0] == "-t" && args[1] == "-f" {
		return "lo:loopback:lo\nWired connection 1:802-3-ethernet:enp3s0\n", nil
	}
	if name == "nmcli" && args[0] == "-t" && args[1] == "-g" {
		return "192.168.4.80/22\n192.168.4.1\n192.168.4.1 | 1.1.1.1\n", nil
	}
	return "", nil
}

func TestInstallRenderStatusRemove(t *testing.T) {
	ctx := context.Background()
	idx, err := catalog.LoadDir("../../../catalog/entries")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	defer st.Close()
	dir := t.TempDir()
	run := &fakeRunner{state: "active"}
	// A fake Jellyfin answering /health on whatever port the Manager picks.
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(200)
		} else {
			w.WriteHeader(404)
		}
	}))
	defer health.Close()
	pools := filepath.Join(dir, "pools")
	if err := os.MkdirAll(filepath.Join(pools, "general"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := services.New(services.Config{UnitsDir: filepath.Join(dir, "units"), PoolsDir: pools, DefaultSpace: func(context.Context) string { return "general" }, HostPortBase: 20000},
		st, run)

	s, err := m.Install(ctx, catalog.SpecFor(idx.Find("jellyfin"), "", map[string]string{"TZ": "Europe/Paris"}, ""))
	if err != nil {
		t.Fatal(err)
	}
	if s.GetSpec().GetName() != "jellyfin" || s.GetSpec().GetWebHostPort() < 20000 || s.GetState() != maitrev1.ServiceStatus_STATE_RUNNING {
		t.Fatalf("status %+v", s)
	}
	if s.GetUrl() != "http://jellyfin.local/" || s.GetRestarts() != 2 {
		t.Fatalf("url/restarts %q %d", s.GetUrl(), s.GetRestarts())
	}
	unit, err := os.ReadFile(filepath.Join(dir, "units", "jellyfin.container"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Image=docker.io/jellyfin/jellyfin:10.10.7@sha256:",
		"Environment=TZ=Europe/Paris",
		"Volume=" + filepath.Join(pools, "general", ".Services", "jellyfin", "media") + ":/media:Z",
		"Network=maitre.network",
		"PublishPort=127.0.0.1:" + itoa(int(s.GetSpec().GetWebHostPort())) + ":8096",
		"[Install]",
	} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
	if _, err := os.Stat(filepath.Join(pools, "general", ".Services", "jellyfin", "config")); err != nil {
		t.Fatal("volume dir not created")
	}
	if !strings.Contains(strings.Join(run.cmds, "\n"), "systemctl daemon-reload") || !strings.Contains(strings.Join(run.cmds, "\n"), "systemctl start --no-block jellyfin.service") {
		t.Fatalf("commands: %v", run.cmds)
	}

	// Second install with the same name is refused; infra entry renders early-boot deps.
	if _, err := m.Install(ctx, catalog.SpecFor(idx.Find("jellyfin"), "", nil, "")); err == nil {
		t.Fatal("duplicate accepted")
	}
	ag, err := m.Install(ctx, catalog.SpecFor(idx.Find("adguard-home"), "", nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	// An Infrastructure Service pins the Node's address (Milestone 3).
	pinned := false
	for _, c := range run.cmds {
		if strings.HasPrefix(c, "nmcli connection modify Wired connection 1 ipv4.method manual ipv4.addresses 192.168.4.80/22 ipv4.gateway 192.168.4.1 ipv4.dns 192.168.4.1,1.1.1.1") {
			pinned = true
		}
	}
	if !pinned {
		t.Errorf("address not pinned; nmcli calls: %v", run.cmds)
	}
	agUnit, _ := os.ReadFile(filepath.Join(dir, "units", "adguard-home.container"))
	if !strings.Contains(string(agUnit), "Before=network-online.target") || ag.GetSpec().GetWebHostPort() == s.GetSpec().GetWebHostPort() {
		t.Fatalf("adguard unit:\n%s", agUnit)
	}

	list, _ := m.List(ctx)
	if len(list) != 2 {
		t.Fatalf("list %d", len(list))
	}
	logs, err := m.Logs(ctx, "jellyfin", "", 10)
	if err != nil || len(logs) != 2 {
		t.Fatalf("logs %v %v", logs, err)
	}

	run.state = "failed"
	if st2, _ := m.Status(ctx, s.GetSpec()); st2.GetState() != maitrev1.ServiceStatus_STATE_FAILED {
		t.Fatalf("state %v", st2.GetState())
	}

	if _, err := m.Control(ctx, &maitrev1.ControlServiceRequest{Name: "jellyfin", Action: maitrev1.ControlServiceRequest_ACTION_REMOVE, DeleteData: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "units", "jellyfin.container")); !os.IsNotExist(err) {
		t.Fatal("unit not removed")
	}
	if _, err := os.Stat(filepath.Join(pools, "general", ".Services", "jellyfin")); !os.IsNotExist(err) {
		t.Fatal("data not deleted")
	}
	if list, _ = m.List(ctx); len(list) != 1 {
		t.Fatalf("list after remove %d", len(list))
	}
	if _, err := m.Control(ctx, &maitrev1.ControlServiceRequest{Name: "jellyfin", Action: maitrev1.ControlServiceRequest_ACTION_START}); err == nil {
		t.Fatal("control of removed service accepted")
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// With ValidateUnits on, every Catalog entry's rendered unit passes podman's
// own Quadlet generator. Skipped where no generator is installed.
func TestCatalogEntriesPassQuadlet(t *testing.T) {
	if quadlet.Generator() == "" {
		t.Skip("no Quadlet generator installed")
	}
	idx, err := catalog.LoadDir("../../../catalog/entries")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range idx.Entries {
		t.Run(e.ID, func(t *testing.T) {
			res, err := services.Check(context.Background(), catalog.SpecFor(idx.Find(e.ID), "", nil, ""))
			if err != nil {
				t.Fatalf("%s rejected: %v", e.ID, err)
			}
			// The Catalog's entries need only the grants they declare
			// (docs/popularity-and-grants.md, G2).
			for _, p := range res.Protected {
				declared := false
				for _, g := range e.Grants {
					declared = declared || g.Matches(p.Component, p.Option)
				}
				if !declared {
					t.Errorf("%s needs a grant it does not declare: %+v", e.ID, p)
				}
			}
		})
	}
}

// Decision 282: a TZ setting of "auto" stays "auto" in the record and the
// unit takes the Fleet's time zone, this Node's when none is set; when the
// Fleet's zone changes, FollowTimezone renders the unit again and restarts
// the Service.
func TestAutoTimezoneFollowsTheFleet(t *testing.T) {
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
	if err := os.MkdirAll(filepath.Join(pools, "general"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := services.New(services.Config{UnitsDir: filepath.Join(dir, "units"), PoolsDir: pools, DefaultSpace: func(context.Context) string { return "general" }, HostPortBase: 20000}, st, run)
	unit := func() string {
		b, err := os.ReadFile(filepath.Join(dir, "units", "jellyfin.container"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	s, err := m.Install(ctx, catalog.SpecFor(idx.Find("jellyfin"), "", map[string]string{"TZ": "auto"}, ""))
	if err != nil {
		t.Fatal(err)
	}
	if s.GetSpec().GetSettings()["TZ"] != "auto" {
		t.Fatalf("the record should keep auto, got %q", s.GetSpec().GetSettings()["TZ"])
	}
	if !strings.Contains(unit(), "Environment=TZ="+timezone.Local()+"\n") {
		t.Fatalf("without a Fleet zone the unit should have the Node's:\n%s", unit())
	}

	zone := "Pacific/Auckland"
	if timezone.Local() == zone {
		zone = "America/Sao_Paulo"
	}
	if _, err := st.Put(ctx, timezone.Kind, timezone.ID, &maitrev1.FleetSettings{TimeZone: zone}, true); err != nil {
		t.Fatal(err)
	}
	run.cmds = nil
	m.FollowTimezone(ctx)
	if !strings.Contains(unit(), "Environment=TZ="+zone+"\n") {
		t.Fatalf("after the Fleet's zone changed the unit should have it:\n%s", unit())
	}
	if !slices.Contains(run.cmds, "systemctl try-restart --no-block jellyfin.service") {
		t.Fatalf("the Service should be restarted, commands %q", run.cmds)
	}
	var spec maitrev1.ServiceSpec
	if _, err := st.Get(ctx, services.KindService, "jellyfin", &spec); err != nil || spec.GetSettings()["TZ"] != "auto" {
		t.Fatalf("the record should still say auto: %q %v", spec.GetSettings()["TZ"], err)
	}

	// A Service with its own zone is left alone.
	if _, err := m.SetSettings(ctx, &maitrev1.SetSettingsRequest{Name: "jellyfin", Settings: map[string]string{"TZ": "Europe/Paris"}}); err != nil {
		t.Fatal(err)
	}
	run.cmds = nil
	m.FollowTimezone(ctx)
	if !strings.Contains(unit(), "Environment=TZ=Europe/Paris\n") || slices.Contains(run.cmds, "systemctl try-restart --no-block jellyfin.service") {
		t.Fatalf("an explicit zone should stay and not restart:\n%s\n%q", unit(), run.cmds)
	}
}
