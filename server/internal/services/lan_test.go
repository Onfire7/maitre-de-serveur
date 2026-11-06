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
	"google.golang.org/protobuf/proto"
)

// fakeLAN is the fixture Node's own place on the home network (decision
// 327): the address the Infrastructure test expects nmcli to pin, on a
// wired interface a macvlan could really be built over.
func fakeLAN() (string, string, string, string, error) {
	return "eth0", "192.168.4.80", "192.168.4.0/22", "192.168.4.1", nil
}

// allowLAN signs the grant an address on the home network needs, as a
// Trusted Device does (decision 189).
func allowLAN(t *testing.T, st *store.Store, name, repo string) {
	t.Helper()
	id := services.GrantID("node-a", name, "", "Network=lan")
	b, err := proto.Marshal(&maitrev1.Grant{NodeId: "node-a", Service: name, Option: "Network=lan", ImageRepository: repo})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutRaw(context.Background(), services.KindGrant, id, b, true); err != nil {
		t.Fatal(err)
	}
}

// lanRepo is the image repository lanSpec uses, which a grant names.
const lanRepo = "docker.io/example/dns"

// lanSpec is a one-container Service that wants an address of its own on
// the home network, as a Catalog entry marked lan: true becomes.
func lanSpec(name, addr string) *maitrev1.ServiceSpec {
	return &maitrev1.ServiceSpec{
		Name:           name,
		CatalogId:      name,
		Image:          "docker.io/example/dns@sha256:" + strings.Repeat("a", 64),
		NetworkKind:    services.NetworkLAN,
		Infrastructure: true,
		Settings:       map[string]string{catalog.LANAddressSetting: addr},
		Quadlet:        "[Container]\nImage={{ .Image }}\nContainerName={{ .Name }}\nNetwork={{ .Network }}\nPublishPort=53:53/tcp\nPublishPort=53:53/udp\n",
		Pages: []*maitrev1.ServicePage{
			{Name: "web", Port: 3000, Path: "/"},
		},
	}
}

func TestAServiceOnTheHomeNetworkKeepsTheSharedOneAndPublishesNothing(t *testing.T) {
	m, store, dir, _ := newManager(t)
	ctx := context.Background()
	allowLAN(t, store, "dns", lanRepo)
	st, err := m.Install(ctx, lanSpec("dns", "192.168.4.81"))
	if err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(filepath.Join(dir, "units", "dns.container"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(unit)
	// Two networks: the shared one at the Service's Fleet address, so
	// Caddy and the Fleet reach it as they do any Service, and the home
	// network at the address the person chose.
	if !strings.Contains(body, "Network=maitre.network:ip="+st.GetSpec().GetAddress()) {
		t.Errorf("not on the shared network:\n%s", body)
	}
	if !strings.Contains(body, "Network=maitre-lan.network:ip=192.168.4.81") {
		t.Errorf("not on the home network:\n%s", body)
	}
	// The template's published ports are gone: a port of the Node is
	// exactly what this kind of Service does not take, and 53 of the Node
	// cannot be bound at all. The Page's loopback port stays, since that
	// is how Caddy serves the page.
	if strings.Contains(body, "PublishPort=53") {
		t.Errorf("still publishes a port of the Node:\n%s", body)
	}
	if !strings.Contains(body, "PublishPort=127.0.0.1:") {
		t.Errorf("the Page lost its loopback port:\n%s", body)
	}
}

func TestTheHomeNetworkUnitComesFromTheNodesOwnInterface(t *testing.T) {
	m, store, dir, run := newManager(t)
	ctx := context.Background()
	allowLAN(t, store, "dns", lanRepo)
	if _, err := m.Install(ctx, lanSpec("dns", "192.168.4.81")); err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(filepath.Join(dir, "units", services.LANNetwork))
	if err != nil {
		t.Fatal(err)
	}
	// A macvlan over the Node's own interface, on the home's own subnet
	// and gateway: a macvlan child is on the same wire as its parent, so
	// any other subnet would be an address nothing in the home routes to.
	for _, want := range []string{
		"NetworkName=maitre-lan\n",
		"Driver=macvlan\n",
		"Options=parent=eth0\n",
		"Subnet=192.168.4.0/22\n",
		"Gateway=192.168.4.1\n",
	} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("home network unit missing %q:\n%s", want, unit)
		}
	}
	// Created here rather than left to the Service, and restarted rather
	// than started, for the reasons ensureNetwork gives about the shared
	// network: a oneshot that has already run answers start with success
	// and does nothing.
	cmds := strings.Join(run.cmds, "\n")
	if !strings.Contains(cmds, "systemctl restart maitre-lan-network.service") {
		t.Errorf("the home network was not created: %v", run.cmds)
	}
}

