package services

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// The shared maitre network has one subnet on every Node of a Fleet (ADR
// 0010), so a Service's address, allocated once and kept on its record,
// means the same Service wherever it runs. Services of the Fleet reach
// each other at <name>.local, which resolves to that address for every
// container: the Node that runs the Service has the container at it; every
// other Node answers at it itself and carries the connection over to the
// running Node's agent (fleetnet).
const (
	// FleetSubnet is the maitre network's subnet on every Node.
	FleetSubnet = "10.90.0.0/16"
	// FleetGateway is the Node's own address on it.
	FleetGateway = "10.90.0.1"
	// FleetDNS is where the Node answers <name>.local for its containers.
	FleetDNS = "10.90.0.2"
	// firstAddress is the first address given to a Service.
	firstAddress = "10.90.0.10"
	// FleetIface is the dummy interface fleetnet holds FleetDNS, and the
	// address of every Service this Node does not run, on. Named here
	// because ensureNetwork has to clear it before Podman will create a
	// network over the same subnet, and fleetnet imports this package.
	FleetIface = "maitre0"
)

// NetworkUnitBody is the maitre.network file the Node writes
// (image/maitre.network before ADR 0010): isolate keeps other networks'
// containers off it; the subnet is the Fleet's; the Node answers names.
func NetworkUnitBody() string {
	return "# Written by Maître. Do not edit.\n" + networkUnitPreamble +
		"[Network]\nNetworkName=maitre\nOptions=isolate=true\nSubnet=" + FleetSubnet + "\nGateway=" + FleetGateway + "\nDNS=" + FleetDNS + "\n"
}

// networkUnitPreamble is what both of the Node's own network units carry
// above their [Network] section. Quadlet gives every unit it generates
// Wants=/After=network-online.target, and a Service ordered before that
// target cannot be after a network that waits for it (see the
// infrastructure branch of renderUnit). Creating a Podman network needs
// the interfaces to exist, not the network to be online, so the
// dependency was never real, and [Quadlet] DefaultDependencies=false is
// the only thing that removes it: an empty After= does not reset an
// ordering list, whatever it does for Wants=.
const networkUnitPreamble = `[Quadlet]
DefaultDependencies=false
[Unit]
Before=network-online.target
`

// Network choices (ADR 0010), ServiceSpec.network_kind.
const (
	// NetworkOwn: the Service's own network, the default.
	NetworkOwn = ""
	// NetworkTunnel: the network of a Tunnel, whose container's network
	// namespace it shares.
	NetworkTunnel = "tunnel"
	// NetworkService: its own plus another Service's network.
	NetworkService = "service"
	// NetworkHost: the Node's own network (the Network=host grant).
	NetworkHost = "host"
	// NetworkLAN: its own plus an address of its own on the home network
	// (decision 327, lan.go), which is what a Service owning port 53 on
	// the LAN needs. Protected, like NetworkHost: a container reachable
	// by every device in the home is not something an entry gets to ask
	// for unasked.
	NetworkLAN = "lan"
)

var portSpecRe = regexp.MustCompile(`^[0-9]{1,5}/(tcp|udp)$`)

// validateNetwork checks a Service's network choice against the Fleet:
// the target exists, runs on this Node, and is the right kind of Service.
func (m *Manager) validateNetwork(ctx context.Context, spec *maitrev1.ServiceSpec) error {
	for _, p := range spec.GetPorts() {
		if !portSpecRe.MatchString(p) {
			return fmt.Errorf("port %q: write it as 5432/tcp or 1194/udp", p)
		}
	}
	switch spec.GetNetworkKind() {
	case NetworkOwn:
		if spec.GetNetworkTarget() != "" {
			return fmt.Errorf("a Service on its own network names no other")
		}
		return nil
	case NetworkHost:
		if isMulti(spec) {
			return fmt.Errorf("%s runs several containers, which cannot share the Node's network", spec.GetName())
		}
		return nil
	case NetworkLAN:
		if spec.GetNetworkTarget() != "" {
			return fmt.Errorf("a Service with an address of its own on the home network names no other")
		}
		if isMulti(spec) {
			return fmt.Errorf("%s runs several containers; an address on the home network goes to one container, so there is no saying which", spec.GetName())
		}
		return m.checkLANAddress(ctx, spec)
	case NetworkTunnel, NetworkService:
	default:
		return fmt.Errorf("network must be the Service's own, a Tunnel's, another Service's or the Node's")
	}
	target := spec.GetNetworkTarget()
	if target == spec.GetName() || target == "" {
		return fmt.Errorf("%s needs another Service to join", spec.GetName())
	}
	var t maitrev1.ServiceSpec
	if _, err := m.store.Get(ctx, KindService, target, &t); err != nil {
		return fmt.Errorf("there is no Service named %s", target)
	}
	if t.GetNodeId() != "" && spec.GetNodeId() != "" && t.GetNodeId() != spec.GetNodeId() {
		return fmt.Errorf("%s runs on another Node; a Service joins only a Service of its own Node", target)
	}
	if t.GetNetworkKind() == NetworkTunnel || t.GetNetworkKind() == NetworkHost {
		return fmt.Errorf("%s is itself on %s's network; join that one", target, t.GetNetworkTarget())
	}
	switch spec.GetNetworkKind() {
	case NetworkTunnel:
		if !t.GetTunnel() {
			return fmt.Errorf("%s is not a Tunnel", target)
		}
		if isMulti(spec) {
			return fmt.Errorf("%s runs several containers, which cannot share a Tunnel yet", spec.GetName())
		}
		if spec.GetTunnel() {
			return fmt.Errorf("a Tunnel cannot go through another Tunnel")
		}
	case NetworkService:
		if !isMulti(&t) {
			return fmt.Errorf("%s has no network of its own: it is already reachable as %s on the shared network", target, target)
		}
	}
	return nil
}

