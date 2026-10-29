package services_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

func TestProtected(t *testing.T) {
	managed := []string{"/var/mnt/pools/media/jellyfin/config", "/var/mnt/pools/media/Libraries"}
	body := strings.Join([]string{
		"[Container]",
		"Image=docker.io/x/y:1",
		"Environment=A=b",
		"Volume=/var/mnt/pools/media/jellyfin/config:/config:Z",
		"Volume=/var/mnt/pools/media/Libraries/Films:/films:z",
		"Volume=named:/data",
		"Network=maitre.network",
		"PublishPort=53:53/udp",
		"DropCapability=ALL",
		"Volume=/etc:/host-etc",
		"Volume=/var/mnt/pools/media/other:/other",
		"Mount=type=bind,source=/run/podman/podman.sock,destination=/sock",
		"Mount=type=volume,source=v,destination=/v",
		"AddCapability=NET_ADMIN SYS_TIME",
		"AddDevice=/dev/dri",
		"PodmanArgs=--pid=host",
		"SecurityLabelDisable=true",
		"NoNewPrivileges=false",
		"FutureKey=1",
		"[Service]",
		"ExecStartPre=/bin/sh -c 'id'",
	}, "\n")
	want := []string{
		"AddCapability=NET_ADMIN", "AddCapability=SYS_TIME", "AddDevice=/dev/dri", "FutureKey=1", "NoNewPrivileges=false",
		"PodmanArgs=--pid=host", "SecurityLabelDisable=true", "Volume=/etc", "Volume=/run/podman/podman.sock",
		"Volume=/var/mnt/pools/media/other", "[Service] ExecStartPre=/bin/sh -c 'id'",
	}
	if got := services.Protected(body, managed); !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if got := services.Protected("[Container]\nNetwork=host\nUserNS=host\n", nil); !reflect.DeepEqual(got, []string{"Network=host", "UserNS=host"}) {
		t.Fatalf("namespaces: %q", got)
	}
}

// The Catalog's own entries need no grants.
// The Catalog's entries need only the grants they declare: checked by
// rendering each as a Node does, in internal/catalogcheck.

