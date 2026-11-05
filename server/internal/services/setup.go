package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// Setup (ADR 0010) is the part of a Service a person changes beyond its
// install form: its network choice, reach from the Fleet's other Nodes,
// fixed address, Pages and container-option overrides. The Console shows
// the rendered units first (PreviewSetup), then applies (SetSetup).

// Preview is what PreviewSetup answers.
type Preview struct {
	Units     map[string]unit
	Protected []ProtectedOption
	Restarts  []string
}

// Names are the preview's unit names, containers first, then the rest
// alphabetically.
func (p *Preview) Names() []string {
	var out []string
	for name := range p.Units {
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool {
		ci, cj := strings.HasSuffix(out[i], ".container"), strings.HasSuffix(out[j], ".container")
		if ci != cj {
			return ci
		}
		return out[i] < out[j]
	})
	return out
}

// withSetup is spec with setup's fields replaced, the rest untouched.
func withSetup(spec *maitrev1.ServiceSpec, setup *maitrev1.Setup) *maitrev1.ServiceSpec {
	out := proto.Clone(spec).(*maitrev1.ServiceSpec)
	if setup == nil {
		return out
	}
	out.NetworkKind, out.NetworkTarget = setup.GetNetworkKind(), setup.GetNetworkTarget()
	out.FleetReach, out.Ports = setup.GetFleetReach(), append([]string(nil), setup.GetPorts()...)
	out.Address = setup.GetAddress()
	out.NetworkInternal = setup.GetNetworkInternal()
	out.Overrides = nil
	for _, o := range setup.GetOverrides() {
		out.Overrides = append(out.Overrides, proto.Clone(o).(*maitrev1.UnitOverride))
	}
	// Pages keep the loopback ports they had; new ones get theirs below.
	had := map[string]uint32{}
	for _, p := range pagesOf(spec) {
		had[p.GetName()] = p.GetHostPort()
	}
	out.Pages = nil
	for _, p := range setup.GetPages() {
		np := proto.Clone(p).(*maitrev1.ServicePage)
		np.HostPort = had[np.GetName()]
		out.Pages = append(out.Pages, np)
	}
	normalizePages(out)
	return out
}

// checkSetup validates a Service with a Setup applied, on this Node, and
// gives its Pages their loopback ports.
func (m *Manager) checkSetup(ctx context.Context, spec *maitrev1.ServiceSpec) error {
	if err := validatePages(spec); err != nil {
		return err
	}
	if err := validateOverrides(spec); err != nil {
		return err
	}
	if err := m.validateNetwork(ctx, spec); err != nil {
		return err
	}
	if spec.GetAddress() == "" {
		addr, err := m.allocateAddress(ctx)
		if err != nil {
			return err
		}
		spec.Address = addr
	} else if err := m.checkAddress(ctx, spec.GetName(), spec.GetAddress()); err != nil {
		return err
	}
	if spec.GetNetworkInternal() && !isMulti(spec) {
		return fmt.Errorf("%s has no network of its own to make internal", spec.GetName())
	}
	for _, p := range spec.GetPages() {
		if p.GetExposure() != "" {
			if _, err := m.checkExposure(ctx, spec.GetName(), spec, p.GetExposure(), p.GetPublicHost()); err != nil {
				return fmt.Errorf("page %s: %w", p.GetName(), err)
			}
		}
	}
	return m.assignHostPorts(ctx, spec)
}

// restartsFor lists the Services that restart when spec replaces cur:
// itself, the Tunnel it joins or leaves, and the Services on those.
func (m *Manager) restartsFor(ctx context.Context, cur, spec *maitrev1.ServiceSpec) []string {
	seen := map[string]bool{spec.GetName(): true}
	out := []string{spec.GetName()}
	for _, t := range []string{cur.GetNetworkTarget(), spec.GetNetworkTarget()} {
		if t == "" || seen[t] {
			continue
		}
		var ts maitrev1.ServiceSpec
		if _, err := m.store.Get(ctx, KindService, t, &ts); err != nil || !ts.GetTunnel() {
			continue
		}
		seen[t] = true
		out = append(out, t)
		for _, d := range m.dependants(ctx, t) {
			if d.GetNetworkKind() == NetworkTunnel && !seen[d.GetName()] {
				seen[d.GetName()] = true
				out = append(out, d.GetName())
			}
		}
	}
	if spec.GetTunnel() {
		for _, d := range m.dependants(ctx, spec.GetName()) {
			if d.GetNetworkKind() == NetworkTunnel && !seen[d.GetName()] {
				seen[d.GetName()] = true
				out = append(out, d.GetName())
			}
		}
	}
	sort.Strings(out[1:])
	return out
}

