package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/quadlet/parser"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Decision 189 (ADR 0008): a Service may use anything a compose file or a
// Catalog entry can say, but options that can give a container control of
// its Node are protected. The Node itself finds them in the container
// definition it is about to install, so a forged or altered Service record
// cannot slip one past it, and runs the Service only when a Trusted Device
// granted each one from a warning dialog.

// KindGrant is the Record kind of a grant (maitrev1.Grant).
const KindGrant = "grant"

// safeKeys are [Container] keys that cannot reach beyond the container's
// own sandbox. Keys not listed here, and these with some values (checked in
// protectedLine), are protected; a key Quadlet adds later is protected
// until it is looked at.
var safeKeys = map[string]bool{
	"Image": true, "ContainerName": true, "Environment": true, "Exec": true, "Entrypoint": true,
	"WorkingDir": true, "User": true, "Group": true, "GroupAdd": true, "Volume": true, "Mount": true,
	"Tmpfs": true, "Network": true, "PublishPort": true, "ExposeHostPort": true, "IP": true, "IP6": true,
	"HostName": true, "DNS": true, "DNSSearch": true, "DNSOption": true, "AddHost": true, "Label": true,
	"Annotation": true, "HealthCmd": true, "HealthInterval": true, "HealthOnFailure": true,
	"HealthRetries": true, "HealthStartPeriod": true, "HealthStartupCmd": true, "HealthStartupInterval": true,
	"HealthStartupRetries": true, "HealthStartupSuccess": true, "HealthStartupTimeout": true,
	"HealthTimeout": true, "Notify": true, "Timezone": true, "Secret": true, "Ulimit": true, "ShmSize": true,
	"PidsLimit": true, "StopSignal": true, "StopTimeout": true, "ReadOnly": true, "ReadOnlyTmpfs": true,
	"RunInit": true, "UserNS": true, "UIDMap": true, "GIDMap": true, "SubUIDMap": true, "SubGIDMap": true,
	"DropCapability": true, "NoNewPrivileges": true, "AutoUpdate": true, "Pull": true, "LogDriver": true,
	"LogOpt": true, "Pod": true, "Memory": true, "StartWithPod": true,
}

