package services

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/quadlet/parser"
)

// comp is the effective view of one container of a Service. A
// single-container Service (no components) is one comp with an empty Name
// describing the whole spec, so the rest of the package can treat both
// shapes the same way where it helps.
type comp struct {
	Name        string
	Image       string
	PrevImage   string
	Quadlet     string
	After       []string
	WaitHealthy bool
	Egress      bool
	Web         bool
}

// components returns a Service's containers (plan C1).
func components(spec *maitrev1.ServiceSpec) []comp {
	if !isMulti(spec) {
		return []comp{{Name: "", Image: spec.GetImage(), PrevImage: spec.GetPreviousImage(), Quadlet: spec.GetQuadlet(), Web: true}}
	}
	out := make([]comp, 0, len(spec.GetComponents()))
	for _, c := range spec.GetComponents() {
		out = append(out, comp{
			Name: c.GetName(), Image: c.GetImage(), PrevImage: c.GetPreviousImage(), Quadlet: c.GetQuadlet(),
			After: c.GetAfter(), WaitHealthy: c.GetWaitHealthy(), Egress: c.GetEgress(),
			Web: c.GetName() == spec.GetWebComponent(),
		})
	}
	return out
}

// isMulti reports whether a Service has explicit components.
func isMulti(spec *maitrev1.ServiceSpec) bool { return len(spec.GetComponents()) > 0 }

// unitBase is the container and unit name for a component: <service> for a
// single-container Service, <service>-<component> otherwise.
func unitBase(serviceName string, c comp) string {
	if c.Name == "" {
		return serviceName
	}
	return serviceName + "-" + c.Name
}

// lifecycleUnit is the systemd unit the Node starts, stops and restarts: the
// grouping target for a multi-container Service, the one .service otherwise.
func lifecycleUnit(spec *maitrev1.ServiceSpec) string {
	if isMulti(spec) {
		return spec.GetName() + ".target"
	}
	return spec.GetName() + ".service"
}

// serviceNetworkUnit is the .network unit a Service's components share.
func serviceNetworkUnit(name string) string { return "maitre-" + name + ".network" }

// egressNetworkUnit is the extra network a route-out component joins when the
// Service network is internal (plan C2).
func egressNetworkUnit(name string) string { return "maitre-" + name + "-egress.network" }

// componentServiceUnits lists the .service units of a multi-container
// Service's components, in spec order.
func componentServiceUnits(spec *maitrev1.ServiceSpec) []string {
	var out []string
	for _, c := range components(spec) {
		out = append(out, unitBase(spec.GetName(), c)+".service")
	}
	return out
}

// unitFiles lists every file the Node writes for a Service, so remove can
// take them all away: each component container, the Service's networks, the
// bundle's extra units, and the grouping target.
func unitFiles(spec *maitrev1.ServiceSpec) []string {
	if !isMulti(spec) {
		return []string{spec.GetName() + ".container"}
	}
	var out []string
	for _, c := range components(spec) {
		out = append(out, unitBase(spec.GetName(), c)+".container")
	}
	out = append(out, serviceNetworkUnit(spec.GetName()))
	if needsEgress(spec) {
		out = append(out, egressNetworkUnit(spec.GetName()))
	}
	for _, u := range spec.GetUnits() {
		out = append(out, spec.GetName()+"-"+u.GetName())
	}
	out = append(out, spec.GetName()+".target")
	return out
}

// needsEgress reports whether the Service network is internal and at least
// one component needs a route out.
func needsEgress(spec *maitrev1.ServiceSpec) bool {
	if !spec.GetNetworkInternal() {
		return false
	}
	for _, c := range spec.GetComponents() {
		if c.GetEgress() {
			return true
		}
	}
	return false
}

