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
	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
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
func TestCatalogNeedsNoGrants(t *testing.T) {
	idx, err := catalog.LoadDir("../../../catalog/entries")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range idx.Entries {
		body := strings.NewReplacer("{{ .Image }}", "img", "{{ .Name }}", "n", "{{ .Network }}", services.Network).Replace(e.Quadlet)
		body = replaceTemplates(body)
		if got := services.Protected(body, []string{"/pools"}); len(got) > 0 {
			t.Errorf("%s: %q", e.ID, got)
		}
	}
}

// replaceTemplates puts a managed folder or a value for the rest.
func replaceTemplates(s string) string {
	for {
		i := strings.Index(s, "{{")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "}}")
		v := "x"
		if strings.Contains(s[i:i+j], ".Volumes.") {
			v = "/pools/v"
		}
		s = s[:i] + v + s[i+j+2:]
	}
}

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