func TestImageRepository(t *testing.T) {
	for in, want := range map[string]string{
		"docker.io/jellyfin/jellyfin:10.10.7@sha256:abc": "docker.io/jellyfin/jellyfin",
		"localhost:5000/app":                             "localhost:5000/app",
		"localhost:5000/app:1":                           "localhost:5000/app",
		"app@sha256:1":                                   "app",
	} {
		if got := services.ImageRepository(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

func TestGrantsGateInstall(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	defer st.Close()
	dir := t.TempDir()
	pools := filepath.Join(dir, "pools")
	_ = os.MkdirAll(filepath.Join(pools, "general"), 0o755)
	trusted := true
	m := services.New(services.Config{UnitsDir: filepath.Join(dir, "units"), PoolsDir: pools, DefaultSpace: func(context.Context) string { return "general" }, HostPortBase: 20000,
		DeviceSigned: func(context.Context, *maitrev1.Record) bool { return trusted }}, st, &fakeRunner{state: "active"})
	spec := &maitrev1.ServiceSpec{Name: "dns", Image: "docker.io/x/dns:1", Quadlet: "[Container]\nImage={{ .Image }}\nAddCapability=NET_ADMIN\nNetwork=host\n"}
	_, err := m.Install(ctx, proto.Clone(spec).(*maitrev1.ServiceSpec))
	var ge *services.GrantsError
	if !errors.As(err, &ge) {
		t.Fatalf("install without grants: %v", err)
	}
	n := ge.Needed
	if !reflect.DeepEqual(n.GetOptions(), []string{"AddCapability=NET_ADMIN", "Network=host"}) || n.GetImageRepository() != "docker.io/x/dns" || n.GetNodeId() != "node-a" || len(n.GetGrantIds()) != 2 {
		t.Fatalf("needed: %v", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "units", "dns.container")); err == nil {
		t.Fatal("unit written without grants")
	}
	grant := func(i int, repo string) {
		b, _ := proto.Marshal(&maitrev1.Grant{NodeId: "node-a", Service: "dns", Option: n.GetOptions()[i], ImageRepository: repo})
		if _, err := st.PutRaw(ctx, services.KindGrant, n.GetGrantIds()[i], b, true); err != nil {
			t.Fatal(err)
		}
	}
	grant(0, "docker.io/x/dns")
	grant(1, "docker.io/other/image") // for another image: does not count
	if _, err := m.Install(ctx, proto.Clone(spec).(*maitrev1.ServiceSpec)); !errors.As(err, &ge) || len(ge.Needed.GetOptions()) != 1 {
		t.Fatalf("one grant missing: %v", err)
	}
	grant(1, "docker.io/x/dns")
	trusted = false // signed by something other than a Trusted Device
	if _, err := m.Install(ctx, proto.Clone(spec).(*maitrev1.ServiceSpec)); !errors.As(err, &ge) {
		t.Fatalf("grants not signed by a device: %v", err)
	}
	trusted = true
	if _, err := m.Install(ctx, proto.Clone(spec).(*maitrev1.ServiceSpec)); err != nil {
		t.Fatalf("granted: %v", err)
	}
	unit, _ := os.ReadFile(filepath.Join(dir, "units", "dns.container"))
	if !strings.Contains(string(unit), "AddCapability=NET_ADMIN") || !strings.Contains(string(unit), "NoNewPrivileges=true") {
		t.Fatalf("unit:\n%s", unit)
	}
	// Revoking a grant stops the Service and removes its unit.
	if _, err := st.Delete(ctx, services.KindGrant, n.GetGrantIds()[1], true); err != nil {
		t.Fatal(err)
	}
	m.EnforceGrants(ctx)
	if _, err := os.Stat(filepath.Join(dir, "units", "dns.container")); err == nil {
		t.Fatal("unit kept after its grant was revoked")
	}
}

// G4: a granted Network=host Service is served on its own port of the
// Node, which must be free.
func TestHostNetwork(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	defer st.Close()
	dir := t.TempDir()
	pools := filepath.Join(dir, "pools")
	_ = os.MkdirAll(filepath.Join(pools, "general"), 0o755)
	m := services.New(services.Config{UnitsDir: filepath.Join(dir, "units"), PoolsDir: pools, DefaultSpace: func(context.Context) string { return "general" }, HostPortBase: 20000}, st, &fakeRunner{state: "active"})
	spec := func(name string, port uint32) *maitrev1.ServiceSpec {
		return &maitrev1.ServiceSpec{Name: name, Image: "docker.io/x/ha:1", WebPort: port, OptionalGrants: map[string]bool{"discovery": true},
			Quadlet: "[Container]\nImage={{ .Image }}\n{{ if .Grants.discovery }}Network=host\n{{ else }}Network={{ .Network }}\n{{ end }}"}
	}
	grant := func(name string) {
		id := services.GrantID("node-a", name, "", "Network=host")
		b, _ := proto.Marshal(&maitrev1.Grant{NodeId: "node-a", Service: name, Option: "Network=host", ImageRepository: "docker.io/x/ha"})
		if _, err := st.PutRaw(ctx, services.KindGrant, id, b, true); err != nil {
			t.Fatal(err)
		}
	}
	grant("ha")
	s, err := m.Install(ctx, spec("ha", 8123))
	if err != nil {
		t.Fatal(err)
	}
	if s.GetSpec().GetWebHostPort() != 8123 {
		t.Errorf("web host port %d, want the app's own 8123", s.GetSpec().GetWebHostPort())
	}
	unit, _ := os.ReadFile(filepath.Join(dir, "units", "ha.container"))
	if !strings.Contains(string(unit), "Network=host") || strings.Contains(string(unit), "PublishPort=") || strings.Contains(string(unit), "HostName=") {
		t.Errorf("unit:\n%s", unit)
	}
	grant("ha2")
	if _, err := m.Install(ctx, spec("ha2", 8123)); err == nil || !strings.Contains(err.Error(), "port 8123 of the Node itself, which ha already uses") {
		t.Errorf("a second Service on port 8123: %v", err)
	}
	if _, err := m.Install(ctx, spec("ha2", 443)); err == nil || !strings.Contains(err.Error(), "Maître's web server") {
		t.Errorf("the Node's own port 443: %v", err)
	}
	// With the feature off it is an ordinary Service, on a loopback port.
	off := spec("ha3", 8123)
	off.OptionalGrants["discovery"] = false
	s, err = m.Install(ctx, off)
	if err != nil || s.GetSpec().GetWebHostPort() == 8123 {
		t.Fatalf("feature off: %v, port %d", err, s.GetSpec().GetWebHostPort())
	}
}

func TestRenderKeepsSectionsApart(t *testing.T) {
	spec := &maitrev1.ServiceSpec{Name: "x", Image: "img", WebPort: 80, WebHostPort: 20001,
		Quadlet: "[Container]\nImage={{ .Image }}\n[Service]\nExecStartPre=/bin/true\n"}
	unit, err := services.Render(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	u := string(unit)
	c, s := strings.Index(u, "[Container]"), strings.Index(u, "ExecStartPre=")
	for _, line := range []string{"PublishPort=127.0.0.1:20001:80", "NoNewPrivileges=true", "Label=dev.maitre.service=x"} {
		if i := strings.Index(u, line); i < c || i > s {
			t.Errorf("%s is not in [Container]:\n%s", line, u)
		}
	}
	// A template that sets NoNewPrivileges keeps its own.
	spec.Quadlet = "[Container]\nImage={{ .Image }}\nNoNewPrivileges=false\n"
	unit, _ = services.Render(spec, nil)
	if strings.Count(string(unit), "NoNewPrivileges") != 1 {
		t.Fatalf("unit:\n%s", unit)
	}
}

// Quadlet copies values into ExecStart, where systemd expands $ and %; a
// setting reaches the container as typed only with them doubled.
func TestRenderEscapesSettingsForSystemd(t *testing.T) {
	spec := &maitrev1.ServiceSpec{Name: "x", Image: "img", Settings: map[string]string{"PW": "a$b${c}%n"},
		Quadlet: "[Container]\nImage={{ .Image }}\nEnvironment=PW={{ .Settings.PW }}\n"}
	unit, err := services.Render(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "Environment=PW=a$$b$${c}%%n\n") {
		t.Fatalf("unit:\n%s", unit)
	}
}

func TestRenderNamesTheHost(t *testing.T) {
	spec := &maitrev1.ServiceSpec{Name: "jellyfin", Image: "img", Quadlet: "[Container]\nImage={{ .Image }}\n"}
	unit, _ := services.Render(spec, nil)
	if !strings.Contains(string(unit), "\nHostName=jellyfin\n") {
		t.Fatalf("no HostName:\n%s", unit)
	}
	// The template's own name, or the host's or a pod's namespace, wins.
	for _, line := range []string{"HostName=media", "Network=host", "UTS=host", "Pod=media.pod"} {
		spec.Quadlet = "[Container]\nImage={{ .Image }}\n" + line + "\n"
		unit, _ = services.Render(spec, nil)
		if strings.Contains(string(unit), "HostName=jellyfin") {
			t.Errorf("%s: unit sets HostName=jellyfin:\n%s", line, unit)
		}
	}
}

func TestInstallRefusesLineBreaks(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	defer st.Close()
	m := services.New(services.Config{UnitsDir: t.TempDir(), PoolsDir: t.TempDir()}, st, &fakeRunner{})
	_, err := m.Install(context.Background(), &maitrev1.ServiceSpec{Name: "xx", Image: "img", Quadlet: "[Container]\nImage={{ .Image }}\nEnvironment=A={{ .Settings.A }}\n",
		Settings: map[string]string{"A": "1\n[Service]\nExecStartPre=/bin/sh"}})
	if err == nil || !strings.Contains(err.Error(), "line break") {
		t.Fatalf("got %v", err)
	}
}
