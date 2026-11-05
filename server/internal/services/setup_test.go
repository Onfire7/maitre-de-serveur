package services_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

func single(name string, port uint32) *maitrev1.ServiceSpec {
	return &maitrev1.ServiceSpec{Name: name, CatalogId: name, Image: "docker.io/x/" + name + ":1",
		Quadlet: "[Container]\nImage={{ .Image }}\nContainerName={{ .Name }}\nNetwork={{ .Network }}\nEnvironment=A=1\n",
		Pages:   []*maitrev1.ServicePage{{Name: "web", Port: port, Path: "/"}}}
}

func newManager(t *testing.T) (*services.Manager, *store.Store, string, *fakeRunner) {
	t.Helper()
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	t.Cleanup(func() { st.Close() })
	dir := t.TempDir()
	pools := filepath.Join(dir, "pools")
	_ = os.MkdirAll(filepath.Join(pools, "general"), 0o755)
	run := &fakeRunner{state: "active"}
	m := services.New(services.Config{UnitsDir: filepath.Join(dir, "units"), SystemdDir: filepath.Join(dir, "systemd"), PoolsDir: pools, DefaultSpace: func(context.Context) string { return "general" }, HostPortBase: 20000}, st, run)
	return m, st, dir, run
}

// managerOver is newManager with a runner of the caller's own, for tests
// that script podman's answers.
func managerOver(t *testing.T, run services.Runner) (*services.Manager, *store.Store) {
	t.Helper()
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	t.Cleanup(func() { st.Close() })
	dir := t.TempDir()
	pools := filepath.Join(dir, "pools")
	_ = os.MkdirAll(filepath.Join(pools, "general"), 0o755)
	m := services.New(services.Config{UnitsDir: filepath.Join(dir, "units"), SystemdDir: filepath.Join(dir, "systemd"), PoolsDir: pools, DefaultSpace: func(context.Context) string { return "general" }, HostPortBase: 20000}, st, run)
	return m, st
}

