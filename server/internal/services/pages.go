package services

import (
	"context"
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/direct"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// DirectPages is a Service's Pages as the direct package wants them.
func DirectPages(spec *maitrev1.ServiceSpec) []direct.Page {
	var out []direct.Page
	for _, p := range pagesOf(spec) {
		exposure, host := PageExposure(spec, p)
		out = append(out, direct.Page{Name: PageHost(spec, p), Exposure: exposure, PublicHost: host, HostPort: p.GetHostPort()})
	}
	return out
}

// A Service has zero or more Pages (ADR 0010): web UIs the Console opens,
// each on a port of one of its containers. The first Page is the main one,
// reached at <service>.local; the others at <service>-<page>.local. A spec
// from before Pages describes its one page with web_port, web_path and
// web_component; those fields keep mirroring the first Page so code and
// Consoles from before keep working.

var pageNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

// pagesOf is a Service's Pages, with one made from the web_* fields for a
// spec that has none of its own.
func pagesOf(spec *maitrev1.ServiceSpec) []*maitrev1.ServicePage {
	if len(spec.GetPages()) > 0 {
		return spec.GetPages()
	}
	if spec.GetWebPort() == 0 && spec.GetWebHostPort() == 0 {
		return nil
	}
	return []*maitrev1.ServicePage{{Name: "web", Component: spec.GetWebComponent(), Port: spec.GetWebPort(), Path: spec.GetWebPath(), HostPort: spec.GetWebHostPort(), HealthHttp: spec.GetHealthHttp()}}
}

// Pages is pagesOf for other packages.
func Pages(spec *maitrev1.ServiceSpec) []*maitrev1.ServicePage { return pagesOf(spec) }

// normalizePages fills spec.Pages from the web_* fields when it has none,
// and the web_* fields from the first Page, so both views agree.
func normalizePages(spec *maitrev1.ServiceSpec) {
	spec.Pages = pagesOf(spec)
	if len(spec.Pages) == 0 {
		spec.WebPort, spec.WebPath, spec.WebComponent, spec.WebHostPort, spec.HealthHttp = 0, "", "", 0, ""
		return
	}
	p := spec.Pages[0]
	spec.WebPort, spec.WebPath, spec.WebComponent, spec.WebHostPort, spec.HealthHttp = p.GetPort(), p.GetPath(), p.GetComponent(), p.GetHostPort(), p.GetHealthHttp()
}

// PageHost is the host name a Page answers at, without the domain:
// <service> for the first Page, <service>-<page> for the others.
func PageHost(spec *maitrev1.ServiceSpec, page *maitrev1.ServicePage) string {
	pages := pagesOf(spec)
	if len(pages) > 0 && pages[0].GetName() == page.GetName() {
		return spec.GetName()
	}
	return spec.GetName() + "-" + page.GetName()
}

// PageByName finds a Page; "" is the first.
func PageByName(spec *maitrev1.ServiceSpec, name string) *maitrev1.ServicePage {
	pages := pagesOf(spec)
	if name == "" && len(pages) > 0 {
		return pages[0]
	}
	for _, p := range pages {
		if p.GetName() == name {
			return p
		}
	}
	return nil
}

// PageExposure is the level a Page is reachable at: its own, else the
// Service's.
func PageExposure(spec *maitrev1.ServiceSpec, page *maitrev1.ServicePage) (exposure, publicHost string) {
	if page.GetExposure() != "" {
		return page.GetExposure(), page.GetPublicHost()
	}
	return spec.GetExposure(), spec.GetPublicHost()
}

// validatePages checks a Service's Pages: names, ports, the component each
// names, and that nothing in them could break a unit line.
func validatePages(spec *maitrev1.ServiceSpec) error {
	comps := map[string]bool{}
	for _, c := range spec.GetComponents() {
		comps[c.GetName()] = true
	}
	seen := map[string]bool{}
	for _, p := range pagesOf(spec) {
		switch {
		case !pageNameRe.MatchString(p.GetName()):
			return fmt.Errorf("page name %q: use lowercase letters, digits and dashes", p.GetName())
		case seen[p.GetName()]:
			return fmt.Errorf("two pages named %q", p.GetName())
		case p.GetPort() == 0 || p.GetPort() > 65535:
			return fmt.Errorf("page %s: port %d is out of range", p.GetName(), p.GetPort())
		case strings.ContainsAny(p.GetPath()+p.GetHealthHttp()+p.GetLabel()+p.GetPublicHost(), "\n\r\x00"):
			return fmt.Errorf("page %s has a line break", p.GetName())
		case isMulti(spec) && !comps[p.GetComponent()]:
			return fmt.Errorf("page %s: component %q is not a component of %s", p.GetName(), p.GetComponent(), spec.GetName())
		case !isMulti(spec) && p.GetComponent() != "":
			return fmt.Errorf("page %s names a component, and %s has none", p.GetName(), spec.GetName())
		}
		switch p.GetExposure() {
		case "", "private", "remote", "public", "direct":
		default:
			return fmt.Errorf("page %s: exposure must be private, remote, public or direct", p.GetName())
		}
		seen[p.GetName()] = true
	}
	return nil
}

// usedHostPorts is every loopback port the Node's Services hold for their
// Pages, by port.
func (m *Manager) usedHostPorts(ctx context.Context, except string) map[int]string {
	used := map[int]string{}
	specs, _ := store.ListLive(ctx, m.store, KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
	for _, s := range specs {
		if s.GetName() == except {
			continue
		}
		for _, p := range pagesOf(s) {
			if p.GetHostPort() != 0 {
				used[int(p.GetHostPort())] = s.GetName()
			}
		}
	}
	return used
}

// assignHostPorts gives each Page its loopback port on this Node: a stable
// one hashed from the Page's host name, avoiding ports other Services hold.
// On the Node's own network a Page's port is the Node's, which must be free.
func (m *Manager) assignHostPorts(ctx context.Context, spec *maitrev1.ServiceSpec) error {
	used := m.usedHostPorts(ctx, spec.GetName())
	host := onHostNetwork(spec)
	for _, p := range pagesOf(spec) {
		if host {
			if err := m.portFree(ctx, spec.GetName(), p.GetPort()); err != nil {
				return err
			}
			p.HostPort = p.GetPort()
			continue
		}
		if p.GetHostPort() != 0 && used[int(p.GetHostPort())] == "" && p.GetHostPort() != p.GetPort() {
			used[int(p.GetHostPort())] = spec.GetName()
			continue
		}
		h := fnv.New32a()
		h.Write([]byte(PageHost(spec, p)))
		port := m.cfg.HostPortBase + int(h.Sum32()%10000)
		for used[port] != "" {
			port = m.cfg.HostPortBase + (port-m.cfg.HostPortBase+1)%10000
		}
		used[port] = spec.GetName()
		p.HostPort = uint32(port)
	}
	normalizePages(spec)
	return nil
}

// pagesOn are the Pages a component serves.
func pagesOn(spec *maitrev1.ServiceSpec, c comp) []*maitrev1.ServicePage {
	var out []*maitrev1.ServicePage
	for _, p := range pagesOf(spec) {
		if p.GetComponent() == c.Name {
			out = append(out, p)
		}
	}
	return out
}