func TestTheHomeNetworkAddressIsChecked(t *testing.T) {
	for _, c := range []struct {
		name, addr, want string
	}{
		{"empty", "", "needs an address of its own"},
		{"not an address", "nonsense", "not an IPv4 address"},
		{"another network", "10.0.0.5", "not on your home network"},
		{"the Node's own", "192.168.4.80", "this Node's own address"},
		{"the router's", "192.168.4.1", "your router's address"},
		{"the network itself", "192.168.4.0", "the network's own address"},
		{"the broadcast", "192.168.7.255", "the network's own address"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, store, _, _ := newManager(t)
			allowLAN(t, store, "dns", lanRepo)
			_, err := m.Install(context.Background(), lanSpec("dns", c.addr))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
}

func TestTwoServicesCannotShareOneAddressOnTheHomeNetwork(t *testing.T) {
	m, store, _, _ := newManager(t)
	ctx := context.Background()
	allowLAN(t, store, "dns", lanRepo)
	allowLAN(t, store, "dns2", lanRepo)
	if _, err := m.Install(ctx, lanSpec("dns", "192.168.4.81")); err != nil {
		t.Fatal(err)
	}
	_, err := m.Install(ctx, lanSpec("dns2", "192.168.4.81"))
	if err == nil || !strings.Contains(err.Error(), "dns already has the address 192.168.4.81") {
		t.Fatalf("got %v", err)
	}
}

func TestANodeThatCannotOfferAnAddressSaysSo(t *testing.T) {
	m, store, _, _ := newManagerWithout(t)
	allowLAN(t, store, "dns", lanRepo)
	_, err := m.Install(context.Background(), lanSpec("dns", "192.168.4.81"))
	if err == nil || !strings.Contains(err.Error(), "cannot give a Service an address of its own on the home network") {
		t.Fatalf("got %v", err)
	}
}

func TestAnAddressOnTheHomeNetworkIsAGrantedOption(t *testing.T) {
	body := "[Container]\nImage=x\nNetwork=maitre.network:ip=10.90.0.10\nNetwork=" + services.LANNetwork + ":ip=192.168.4.81\n"
	got := services.Protected(body, nil)
	found := false
	for _, o := range got {
		if o == "Network=lan" {
			found = true
		}
		// The address is not part of the grant, so changing it later does
		// not ask the person again.
		if strings.Contains(o, "192.168.4.81") {
			t.Errorf("the grant names the address: %q", o)
		}
	}
	if !found {
		t.Errorf("Network=lan is not protected: %v", got)
	}
}

func TestAServiceOfSeveralContainersCannotTakeAnAddressOnTheHomeNetwork(t *testing.T) {
	m, store, _, _ := newManager(t)
	allowLAN(t, store, "dns", lanRepo)
	spec := lanSpec("dns", "192.168.4.81")
	spec.Quadlet = ""
	spec.Components = []*maitrev1.ServiceComponent{
		{Name: "app", Image: spec.GetImage(), Quadlet: "[Container]\nImage={{ .Image }}\n"},
		{Name: "db", Image: spec.GetImage(), Quadlet: "[Container]\nImage={{ .Image }}\n"},
	}
	spec.Pages[0].Component = "app"
	_, err := m.Install(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "an address on the home network goes to one container") {
		t.Fatalf("got %v", err)
	}
}

// ensureNetwork reconciles against Podman's live network and never
// against the unit file, because image/maitre.network ships with exactly
// the bytes NetworkUnitBody writes. That is only true while this holds,
// and a drift would make every repair below it dead code.
func TestTheShippedNetworkUnitIsTheOneTheNodeWrites(t *testing.T) {
	shipped, err := os.ReadFile("../../../image/maitre.network")
	if err != nil {
		t.Fatal(err)
	}
	if string(shipped) != services.NetworkUnitBody() {
		t.Errorf("image/maitre.network is:\n%s\nthe Node writes:\n%s", shipped, services.NetworkUnitBody())
	}
	// Both of the Node's network units turn off the wait for
	// network-online.target that Quadlet would add, which is what lets an
	// infrastructure Service be ordered before that target without systemd
	// deleting its own start job.
	for _, body := range []string{services.NetworkUnitBody(), services.LANNetworkBodyFor("eth0", "192.168.4.0/22", "192.168.4.1")} {
		if !strings.Contains(body, "[Quadlet]\nDefaultDependencies=false\n") {
			t.Errorf("a network unit still waits for the network to be online:\n%s", body)
		}
		if !strings.Contains(body, "[Unit]\nBefore=network-online.target\n") {
			t.Errorf("a network unit is not ordered before the network is online:\n%s", body)
		}
	}
}

// An infrastructure Service is ordered before network-online.target, and
// Quadlet gives every unit it generates Wants=/After= that same target --
// so without [Quadlet] DefaultDependencies=false the unit is before and
// after one target at once, systemd breaks the cycle by deleting the
// Service's own start job, and the Service never comes up at boot. That is
// what Boulder did on 2026-11-06, three reboots running, while an empty
// After= sat in the unit doing nothing: an empty assignment resets Wants=
// but not an ordering list.
func TestAnInfrastructureServiceDoesNotContradictItself(t *testing.T) {
	m, store, dir, _ := newManager(t)
	allowLAN(t, store, "dns", lanRepo)
	allowLAN(t, store, "plain", lanRepo)
	if _, err := m.Install(context.Background(), lanSpec("dns", "192.168.4.81")); err != nil {
		t.Fatal(err)
	}
	body := unitOf(t, dir, "dns.container")
	if !strings.Contains(body, "\n[Quadlet]\nDefaultDependencies=false\n") {
		t.Errorf("Quadlet still orders it after network-online.target:\n%s", body)
	}
	if !strings.Contains(body, "Before=network-online.target") {
		t.Errorf("not ordered before the network is online:\n%s", body)
	}
	// An ordinary Service must keep waiting for the network: the whole
	// point of the pair above is that it is only for the Services that
	// have to be up before it.
	spec := lanSpec("plain", "192.168.4.82")
	spec.Infrastructure = false
	if _, err := m.Install(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	plain := unitOf(t, dir, "plain.container")
	if strings.Contains(plain, "[Quadlet]") || strings.Contains(plain, "Before=network-online.target") {
		t.Errorf("an ordinary Service touches the network-online ordering:\n%s", plain)
	}
}