// dependants are the live Services that join name's network, Tunnel or
// not, on this Node.
func (m *Manager) dependants(ctx context.Context, name string) []*maitrev1.ServiceSpec {
	specs, _ := m.mine(ctx)
	var out []*maitrev1.ServiceSpec
	for _, s := range specs {
		if s.GetNetworkTarget() == name && s.GetName() != name && (s.GetNetworkKind() == NetworkTunnel || s.GetNetworkKind() == NetworkService) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out
}

// Dependants is dependants for other packages, by name.
func (m *Manager) Dependants(ctx context.Context, name string) []string {
	var out []string
	for _, d := range m.dependants(ctx, name) {
		out = append(out, d.GetName())
	}
	return out
}

// onHostNetwork reports whether a Service runs on the Node's own network:
// chosen in Setup, or said by a single container's template (Network=host,
// a granted option, G4).
func onHostNetwork(spec *maitrev1.ServiceSpec) bool {
	if spec.GetNetworkKind() == NetworkHost {
		return true
	}
	if spec.GetNetworkKind() != NetworkOwn || isMulti(spec) {
		return false
	}
	host, err := hostNetworked(spec)
	return err == nil && host
}

// tunnelContainer is the container name of a Tunnel, which a Service that
// joins it names in Network=container:<name>. The agent sets
// ContainerName on a Tunnel, so it is the Service's name.
func tunnelContainer(name string) string { return name }

// AllocateAddress gives a Service the lowest free address of the Fleet's
// subnet, unique across every Node's Services.
func (m *Manager) allocateAddress(ctx context.Context) (string, error) {
	used := map[string]bool{}
	specs, _ := store.ListLive(ctx, m.store, KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
	for _, s := range specs {
		used[s.GetAddress()] = true
	}
	_, subnet, _ := net.ParseCIDR(FleetSubnet)
	start := net.ParseIP(firstAddress).To4()
	ip := binary.BigEndian.Uint32(start)
	for ; ; ip++ {
		b := make(net.IP, 4)
		binary.BigEndian.PutUint32(b, ip)
		if !subnet.Contains(b) {
			return "", fmt.Errorf("the Fleet's network %s has no address left", FleetSubnet)
		}
		if b[3] == 0 || b[3] == 255 {
			continue
		}
		if !used[b.String()] {
			return b.String(), nil
		}
	}
}

// checkAddress refuses an address outside the Fleet's subnet, one the
// Node keeps for itself, or one another Service holds.
func (m *Manager) checkAddress(ctx context.Context, name, addr string) error {
	ip := net.ParseIP(addr)
	_, subnet, _ := net.ParseCIDR(FleetSubnet)
	if ip == nil || ip.To4() == nil || !subnet.Contains(ip) {
		return fmt.Errorf("%q is not an address of the Fleet's network %s", addr, FleetSubnet)
	}
	first := net.ParseIP(firstAddress).To4()
	if binary.BigEndian.Uint32(ip.To4()) < binary.BigEndian.Uint32(first) || ip.To4()[3] == 0 || ip.To4()[3] == 255 {
		return fmt.Errorf("addresses below %s are the Node's own", firstAddress)
	}
	specs, _ := store.ListLive(ctx, m.store, KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
	for _, s := range specs {
		if s.GetName() != name && s.GetAddress() == addr {
			return fmt.Errorf("%s already has the address %s", s.GetName(), addr)
		}
	}
	return nil
}

// netPlan is what the renderer needs to know about a Service's place in
// the Fleet's network, worked out once per render.
type netPlan struct {
	kind   string
	target string
	// publish are PublishPort lines a Tunnel carries for the Services that
	// join it, and wants their units, so they start with it.
	publish []string
	wants   []string
}

// planNetwork builds the netPlan from the store: for a Tunnel, the
// Services on it and their ports.
func (m *Manager) planNetwork(ctx context.Context, spec *maitrev1.ServiceSpec) netPlan {
	plan := netPlan{kind: spec.GetNetworkKind(), target: spec.GetNetworkTarget()}
	if !spec.GetTunnel() || m.store == nil {
		return plan
	}
	for _, d := range m.dependants(ctx, spec.GetName()) {
		if d.GetNetworkKind() != NetworkTunnel {
			continue
		}
		plan.wants = append(plan.wants, d.GetName()+".service")
		for _, p := range pagesOf(d) {
			if p.GetHostPort() != 0 {
				plan.publish = append(plan.publish, fmt.Sprintf("PublishPort=127.0.0.1:%d:%d", p.GetHostPort(), p.GetPort()))
			}
		}
		// The template's own published ports (a torrent client's listening
		// port) are the Tunnel's to publish, since the Service has no
		// network of its own.
		plan.publish = append(plan.publish, templatePublishes(d)...)
	}
	return plan
}

// templatePublishes are the PublishPort lines a single-container
// Service's template writes, rendered with placeholder volumes.
func templatePublishes(spec *maitrev1.ServiceSpec) []string {
	vols := map[string]string{}
	for _, v := range spec.GetVolumes() {
		vols[v.GetName()] = "/maitre-volume"
	}
	body, err := renderBody(spec, vols)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(body, "\n") {
		if k, v, ok := keyValue(l); ok && k == "PublishPort" {
			out = append(out, "PublishPort="+strings.TrimSpace(v))
		}
	}
	return out
}

// HostNetworked is onHostNetwork for other packages.
func HostNetworked(spec *maitrev1.ServiceSpec) bool { return onHostNetwork(spec) }

// Migrate brings a Node's Services to ADR 0010 at start: the shared
// network gets the Fleet's subnet (recreated if it had another, with the
// Node's Services stopped meanwhile), every Service of this Node without
// an address gets one, and every unit whose render changed is written
// again and its Service restarted if it ran. Also what a later version
// needs, each time: a Service rendered by an older agent is brought up to
// date here.
func (m *Manager) Migrate(ctx context.Context) {
	if err := m.ensureNetwork(ctx); err != nil {
		m.logf("fleet network: %v", err)
	}
	specs, err := store.ListLive(ctx, m.store, KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
	if err != nil {
		return
	}
	// The home network alongside the shared one (decision 327), and for
	// the same reason: its unit is written from the Node and from this
	// agent's idea of what a network unit says, so a Node whose unit was
	// written by an older agent is brought up to date here rather than at
	// the next install. Only when a Service of this Node is on it, since
	// a Node with none has no business holding a macvlan.
	for _, spec := range specs {
		if spec.GetNodeId() == m.store.NodeID() && spec.GetNetworkKind() == NetworkLAN {
			if err := m.ensureLANNetwork(ctx); err != nil {
				m.logf("home network: %v", err)
			}
			break
		}
	}
	for _, spec := range specs {
		if spec.GetNodeId() != m.store.NodeID() {
			continue
		}
		changed := false
		if len(spec.GetPages()) == 0 && spec.GetWebPort() != 0 {
			normalizePages(spec)
			changed = true
		}
		if spec.GetAddress() == "" {
			addr, err := m.allocateAddress(ctx)
			if err != nil {
				m.logf("%s: address: %v", spec.GetName(), err)
				continue
			}
			spec.Address = addr
			changed = true
		}
		if changed {
			if _, err := m.store.Put(ctx, KindService, spec.GetName(), spec, false); err != nil {
				m.logf("%s: %v", spec.GetName(), err)
				continue
			}
		}
		if !m.unitsStale(ctx, spec) {
			continue
		}
		if err := m.apply(ctx, spec); err != nil {
			m.logf("%s: bringing its units up to date: %v", spec.GetName(), err)
			continue
		}
		m.logf("%s: units written again for this version", spec.GetName())
		_, _ = m.run.Run(ctx, "systemctl", "try-restart", "--no-block", lifecycleUnit(spec))
	}
}

// unitsStale reports whether a Service's units on disk differ from what
// this agent renders.
func (m *Manager) unitsStale(ctx context.Context, spec *maitrev1.ServiceSpec) bool {
	vols, extra, err := m.layout(ctx, spec)
	if err != nil {
		return false
	}
	set, err := m.renderUnits(ctx, spec, vols, extra)
	if err != nil {
		return false
	}
	for name, body := range set {
		cur, err := os.ReadFile(m.unitPath(name))
		if err != nil || string(cur) != body {
			return true
		}
	}
	return false
}

// ensureNetwork makes Podman's maitre network the Fleet's subnet, creating
// it when it is not there. What it reconciles against is Podman's live
// network, never the unit file: image/Containerfile ships
// /etc/containers/systemd/maitre.network with exactly the bytes
// NetworkUnitBody writes, so a file comparison always matches and every
// repair below it would be dead code on a shipped image.
//
// The order matters and is the whole fix. fleetnet holds FleetDNS, and
// every Service this Node does not run, as /32s on its dummy interface,
// and those addresses are inside FleetSubnet by design — a Service keeps
// one address across the Fleet. Podman refuses to create a network whose
// subnet is already on the host ("subnet ... is already used on the host
// or by another config"), and FleetDNS alone is enough to trigger it, so
// the bridge could never be created on any real Node. Podman only checks
// at create time, so the addresses come off first and fleetnet puts them
// back on its next Reconcile; a /32 beside the bridge's /16 then routes
// to the agent, which is what ADR 0010 wants.
//
// A network Podman already made with another subnet cannot change under
// its containers: the Node's Services stop, the network goes, and they
// start again on the new one.
func (m *Manager) ensureNetwork(ctx context.Context) error {
	path := filepath.Join(m.cfg.UnitsDir, Network)
	want := NetworkUnitBody()
	writeUnit := func() error {
		if cur, err := os.ReadFile(path); err == nil && string(cur) == want {
			return nil
		}
		if err := os.MkdirAll(m.cfg.UnitsDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			return err
		}
		_, err := m.run.Run(ctx, "systemctl", "daemon-reload")
		return err
	}

	out, err := m.run.Run(ctx, "podman", "network", "inspect", "maitre", "--format", "{{range .Subnets}}{{.Subnet}} {{end}}")
	if err == nil && strings.Contains(out, FleetSubnet) {
		// Already the Fleet's; keep the unit file in step and leave the
		// running containers alone.
		return writeUnit()
	}
	exists := err == nil && strings.TrimSpace(out) != ""

	var mine []*maitrev1.ServiceSpec
	specs, _ := store.ListLive(ctx, m.store, KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
	for _, s := range specs {
		if s.GetNodeId() == m.store.NodeID() {
			mine = append(mine, s)
		}
	}
	if exists {
		m.logf("the shared network moves to %s; this Node's %d Services restart", FleetSubnet, len(mine))
	} else {
		m.logf("the shared network %s is not there; creating it, and this Node's %d Services restart", FleetSubnet, len(mine))
	}
	for _, s := range mine {
		_, _ = m.run.Run(ctx, "systemctl", "stop", lifecycleUnit(s))
	}
	defer func() {
		for _, s := range mine {
			_, _ = m.run.Run(ctx, "systemctl", "start", "--no-block", lifecycleUnit(s))
		}
	}()
	if exists {
		if _, err := m.run.Run(ctx, "podman", "network", "rm", "-f", "maitre"); err != nil {
			return fmt.Errorf("remove the old network: %w", err)
		}
	}
	// The addresses fleetnet holds inside FleetSubnet are what Podman
	// objects to. Taking the interface down drops all of them at once;
	// fleetnet's next Reconcile puts back the ones it still wants.
	_, _ = m.run.Run(ctx, "ip", "link", "del", FleetIface)
	if err := writeUnit(); err != nil {
		return err
	}
	// Created here rather than left to the first Service that wants it: a
	// failure belongs in this Node's log next to the reason, not in
	// fourteen Services reporting "Dependency failed".
	//
	// restart, never start: maitre-network.service is a oneshot, and a
	// Service of this Node may already have driven it to "active
	// (exited)" earlier in the boot. `systemctl start` on a oneshot that
	// has already run is a no-op that reports success, so the network
	// would stay removed and every Service would then fail with "unable
	// to find network with name or ID maitre". Avalon did exactly that.
	if _, err := m.run.Run(ctx, "systemctl", "restart", "maitre-network.service"); err != nil {
		return fmt.Errorf("create the shared network %s: %w", FleetSubnet, err)
	}
	// Say what Podman ended up with rather than assume. Removing a
	// network and not replacing it is worse than leaving a wrong one
	// alone, so this is the check that must not be skipped, and its
	// message carries the subnets Podman reports so the next person does
	// not have to guess at them.
	out, err = m.run.Run(ctx, "podman", "network", "inspect", "maitre", "--format", "{{range .Subnets}}{{.Subnet}} {{end}}")
	if err != nil {
		return fmt.Errorf("the shared network was not there after creating it: %w", err)
	}
	if !strings.Contains(out, FleetSubnet) {
		return fmt.Errorf("the shared network came back on %q, not %s", strings.TrimSpace(out), FleetSubnet)
	}
	return nil
}
