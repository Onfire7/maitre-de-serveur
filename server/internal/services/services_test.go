package services_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
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
	m := services.New(services.Config{UnitsDir: filepath.Join(dir, "units"), PoolDir: filepath.Join(dir, "pool"), HostPortBase: 20000},
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
		"Volume=" + filepath.Join(dir, "pool", "jellyfin", "media") + ":/media:Z",
		"Network=maitre.network",
		"PublishPort=127.0.0.1:" + itoa(int(s.GetSpec().GetWebHostPort())) + ":8096",
		"AutoUpdate=registry",
		"[Install]",
	} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "pool", "jellyfin", "config")); err != nil {
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
	logs, err := m.Logs(ctx, "jellyfin", 10)
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
	if _, err := os.Stat(filepath.Join(dir, "pool", "jellyfin")); !os.IsNotExist(err) {
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