// renderMulti renders every unit file of a multi-container Service: one per
// component, the Service network (and egress network when internal), the
// bundle's own units, and the grouping target. It returns a map of file name
// to contents, the same shape quadlet.DryRun takes.
func (m *Manager) renderMulti(spec *maitrev1.ServiceSpec, vols map[string]string, extra Extra) (map[string]string, error) {
	set := map[string]string{}
	set[serviceNetworkUnit(spec.GetName())] = renderServiceNetwork(spec)
	if needsEgress(spec) {
		set[egressNetworkUnit(spec.GetName())] = egressNetworkBody
	}
	for _, u := range spec.GetUnits() {
		set[spec.GetName()+"-"+u.GetName()] = u.GetBody()
	}
	for _, c := range components(spec) {
		body, err := renderComponentUnit(spec, c, vols, extra)
		if err != nil {
			return nil, fmt.Errorf("component %s: %w", c.Name, err)
		}
		set[unitBase(spec.GetName(), c)+".container"] = body
	}
	set[spec.GetName()+".target"] = renderTarget(spec)
	return set, nil
}

// renderServiceNetwork is the .network every component joins. isolate keeps
// containers of other Services off it; Internal cuts its route out (plan C2).
func renderServiceNetwork(spec *maitrev1.ServiceSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by Maître for Service %q. Do not edit.\n", spec.GetName())
	fmt.Fprintf(&b, "[Network]\nNetworkName=maitre-%s\nOptions=isolate=true\n", spec.GetName())
	if spec.GetNetworkInternal() {
		b.WriteString("Internal=true\n")
	}
	return b.String()
}

// egressNetworkBody is the route-out network for egress components of an
// internal Service.
const egressNetworkBody = "[Network]\nNetworkName=maitre-egress\n"

// renderTarget groups a multi-container Service's components so one
// systemctl start/stop/restart acts on the whole Service.
func renderTarget(spec *maitrev1.ServiceSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by Maître for Service %q. Do not edit.\n", spec.GetName())
	fmt.Fprintf(&b, "[Unit]\nDescription=Maître Service %s\n", spec.GetName())
	b.WriteString("[Install]\nWantedBy=multi-user.target default.target\n")
	return b.String()
}