// PreviewSetup renders a Service as SetSetup would leave it, without
// changing anything; a nil setup renders it as it is.
func (m *Manager) PreviewSetup(ctx context.Context, name string, setup *maitrev1.Setup) (*Preview, error) {
	var cur maitrev1.ServiceSpec
	if _, err := m.store.Get(ctx, KindService, name, &cur); err != nil {
		return nil, ErrNotFound
	}
	spec := withSetup(&cur, setup)
	if setup != nil {
		if err := m.checkSetup(ctx, spec); err != nil {
			return nil, err
		}
	}
	vols, extra, err := m.layout(ctx, spec)
	if err != nil {
		return nil, err
	}
	out := &Preview{}
	if out.Protected, err = protectedOptions(spec, vols, extra, m.planNetwork(ctx, spec), managedDirs(vols, extra)); err != nil {
		return nil, err
	}
	if out.Units, err = m.renderAnnotated(ctx, spec, vols, extra); err != nil {
		return nil, err
	}
	if setup != nil {
		out.Restarts = m.restartsFor(ctx, &cur, spec)
	}
	return out, nil
}

// SetSetup replaces a Service's Setup: saved, applied, and the Service
// restarted with the Tunnel it joins or leaves.
func (m *Manager) SetSetup(ctx context.Context, name string, setup *maitrev1.Setup) (*maitrev1.ServiceStatus, error) {
	if setup == nil {
		return nil, errors.New("no setup")
	}
	var cur maitrev1.ServiceSpec
	if _, err := m.store.Get(ctx, KindService, name, &cur); err != nil {
		return nil, ErrNotFound
	}
	if cur.GetNodeId() != "" && cur.GetNodeId() != m.store.NodeID() {
		return nil, fmt.Errorf("%s runs on another Node; ask that one", name)
	}
	spec := withSetup(&cur, setup)
	if err := m.checkSetup(ctx, spec); err != nil {
		return nil, err
	}
	if cur.GetTunnel() && spec.GetNetworkKind() != NetworkOwn {
		return nil, fmt.Errorf("%s is a Tunnel; it stays on its own network", name)
	}
	if err := m.apply(ctx, spec); err != nil {
		return nil, err
	}
	if _, err := m.store.Put(ctx, KindService, name, spec, false); err != nil {
		return nil, err
	}
	// The Tunnels it leaves and joins publish for it no longer and from
	// now on; restarting a Tunnel restarts the Services on it.
	for _, t := range []string{cur.GetNetworkTarget(), spec.GetNetworkTarget()} {
		if t == "" {
			continue
		}
		if err := m.reapplyTunnel(ctx, t); err != nil {
			return nil, err
		}
	}
	if _, err := m.run.Run(ctx, "systemctl", "try-restart", "--no-block", lifecycleUnit(spec)); err != nil {
		return nil, err
	}
	if m.OnExposure != nil {
		m.OnExposure()
	}
	return m.Status(ctx, spec)
}

// reapplyTunnel writes a Tunnel's unit again, with the Services now on it,
// and restarts it if it runs, which restarts them (BindsTo, Wants). Not a
// Tunnel, or not here: nothing to do.
func (m *Manager) reapplyTunnel(ctx context.Context, name string) error {
	var t maitrev1.ServiceSpec
	if _, err := m.store.Get(ctx, KindService, name, &t); err != nil || !t.GetTunnel() || t.GetNodeId() != m.store.NodeID() {
		return nil
	}
	if err := m.apply(ctx, &t); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if _, err := m.run.Run(ctx, "systemctl", "try-restart", "--no-block", lifecycleUnit(&t)); err != nil {
		return fmt.Errorf("restart %s: %w", name, err)
	}
	return nil
}

// SetPageExposure changes where one Page is reachable; page "" changes
// the Service's own level, which every Page without one follows.
func (m *Manager) SetPageExposure(ctx context.Context, name, page, exposure, publicHost string) (*maitrev1.ServiceStatus, error) {
	if page == "" {
		return m.SetExposure(ctx, name, exposure, publicHost)
	}
	var spec maitrev1.ServiceSpec
	if _, err := m.store.Get(ctx, KindService, name, &spec); err != nil {
		return nil, ErrNotFound
	}
	normalizePages(&spec)
	p := PageByName(&spec, page)
	if p == nil {
		return nil, fmt.Errorf("%s has no Page %q", name, page)
	}
	host, err := m.checkExposure(ctx, name, &spec, exposure, publicHost)
	if err != nil {
		return nil, err
	}
	p.Exposure, p.PublicHost = exposure, host
	if _, err := m.store.Put(ctx, KindService, name, &spec, false); err != nil {
		return nil, err
	}
	if m.OnExposure != nil {
		m.OnExposure()
	}
	return m.Status(ctx, &spec)
}
