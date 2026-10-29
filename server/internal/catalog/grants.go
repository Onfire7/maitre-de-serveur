package catalog

import (
	"fmt"
	"strings"
)

// Grant is a protected option an entry needs (docs/popularity-and-grants.md,
// G1), declared so the Console can say why before it installs.
type Grant struct {
	// Option is a protected option as services.Protected words it. A
	// trailing * matches any value: AddDevice=* for a device the person
	// picks.
	Option string `yaml:"option" json:"option"`
	// Component is the component that needs it, in an entry of several.
	Component string `yaml:"component,omitempty" json:"component,omitempty"`
	// Why the app needs this power, in the person's words.
	Why string `yaml:"why" json:"why"`
	// Optional names the feature the grant enables; the templates put the
	// option inside {{ if .Grants.<feature> }}. Empty: required.
	Optional string `yaml:"optional,omitempty" json:"optional,omitempty"`
	// Without says what the person loses with the feature off.
	Without string `yaml:"without,omitempty" json:"without,omitempty"`
}

// Matches says whether a protected option of component is this grant's.
func (g Grant) Matches(component, option string) bool {
	if g.Component != component {
		return false
	}
	if p, ok := strings.CutSuffix(g.Option, "*"); ok {
		return strings.HasPrefix(option, p)
	}
	return option == g.Option
}

// Features are the entry's optional features, in the order declared.
func (m *Manifest) Features() []string {
	var out []string
	seen := map[string]bool{}
	for _, g := range m.Grants {
		if g.Optional != "" && !seen[g.Optional] {
			seen[g.Optional] = true
			out = append(out, g.Optional)
		}
	}
	return out
}

func (m *Manifest) validateGrants() error {
	comps := map[string]bool{}
	for _, c := range m.Components {
		comps[c.Name] = true
	}
	for _, g := range m.Grants {
		switch {
		case g.Option == "" || !strings.Contains(g.Option, "="):
			return fmt.Errorf("grant %q: name the option as Key=value", g.Option)
		case strings.TrimSpace(g.Why) == "":
			return fmt.Errorf("grant %s: say why the app needs it (why:)", g.Option)
		case len(m.Components) == 0 && g.Component != "":
			return fmt.Errorf("grant %s: component %q, and the entry has none", g.Option, g.Component)
		case len(m.Components) > 0 && !comps[g.Component]:
			return fmt.Errorf("grant %s: component must name the component that needs it", g.Option)
		case g.Optional != "" && !fieldRe.MatchString(g.Optional):
			return fmt.Errorf("grant %s: optional %q is a template field, lowercase letters, digits and underscores", g.Option, g.Optional)
		case g.Optional == "" && g.Without != "":
			return fmt.Errorf("grant %s: without: is for an optional grant", g.Option)
		case g.Optional != "" && strings.TrimSpace(g.Without) == "":
			return fmt.Errorf("grant %s: say what the person loses without %s (without:)", g.Option, g.Optional)
		}
	}
	for _, f := range m.Features() {
		used := false
		for _, q := range m.templates() {
			used = used || strings.Contains(q, ".Grants."+f)
		}
		if !used {
			return fmt.Errorf("optional feature %q is not used by any template ({{ if .Grants.%s }})", f, f)
		}
	}
	return nil
}

// Support says whether Maître runs a protected option as the Catalog would
// promise (G3), and when it does not, what must be added before it can.
// The table grows until it covers everything Quadlet can say.
func Support(option string) (ok bool, needs string) {
	key, value, _ := strings.Cut(option, "=")
	switch {
	case strings.HasPrefix(option, "["):
		return false, "a design for commands in a unit's own sections: they run as root on the Node, outside any container"
	case option == "(unit could not be parsed)":
		return false, "a unit Podman's parser can read"
	}
	switch key {
	case "Network":
		if value == "host" {
			return true, ""
		}
		return false, "Maître to manage the other container's lifecycle with this one (Network=" + value + ")"
	case "AddDevice":
		// container-selinux lets containers use GPUs (container_use_dri_devices,
		// on by default); any other device needs container_use_devices, off.
		// host[:container[:permissions]], as Quadlet and Podman read it.
		if host, _, _ := strings.Cut(value, ":"); host == "/dev/dri" || strings.HasPrefix(host, "/dev/dri/") {
			return true, ""
		}
		return false, "SELinux letting a container use the device (container-selinux's container_use_devices boolean, off on Fedora, or a narrower rule) set in the Node image, checked on a Node with the device plugged in"
	case "AddCapability", "Sysctl", "UserNS", "NoNewPrivileges":
		return true, ""
	case "Volume":
		path, _, _ := strings.Cut(value, ":")
		switch path {
		case "/run/dbus", "/run/dbus/system_bus_socket", "/var/run/dbus":
			// Bluetooth for Home Assistant and the like goes through the
			// host's BlueZ over the system bus.
			return false, "BlueZ (bluetooth.service) in the Node image, and SELinux letting a container connect to the Node's system bus (container_t to system_dbusd_t), checked on a Node"
		case "/run/podman/podman.sock", "/var/run/podman/podman.sock", "/run/docker.sock", "/var/run/docker.sock":
			// The Node runs podman.socket, but SELinux keeps container_t
			// from connecting to it; Podman's own answer is label=disable.
			return false, "SELinux letting a container use the Node's Podman socket (container_t to container_runtime_t) without turning SELinux off for it, and a warning that says the socket is root on the Node"
		}
		// Any other folder of the Node, named in the warning.
		return true, ""
	case "PodmanArgs", "GlobalArgs":
		return false, "nothing in Maître can check raw Podman arguments; ask for the specific capability, device or folder instead, or teach protect.go the Quadlet key that says it"
	case "SecurityLabelDisable", "SecurityLabelType", "SecurityLabelLevel", "SecurityLabelFileType", "SecurityLabelNested", "SeccompProfile", "Mask", "Unmask":
		return false, "a design for weakening SELinux or seccomp per Service (" + key + ")"
	case "Mount":
		return false, "protect.go to classify this kind of mount (" + value + ")"
	}
	return false, "protect.go to classify the Quadlet key " + key + ", and a line in catalog.Support saying whether Maître runs it"
}