// renderComponentUnit renders one container's .container file. The component
// template is a plain [Container] body (no Maître network); the agent adds
// the hostname, networks, publish port, labels, ordering and confinement.
func renderComponentUnit(spec *maitrev1.ServiceSpec, c comp, vols map[string]string, extra Extra) (string, error) {
	rendered, err := renderComponentBody(spec, c, vols)
	if err != nil {
		return "", err
	}
	if c.Web && len(extra.Shared) > 0 {
		hosts := map[string]bool{}
		for name := range extra.Shared {
			hosts[vols[name]] = true
		}
		rendered = shareVolumes(rendered, hosts)
	}
	parsed := parser.NewUnitFile()
	if err := parsed.Parse(rendered); err != nil {
		return "", fmt.Errorf("quadlet unit: %w", err)
	}
	container, rest := splitContainer(rendered)
	var out bytes.Buffer
	fmt.Fprintf(&out, "# Generated by Maître for Service %q component %q. Do not edit.\n", spec.GetName(), c.Name)

	// [Unit]: part of the Service's target, ordered after its dependencies.
	fmt.Fprintf(&out, "[Unit]\nDescription=Maître Service %s (%s)\n", spec.GetName(), c.Name)
	fmt.Fprintf(&out, "PartOf=%s.target\n", spec.GetName())
	for _, dep := range c.After {
		depUnit := spec.GetName() + "-" + dep + ".service"
		fmt.Fprintf(&out, "Requires=%s\nAfter=%s\n", depUnit, depUnit)
	}
	out.WriteString("\n")

	// [Container]: the template body, then Maître's own keys.
	out.WriteString(container)
	if !strings.HasSuffix(container, "\n") {
		out.WriteString("\n")
	}
	if !parsed.HasKey("Container", "NoNewPrivileges") {
		out.WriteString("NoNewPrivileges=true\n")
	}
	if !parsed.HasKey("Container", "HostName") && !parsed.HasKey("Container", "Pod") && !lookupIs(parsed, "Network", "host") && !lookupIs(parsed, "UTS", "host") {
		fmt.Fprintf(&out, "HostName=%s\n", c.Name)
	}
	// Networks: the Service's own, under this component's name; the web
	// component also on maitre.network under the Service's name; a route out
	// for egress components of an internal Service.
	fmt.Fprintf(&out, "Network=%s:alias=%s\n", serviceNetworkUnit(spec.GetName()), c.Name)
	if c.Web {
		fmt.Fprintf(&out, "Network=%s:alias=%s\n", Network, spec.GetName())
	}
	if c.Egress && needsEgress(spec) {
		fmt.Fprintf(&out, "Network=%s\n", egressNetworkUnit(spec.GetName()))
	}
	if c.WaitHealthy && !parsed.HasKey("Container", "Notify") {
		out.WriteString("Notify=healthy\n")
	}
	if c.Web {
		if spec.GetWebPort() != 0 && spec.GetWebHostPort() != 0 {
			fmt.Fprintf(&out, "PublishPort=127.0.0.1:%d:%d\n", spec.GetWebHostPort(), spec.GetWebPort())
		}
		for _, mt := range extra.Mounts {
			opts := "z"
			if mt.ReadOnly {
				opts += ",ro"
			}
			fmt.Fprintf(&out, "Volume=%s:%s:%s\n", mt.Host, mt.Path, opts)
		}
		for _, g := range extra.Groups {
			fmt.Fprintf(&out, "GroupAdd=%d\n", g)
		}
	}
	out.WriteString("Label=dev.maitre.service=" + spec.GetName() + "\n")
	out.WriteString("Label=dev.maitre.component=" + c.Name + "\n")
	if rest != "" {
		out.WriteString("\n" + rest)
		if !strings.HasSuffix(rest, "\n") {
			out.WriteString("\n")
		}
	}

	fmt.Fprintf(&out, "\n[Service]\nRestart=on-failure\nRestartSec=5\nTimeoutStartSec=900\n")
	fmt.Fprintf(&out, "\n[Install]\nWantedBy=%s.target\n", spec.GetName())
	return out.String(), nil
}

// validateContainers checks a Service's container definitions: a
// single-container Service needs an image and a quadlet template; a
// multi-container one needs well-formed components, a web_component that
// names one of them (or none), and every template to parse. Decision 189:
// images must not carry line breaks, since they land in unit lines.
func validateContainers(spec *maitrev1.ServiceSpec) error {
	if !isMulti(spec) {
		if spec.GetImage() == "" {
			return errors.New("service spec has no image")
		}
		if strings.ContainsAny(spec.GetImage(), "\n\r") {
			return errors.New("the image name has a line break")
		}
		if strings.TrimSpace(spec.GetQuadlet()) == "" {
			return errors.New("service spec has no quadlet")
		}
		if _, err := template.New(spec.GetName()).Option("missingkey=error").Parse(spec.GetQuadlet()); err != nil {
			return fmt.Errorf("quadlet template: %w", err)
		}
		if len(spec.GetUnits()) > 0 {
			return errors.New("bundle units need a Service of components")
		}
		return nil
	}
	seen := map[string]bool{}
	for _, c := range spec.GetComponents() {
		if !plainName(c.GetName()) || c.GetName() == "" {
			return fmt.Errorf("component name %q: use letters, digits and dashes", c.GetName())
		}
		if seen[c.GetName()] {
			return fmt.Errorf("two components named %q", c.GetName())
		}
		seen[c.GetName()] = true
		if c.GetImage() == "" {
			return fmt.Errorf("component %s has no image", c.GetName())
		}
		if strings.ContainsAny(c.GetImage(), "\n\r") {
			return fmt.Errorf("component %s: the image name has a line break", c.GetName())
		}
		if strings.TrimSpace(c.GetQuadlet()) == "" {
			return fmt.Errorf("component %s has no quadlet", c.GetName())
		}
		if _, err := template.New(c.GetName()).Option("missingkey=error").Parse(c.GetQuadlet()); err != nil {
			return fmt.Errorf("component %s: quadlet template: %w", c.GetName(), err)
		}
	}
	for _, c := range spec.GetComponents() {
		for _, dep := range c.GetAfter() {
			if !seen[dep] {
				return fmt.Errorf("component %s waits for %q, which is not a component", c.GetName(), dep)
			}
		}
	}
	units := map[string]bool{}
	for _, u := range spec.GetUnits() {
		if !bundleUnitRe.MatchString(u.GetName()) {
			return fmt.Errorf("bundle unit %q: name it <name>.volume or <name>.network", u.GetName())
		}
		if units[u.GetName()] {
			return fmt.Errorf("two bundle units named %q", u.GetName())
		}
		units[u.GetName()] = true
	}
	if wc := spec.GetWebComponent(); wc != "" && !seen[wc] {
		return fmt.Errorf("web_component %q is not a component", wc)
	}
	if spec.GetWebPort() != 0 && spec.GetWebComponent() == "" {
		return errors.New("a multi-container Service with a web page needs web_component set")
	}
	return nil
}