// Protected returns the protected options in a Service's rendered
// [Container] body, sorted, each in the words a grant names it by.
// managed are the host folders Maître itself mounts for the Service (its
// volumes and the folders picked for it); other host paths are protected.
func Protected(body string, managed []string) []string {
	unit := parser.NewUnitFile()
	if err := unit.Parse(body); err != nil {
		// A body Quadlet cannot parse is wholly protected: the Node does
		// not get to run a Service it cannot read. apply also runs the
		// Quadlet dry run, which reports the parse error itself.
		return []string{"(unit could not be parsed)"}
	}
	seen := map[string]bool{}
	for _, group := range unit.ListGroups() {
		if group == "Container" {
			for _, o := range protectedContainer(unit, managed) {
				seen[o] = true
			}
			continue
		}
		// Anything in [Service], [Unit] and the like runs on the Node
		// itself, such as ExecStartPre= as root. Read each key's values the
		// way Quadlet does (continuations applied, quoting handled).
		for _, key := range unit.ListKeys(group) {
			for _, v := range unit.LookupAll(group, key) {
				seen["["+group+"] "+key+"="+strings.TrimSpace(v)] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for o := range seen {
		out = append(out, o)
	}
	sort.Strings(out)
	return out
}

// protectedContainer classifies the [Container] group's keys, reading each
// the way Quadlet's own converter reads it.
func protectedContainer(unit *parser.UnitFile, managed []string) []string {
	const g = "Container"
	var out []string
	for _, key := range unit.ListKeys(g) {
		switch key {
		case "AddCapability", "AddDevice", "Sysctl", "Mask", "Unmask":
			// Space-separated, quote-aware, like Quadlet; one grant each.
			for _, f := range unit.LookupAllStrv(g, key) {
				out = append(out, key+"="+f)
			}
		case "Network":
			for _, v := range unit.LookupAll(g, key) {
				v = strings.TrimSpace(v)
				if v == "host" || strings.HasPrefix(v, "container:") || strings.HasPrefix(v, "ns:") {
					out = append(out, "Network="+v)
				}
			}
		case "UserNS":
			for _, v := range unit.LookupAll(g, key) {
				if strings.TrimSpace(v) == "host" {
					out = append(out, "UserNS=host")
				}
			}
		case "NoNewPrivileges":
			if v, ok := unit.Lookup(g, key); ok {
				switch strings.TrimSpace(v) {
				case "false", "no", "0", "off":
					out = append(out, "NoNewPrivileges=false")
				}
			}
		case "Volume":
			for _, v := range unit.LookupAll(g, key) {
				src, _, _ := strings.Cut(strings.TrimSpace(v), ":")
				if hostPath(src) && !under(src, managed) {
					out = append(out, "Volume="+filepath.Clean(src))
				}
			}
		case "Mount":
			// Each arg (whitespace-split, quote-aware) is one mount spec, a
			// comma-separated list, exactly as Quadlet reads KeyMount.
			for _, m := range unit.LookupAllArgs(g, key) {
				if o := protectedMount(m, managed); o != "" {
					out = append(out, o)
				}
			}
		default:
			if safeKeys[key] {
				continue
			}
			for _, v := range unit.LookupAll(g, key) {
				out = append(out, key+"="+strings.TrimSpace(v))
			}
		}
	}
	return out
}

// protectedMount classifies one --mount spec. Bind mounts of a folder
// Maître did not place, and any type it does not vet, are protected.
func protectedMount(spec string, managed []string) string {
	typ, src := "volume", ""
	for _, kv := range strings.Split(spec, ",") {
		k, v, _ := strings.Cut(kv, "=")
		switch strings.TrimSpace(k) {
		case "type":
			typ = strings.TrimSpace(v)
		case "source", "src":
			src = strings.TrimSpace(v)
		}
	}
	switch typ {
	case "volume", "tmpfs", "image":
		return ""
	case "bind":
		if hostPath(src) && under(src, managed) {
			return ""
		}
		return "Volume=" + filepath.Clean(src)
	}
	return "Mount=" + spec
}

// hostPath reports whether a Volume source is a folder of the Node rather
// than a named volume.
func hostPath(src string) bool {
	return strings.HasPrefix(src, "/") || strings.HasPrefix(src, ".") || strings.HasPrefix(src, "~")
}

func under(p string, dirs []string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	p = filepath.Clean(p)
	for _, d := range dirs {
		d = filepath.Clean(d)
		if d != "/" && (p == d || strings.HasPrefix(p, d+"/")) {
			return true
		}
	}
	return false
}

// ImageRepository is an image without its tag or digest.
func ImageRepository(image string) string {
	repo, _, _ := strings.Cut(image, "@")
	if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
		repo = repo[:i]
	}
	return repo
}

// GrantID is the record id of the grant of option to a service's component
// on nodeID. For a single-container Service (component "") it is exactly what
// it was before components, so grants made then still match.
func GrantID(nodeID, service, component, option string) string {
	key := option
	if component != "" {
		key = component + "\x00" + option
	}
	sum := sha256.Sum256([]byte(key))
	if len(nodeID) > 16 {
		nodeID = nodeID[:16]
	}
	return nodeID + "-" + service + "-" + hex.EncodeToString(sum[:8])
}

// GrantsError is a Service whose protected options lack grants.
type GrantsError struct{ Needed *maitrev1.GrantsNeeded }

func (e *GrantsError) Error() string {
	return fmt.Sprintf("%s needs a Trusted Device to allow: %s", e.Needed.GetService(), strings.Join(e.Needed.GetOptions(), ", "))
}

// ProtectedOption is one protected option of a Service, with the component
// it is in ("" for a single-container Service) and that component's image
// repository, which a grant names.
type ProtectedOption struct {
	Component       string `json:"component,omitempty"`
	ImageRepository string `json:"image_repository"`
	Option          string `json:"option"`
}

// protectedOptions lists the protected options of every component of a
// Service, rendered with its volumes in place.
func protectedOptions(spec *maitrev1.ServiceSpec, vols map[string]string, managed []string) ([]ProtectedOption, error) {
	var out []ProtectedOption
	for _, c := range components(spec) {
		body, err := renderComponentBody(spec, c, vols)
		if err != nil {
			return nil, err
		}
		repo := ImageRepository(c.Image)
		for _, o := range Protected(body, managed) {
			out = append(out, ProtectedOption{Component: c.Name, ImageRepository: repo, Option: o})
		}
	}
	return out, nil
}

// checkGrants returns a GrantsError when a protected option in any of the
// Service's components has no live grant for this Service, Node, component
// and image repository, signed by a Trusted Device. A single-container
// Service is one component with an empty name, so its grants are unchanged.
func (m *Manager) checkGrants(ctx context.Context, spec *maitrev1.ServiceSpec, vols map[string]string, managed []string) error {
	self := m.store.NodeID()
	type need struct{ component, repo, option, id string }
	var needs []need
	opts, err := protectedOptions(spec, vols, managed)
	if err != nil {
		return err
	}
	for _, o := range opts {
		needs = append(needs, need{o.Component, o.ImageRepository, o.Option, GrantID(self, spec.GetName(), o.Component, o.Option)})
	}
	if len(needs) == 0 {
		return nil
	}
	recs, err := m.store.List(ctx, KindGrant)
	if err != nil {
		return err
	}
	granted := map[string]bool{}
	key := func(component, repo, option string) string { return component + "\x00" + repo + "\x00" + option }
	for _, r := range recs {
		var g maitrev1.Grant
		if r.GetDeleted() || proto.Unmarshal(r.GetBody(), &g) != nil {
			continue
		}
		if g.GetNodeId() != self || g.GetService() != spec.GetName() || r.GetId() != GrantID(self, g.GetService(), g.GetComponent(), g.GetOption()) {
			continue
		}
		if m.cfg.DeviceSigned != nil && !m.cfg.DeviceSigned(ctx, r) {
			continue
		}
		granted[key(g.GetComponent(), g.GetImageRepository(), g.GetOption())] = true
	}
	missing := &maitrev1.GrantsNeeded{NodeId: self, Service: spec.GetName()}
	for _, n := range needs {
		if granted[key(n.component, n.repo, n.option)] {
			continue
		}
		missing.Options = append(missing.Options, n.option)
		missing.GrantIds = append(missing.GrantIds, n.id)
		if isMulti(spec) {
			missing.Components = append(missing.Components, n.component)
			missing.ImageRepositories = append(missing.ImageRepositories, n.repo)
		}
	}
	if !isMulti(spec) {
		// Single-container shape, unchanged for the current Console.
		missing.ImageRepository = ImageRepository(spec.GetImage())
	}
	if len(missing.Options) == 0 {
		return nil
	}
	return &GrantsError{Needed: missing}
}

// EnforceGrants stops and uninstalls every Service on this Node whose
// protected options are no longer all granted (a grant was revoked, or a
// Service from before decision 189 has one). Runs when grants change and
// at start.
func (m *Manager) EnforceGrants(ctx context.Context) {
	specs, err := store.ListLive(ctx, m.store, KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
	if err != nil {
		return
	}
	for _, spec := range specs {
		if spec.GetNodeId() != m.store.NodeID() {
			continue
		}
		vols, extra, err := m.layout(ctx, spec)
		if err != nil {
			continue
		}
		if err := m.checkGrants(ctx, spec, vols, managedDirs(vols, extra)); err != nil {
			m.logf("stopping %s: %v", spec.GetName(), err)
			m.uninstallUnit(ctx, spec)
		}
	}
}