func unitOf(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "units", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ADR 0010: every Service gets an address of the Fleet's subnet, unique
// across the Fleet, written on its shared-network line; a Service of
// components carries it on the web component's line.
func TestAddressesAreFleetUnique(t *testing.T) {
	ctx := context.Background()
	m, st, dir, _ := newManager(t)
	// A Service of another Node holds an address already.
	other := single("plex", 32400)
	other.NodeId, other.Address = "node-b", "10.90.0.10"
	if _, err := st.Put(ctx, services.KindService, "plex", other, true); err != nil {
		t.Fatal(err)
	}
	s, err := m.Install(ctx, single("sonarr", 8989))
	if err != nil {
		t.Fatal(err)
	}
	if s.GetSpec().GetAddress() != "10.90.0.11" {
		t.Fatalf("address %q, want the next free one", s.GetSpec().GetAddress())
	}
	if u := unitOf(t, dir, "sonarr.container"); !strings.Contains(u, "Network=maitre.network:ip=10.90.0.11\n") {
		t.Fatalf("unit:\n%s", u)
	}
	spec := multiSpec()
	if _, err := m.Install(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if u := unitOf(t, dir, "notes-app.container"); !strings.Contains(u, "Network=maitre.network:alias=notes,ip=10.90.0.12\n") {
		t.Fatalf("unit:\n%s", u)
	}
	if _, err := m.Install(ctx, func() *maitrev1.ServiceSpec { s := single("dup", 80); s.Address = "10.90.0.11"; return s }()); err == nil || !strings.Contains(err.Error(), "sonarr already has the address") {
		t.Fatalf("a taken address: %v", err)
	}
}

// Overrides land in the unit after the template, marked as the person's;
// a removal reaches the Node's own lines; an override that needs a grant
// is refused until one exists.
func TestOverrides(t *testing.T) {
	ctx := context.Background()
	m, _, dir, _ := newManager(t)
	spec := single("kuma", 3001)
	spec.Overrides = []*maitrev1.UnitOverride{
		{Key: "Environment", Value: "B=2"},
		{Key: "Environment", Value: "A=1", Remove: true},
		{Key: "HostName", Value: "uptime"},
		{Key: "NoNewPrivileges", Remove: true},
	}
	if _, err := m.Install(ctx, spec); err != nil {
		t.Fatal(err)
	}
	u := unitOf(t, dir, "kuma.container")
	for _, want := range []string{"Environment=B=2\n", "HostName=uptime\n"} {
		if !strings.Contains(u, want) {
			t.Errorf("missing %q in\n%s", want, u)
		}
	}
	for _, bad := range []string{"Environment=A=1\n", "NoNewPrivileges", "HostName=kuma\n"} {
		if strings.Contains(u, bad) {
			t.Errorf("still has %q in\n%s", bad, u)
		}
	}
	pv, err := m.PreviewSetup(ctx, "kuma", nil)
	if err != nil {
		t.Fatal(err)
	}
	unit := pv.Units["kuma.container"]
	srcOf := func(text string) string {
		for i, l := range strings.Split(strings.TrimSuffix(unit.String(), "\n"), "\n") {
			if l == text {
				return unit.Sources()[i]
			}
		}
		return "absent"
	}
	if srcOf("Environment=B=2") != "override" || srcOf("Image=docker.io/x/kuma:1") != "template" || srcOf("Label=dev.maitre.service=kuma") != "agent" {
		t.Errorf("sources: %q %q %q", srcOf("Environment=B=2"), srcOf("Image=docker.io/x/kuma:1"), srcOf("Label=dev.maitre.service=kuma"))
	}
	// A protected option through an override needs a grant like any other.
	var ge *services.GrantsError
	if _, err := m.SetSetup(ctx, "kuma", &maitrev1.Setup{Pages: spec.Pages, Overrides: []*maitrev1.UnitOverride{{Key: "AddCapability", Value: "NET_ADMIN"}}}); !errorsAs(err, &ge) || ge.Needed.GetOptions()[0] != "AddCapability=NET_ADMIN" {
		t.Fatalf("override needing a grant: %v", err)
	}
}

func errorsAs(err error, target **services.GrantsError) bool {
	for err != nil {
		if g, ok := err.(*services.GrantsError); ok {
			*target = g
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// A Service on a Tunnel shares its container's network: its own network
// and ports go, it is bound to the Tunnel's unit, and the Tunnel
// publishes its Pages and wants it, so the two restart together.
func TestTunnel(t *testing.T) {
	ctx := context.Background()
	m, _, dir, run := newManager(t)
	vpn := single("vpn", 8000)
	vpn.Tunnel, vpn.Pages = true, nil
	if _, err := m.Install(ctx, vpn); err != nil {
		t.Fatal(err)
	}
	qb := single("qbittorrent", 8080)
	qb.Quadlet += "PublishPort=6881:6881\n"
	if _, err := m.Install(ctx, qb); err != nil {
		t.Fatal(err)
	}
	run.cmds = nil
	st, err := m.SetSetup(ctx, "qbittorrent", &maitrev1.Setup{NetworkKind: services.NetworkTunnel, NetworkTarget: "vpn", Pages: qb.Pages, Address: "10.90.0.11"})
	if err != nil {
		t.Fatal(err)
	}
	hp := st.GetSpec().GetPages()[0].GetHostPort()
	d := unitOf(t, dir, "qbittorrent.container")
	for _, want := range []string{"Network=container:vpn\n", "BindsTo=vpn.service\n", "After=vpn.service\n"} {
		if !strings.Contains(d, want) {
			t.Errorf("missing %q in\n%s", want, d)
		}
	}
	for _, bad := range []string{"PublishPort=", "HostName=", "Network=maitre"} {
		if strings.Contains(d, bad) {
			t.Errorf("still has %q in\n%s", bad, d)
		}
	}
	tu := unitOf(t, dir, "vpn.container")
	for _, want := range []string{"Wants=qbittorrent.service\n", "PublishPort=127.0.0.1:" + itoa(int(hp)) + ":8080\n", "PublishPort=6881:6881\n", "ContainerName=vpn\n"} {
		if !strings.Contains(tu, want) {
			t.Errorf("missing %q in\n%s", want, tu)
		}
	}
	joined := strings.Join(run.cmds, "\n")
	if !strings.Contains(joined, "systemctl try-restart --no-block vpn.service") || !strings.Contains(joined, "systemctl try-restart --no-block qbittorrent.service") {
		t.Errorf("restarts:\n%s", joined)
	}
	pv, err := m.PreviewSetup(ctx, "qbittorrent", &maitrev1.Setup{NetworkKind: services.NetworkTunnel, NetworkTarget: "vpn", Pages: qb.Pages, Address: "10.90.0.11"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pv.Restarts, ",") != "qbittorrent,vpn" {
		t.Errorf("restarts %v", pv.Restarts)
	}
	// The Tunnel cannot go while something is on it.
	if _, err := m.Control(ctx, &maitrev1.ControlServiceRequest{Name: "vpn", Action: maitrev1.ControlServiceRequest_ACTION_REMOVE}); err == nil || !strings.Contains(err.Error(), "qbittorrent uses it") {
		t.Fatalf("remove a used Tunnel: %v", err)
	}
	// A failed Tunnel is the Service's own failure, in words.
	run.state = "failed"
	s, err := m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range s {
		if x.GetSpec().GetName() == "qbittorrent" && !strings.HasPrefix(x.GetMessage(), "through vpn: failed") {
			t.Errorf("status %v", x.GetMessage())
		}
	}
	// Not a Tunnel: refused.
	if _, err := m.SetSetup(ctx, "vpn", &maitrev1.Setup{NetworkKind: services.NetworkTunnel, NetworkTarget: "qbittorrent", Address: "10.90.0.10"}); err == nil {
		t.Fatal("a Tunnel through a Service")
	}
}

// A Service may join a Service of components' own network, under its own
// name, to reach the components as that Service does; a Service of one
// container has no such network, and the reason says so.
func TestJoinServiceNetwork(t *testing.T) {
	ctx := context.Background()
	m, _, dir, _ := newManager(t)
	if _, err := m.Install(ctx, multiSpec()); err != nil {
		t.Fatal(err)
	}
	s := single("sonarr", 8989)
	if _, err := m.Install(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetSetup(ctx, "sonarr", &maitrev1.Setup{NetworkKind: services.NetworkService, NetworkTarget: "notes", Pages: s.Pages, Address: "10.90.0.11"}); err != nil {
		t.Fatal(err)
	}
	if u := unitOf(t, dir, "sonarr.container"); !strings.Contains(u, "Network=maitre-notes.network:alias=sonarr\n") || !strings.Contains(u, "Network=maitre.network:ip=10.90.0.11\n") {
		t.Fatalf("unit:\n%s", u)
	}
	if _, err := m.SetSetup(ctx, "notes", &maitrev1.Setup{NetworkKind: services.NetworkService, NetworkTarget: "sonarr", Pages: multiSpec().Pages, Address: "10.90.0.10"}); err == nil || !strings.Contains(err.Error(), "already reachable as sonarr") {
		t.Fatalf("join a single container's network: %v", err)
	}
	if _, err := m.Control(ctx, &maitrev1.ControlServiceRequest{Name: "notes", Action: maitrev1.ControlServiceRequest_ACTION_REMOVE}); err == nil || !strings.Contains(err.Error(), "sonarr uses it") {
		t.Fatalf("remove a joined Service: %v", err)
	}
}

// Several Pages: each its own loopback port and name, the first the
// Service's own; exposure per Page follows the Service's unless set.
func TestPages(t *testing.T) {
	ctx := context.Background()
	m, _, dir, _ := newManager(t)
	s := single("immich", 2283)
	s.Pages = append(s.Pages, &maitrev1.ServicePage{Name: "admin", Port: 2284, Path: "/admin", Label: "Admin"})
	st, err := m.Install(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	p := st.GetSpec().GetPages()
	if len(p) != 2 || p[0].GetHostPort() == 0 || p[1].GetHostPort() == 0 || p[0].GetHostPort() == p[1].GetHostPort() {
		t.Fatalf("pages %v", p)
	}
	if st.GetSpec().GetWebHostPort() != p[0].GetHostPort() || st.GetSpec().GetWebPort() != 2283 {
		t.Errorf("web_* fields do not mirror the first Page: %v", st.GetSpec())
	}
	if len(st.GetPages()) != 2 || st.GetPages()[0].GetUrl() != "http://immich.local/" || st.GetPages()[1].GetUrl() != "http://immich-admin.local/admin" || st.GetUrl() != "http://immich.local/" {
		t.Errorf("page statuses %v", st.GetPages())
	}
	u := unitOf(t, dir, "immich.container")
	if !strings.Contains(u, "PublishPort=127.0.0.1:"+itoa(int(p[0].GetHostPort()))+":2283\n") || !strings.Contains(u, "PublishPort=127.0.0.1:"+itoa(int(p[1].GetHostPort()))+":2284\n") {
		t.Errorf("unit:\n%s", u)
	}
	st, err = m.SetPageExposure(ctx, "immich", "admin", "remote", "")
	if err != nil {
		t.Fatal(err)
	}
	if st.GetSpec().GetPages()[1].GetExposure() != "remote" || st.GetSpec().GetExposure() != "private" {
		t.Errorf("exposure: %v", st.GetSpec())
	}
	if _, err := m.SetPageExposure(ctx, "immich", "nope", "remote", ""); err == nil {
		t.Fatal("unknown page")
	}
	// Setup keeps the loopback ports of Pages that stay.
	st2, err := m.SetSetup(ctx, "immich", &maitrev1.Setup{Pages: []*maitrev1.ServicePage{proto.Clone(p[1]).(*maitrev1.ServicePage), {Name: "other", Port: 9000}}, Address: st.GetSpec().GetAddress()})
	if err != nil {
		t.Fatal(err)
	}
	if st2.GetSpec().GetPages()[0].GetHostPort() != p[1].GetHostPort() || st2.GetSpec().GetPages()[1].GetHostPort() == 0 || st2.GetPages()[0].GetUrl() != "http://immich.local/admin" {
		t.Errorf("after setup: %v %v", st2.GetSpec().GetPages(), st2.GetPages())
	}
}

// Decisions 315-321: a Service that cannot start re-pulls its image on every
// restart, and systemd's default burst of five over ten seconds never catches
// a failing pull, which takes far longer than the window. Avalon looped ~1800
// times that way.
func TestAFailingServiceGivesUpInsteadOfPullingForever(t *testing.T) {
	ctx := context.Background()
	m, _, dir, _ := newManager(t)
	if _, err := m.Install(ctx, single("jellyfin", 8096)); err != nil {
		t.Fatal(err)
	}
	u := unitOf(t, dir, "jellyfin.container")
	head, _, found := strings.Cut(u, "\n[Service]")
	if !found {
		t.Fatalf("no [Service] section in\n%s", u)
	}
	for _, want := range []string{"StartLimitIntervalSec=1h", "StartLimitBurst=5"} {
		if !strings.Contains(head, want) {
			t.Errorf("unit is missing %s before [Service]:\n%s", want, head)
		}
	}
}

// A unit held down by that limit refuses a plain start, so asking again
// clears the counter first.
func TestStartingAHeldDownServiceClearsItsLimit(t *testing.T) {
	ctx := context.Background()
	m, _, _, run := newManager(t)
	if _, err := m.Install(ctx, single("jellyfin", 8096)); err != nil {
		t.Fatal(err)
	}

	run.cmds = nil
	if _, err := m.Control(ctx, &maitrev1.ControlServiceRequest{Name: "jellyfin", Action: maitrev1.ControlServiceRequest_ACTION_START}); err != nil {
		t.Fatal(err)
	}
	var reset, start int
	for i, c := range run.cmds {
		if strings.HasPrefix(c, "systemctl reset-failed") {
			reset = i + 1
		}
		if strings.HasPrefix(c, "systemctl start") {
			start = i + 1
		}
	}
	if reset == 0 {
		t.Fatalf("Start did not clear the start limit: %v", run.cmds)
	}
	if start == 0 || reset > start {
		t.Errorf("reset-failed must come before start: %v", run.cmds)
	}
}