// renderComponentBody executes one component's [Container] template, with
// its references to the bundle's own units renamed as they are written.
func renderComponentBody(spec *maitrev1.ServiceSpec, c comp, vols map[string]string) (string, error) {
	body, err := renderTemplate(unitBase(spec.GetName(), c), c.Quadlet, c.Image, spec, vols)
	if err != nil {
		return "", err
	}
	return bundleRefs(body, spec), nil
}

// bundleUnitRe is a bundle's own unit: a .volume or .network file name.
var bundleUnitRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*\.(volume|network)$`)

// bundleRefs renames a container's references to the bundle's units
// (Volume=cache.volume:/data, Network=backend.network,
// Mount=type=volume,source=cache.volume,...) to the files the Node writes,
// <service>-<name>, in the [Container] group only (plan C1).
func bundleRefs(body string, spec *maitrev1.ServiceSpec) string {
	if len(spec.GetUnits()) == 0 {
		return body
	}
	names := map[string]string{}
	for _, u := range spec.GetUnits() {
		names[u.GetName()] = spec.GetName() + "-" + u.GetName()
	}
	// A reference is the whole value or its first colon-separated field.
	first := func(v string) string {
		head, rest, found := strings.Cut(v, ":")
		if to, ok := names[head]; ok {
			if found {
				return to + ":" + rest
			}
			return to
		}
		return v
	}
	lines := strings.Split(body, "\n")
	group := ""
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			group = t[1 : len(t)-1]
			continue
		}
		key, val, ok := strings.Cut(t, "=")
		if group != "Container" || !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Volume", "Network":
			lines[i] = strings.TrimSpace(key) + "=" + first(strings.TrimSpace(val))
		case "Mount":
			fields := strings.Split(strings.TrimSpace(val), ",")
			for j, f := range fields {
				if k, v, ok := strings.Cut(f, "="); ok && (k == "source" || k == "src") {
					if to, ok := names[v]; ok {
						fields[j] = k + "=" + to
					}
				}
			}
			lines[i] = "Mount=" + strings.Join(fields, ",")
		}
	}
	return strings.Join(lines, "\n")
}

// imageHolder points at one container's deployed and previous image, so
// Update, Rollback and SetImage can advance images uniformly: the spec
// itself for a single-container Service, each component otherwise.
type imageHolder struct {
	name        string
	image, prev *string
}

func imageHolders(spec *maitrev1.ServiceSpec) []imageHolder {
	if !isMulti(spec) {
		return []imageHolder{{"", &spec.Image, &spec.PreviousImage}}
	}
	hs := make([]imageHolder, 0, len(spec.GetComponents()))
	for _, c := range spec.GetComponents() {
		hs = append(hs, imageHolder{c.GetName(), &c.Image, &c.PreviousImage})
	}
	return hs
}
