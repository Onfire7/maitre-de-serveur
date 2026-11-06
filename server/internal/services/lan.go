package services

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Decision 327: a Service may have an address of its own on the home
// network, beside the Node's. Its container joins a macvlan over the
// Node's LAN interface, which gives it a MAC address and an address of
// its own there, and so every port on that address — including 53.
//
// Port 53 of the Node is the reason this exists. ADR 0010 has the agent
// answer <name>.local for the Node's containers, on 10.90.0.2:53, and
// Podman's DNS= takes an address and no port, so that listener can never
// move. A listening socket on one address blocks a wildcard bind on the
// same port whatever SO_REUSEADDR says, so `PublishPort=53:53` can never
// be bound on any Node of a Fleet, and `Network=host` fails the same way
// one layer further in: AdGuard Home would be binding 0.0.0.0:53 itself.
// systemd-resolved's stubs on 127.0.0.53 and 127.0.0.54 hold it too, but
// they are not the reason — turning them off would change nothing.
//
// The Service keeps its place on the shared maitre network as well, so
// Caddy still serves its Pages at <name>.local on the Node, the Fleet
// still reaches it, and nothing about the rest of ADR 0010 changes. Only
// the extra address is new.

const (
	// LANNetwork is the .network unit of the home network.
	LANNetwork = "maitre-lan.network"
	// LANNetworkName is the Podman network it creates.
	LANNetworkName = "maitre-lan"
)

// LANNetworkBody is the maitre-lan.network file the Node writes. The
// subnet and gateway are the home's own, read off the Node: a macvlan
// child is on the same wire as its parent, so any other subnet would be
// an address nothing in the home could route to.
// LANNetworkBodyFor is LANNetworkBody by its parts, for tests and for
// anything outside this package that has to know what the Node writes.
func LANNetworkBodyFor(iface, subnet, gateway string) string {
	return LANNetworkBody(hostLAN{Iface: iface, Subnet: subnet, Gateway: gateway})
}

func LANNetworkBody(lan hostLAN) string {
	return "# Written by Maître. Do not edit.\n" + networkUnitPreamble +
		"[Network]\nNetworkName=" + LANNetworkName +
		"\nDriver=macvlan\nOptions=parent=" + lan.Iface +
		"\nSubnet=" + lan.Subnet +
		"\nGateway=" + lan.Gateway + "\n"
}

// hostLAN is what the agent reports about the home network; hostnet.LAN
// without the import, so this package stays testable without one.
type hostLAN struct {
	Iface, Address, Subnet, Gateway string
}

// lan reads the Node's place on the home network, or says why a Service
// cannot have an address there.
func (m *Manager) lan() (hostLAN, error) {
	if m.cfg.LAN == nil {
		return hostLAN{}, fmt.Errorf("this Node cannot give a Service an address of its own on the home network")
	}
	iface, addr, subnet, gw, err := m.cfg.LAN()
	if err != nil {
		return hostLAN{}, err
	}
	return hostLAN{Iface: iface, Address: addr, Subnet: subnet, Gateway: gw}, nil
}

// ensureLANNetwork writes the home network's unit so Quadlet can create
// it, and is called before a Service that joins it is written. Unlike the
// shared network (ensureNetwork) there is nothing to reconcile against a
// live Podman network: a macvlan holds no addresses of its own, so a
// Node whose LAN moved to another subnet only needs the unit rewritten
// and the network recreated, which Quadlet does when the file changes.
func (m *Manager) ensureLANNetwork(ctx context.Context) error {
	lan, err := m.lan()
	if err != nil {
		return err
	}
	path := filepath.Join(m.cfg.UnitsDir, LANNetwork)
	want := LANNetworkBody(lan)
	if cur, err := os.ReadFile(path); err == nil && string(cur) == want {
		return nil
	}
	if err := os.MkdirAll(m.cfg.UnitsDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		return err
	}
	if _, err := m.run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	// Only when the unit changed, which the early return above means: a
	// Service's own unit has Requires= on this one, so Quadlet starts an
	// unchanged network by itself and there is nothing to repair. What
	// Quadlet will not do is replace a network whose parent or subnet
	// moved, and a oneshot that has already run answers `start` with
	// success and does nothing -- so changing the file has to be followed
	// by a restart, not a start. Done here rather than left to the
	// Service so that a Node which cannot make the network says why in
	// its own log, beside the cause, instead of in the Service's.
	if _, err := m.run.Run(ctx, "systemctl", "restart", "maitre-lan-network.service"); err != nil {
		return fmt.Errorf("create the home network over %s: %w", lan.Iface, err)
	}
	return nil
}

// liftLANAddress moves the address the person entered as a setting onto
// the Service's record, where the renderer and the checks below read it.
// Kept in settings as well: that is where the Console shows it and edits
// it, and lifting again on every update is what makes an edit take.
func liftLANAddress(spec *maitrev1.ServiceSpec) {
	if spec.GetNetworkKind() != NetworkLAN {
		return
	}
	if v := strings.TrimSpace(spec.GetSettings()[catalog.LANAddressSetting]); v != "" {
		spec.LanAddress = v
	}
}

// checkLANAddress refuses an address a Service cannot hold on the home
// network: one outside it, one already spoken for, or the router's.
func (m *Manager) checkLANAddress(ctx context.Context, spec *maitrev1.ServiceSpec) error {
	name, addr := spec.GetName(), spec.GetLanAddress()
	if addr == "" {
		return fmt.Errorf("%s needs an address of its own on your home network: a free one outside your router's DHCP pool, which is also the address you give your devices as their DNS server", name)
	}
	lan, err := m.lan()
	if err != nil {
		return err
	}
	ip := net.ParseIP(addr)
	_, subnet, cidrErr := net.ParseCIDR(lan.Subnet)
	if cidrErr != nil {
		return fmt.Errorf("the home network %q is not a subnet: %w", lan.Subnet, cidrErr)
	}
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("%q is not an IPv4 address", addr)
	}
	if !subnet.Contains(ip) {
		return fmt.Errorf("%s is not on your home network %s", addr, lan.Subnet)
	}
	if ip.To4().Equal(net.ParseIP(lan.Address).To4()) {
		return fmt.Errorf("%s is this Node's own address", addr)
	}
	if ip.To4().Equal(net.ParseIP(lan.Gateway).To4()) {
		return fmt.Errorf("%s is your router's address", addr)
	}
	ones, bits := subnet.Mask.Size()
	if ones < bits {
		network := ip.Mask(subnet.Mask).To4()
		broadcast := make(net.IP, 4)
		for i := range network {
			broadcast[i] = network[i] | ^subnet.Mask[i]
		}
		if ip.To4().Equal(network) || ip.To4().Equal(broadcast) {
			return fmt.Errorf("%s is the network's own address, not a host's", addr)
		}
	}
	specs, _ := store.ListLive(ctx, m.store, KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
	for _, s := range specs {
		if s.GetName() != name && s.GetLanAddress() == addr {
			return fmt.Errorf("%s already has the address %s on your home network", s.GetName(), addr)
		}
	}
	return nil
}
