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
	seen := map[string]bool{}
	section := "Container"
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			continue
		}
		if section != "Container" {
			// Anything in [Service], [Unit] and the like runs on the Node
			// itself, such as ExecStartPre= as root.
			seen["["+section+"] "+line] = true
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		for _, o := range protectedLine(key, value, managed) {
			seen[o] = true
		}
	}
	out := make([]string, 0, len(seen))
	for o := range seen {
		out = append(out, o)
	}
	sort.Strings(out)
	return out
}

func protectedLine(key, value string, managed []string) []string {
	switch key {
	case "AddCapability", "AddDevice", "Sysctl", "Mask", "Unmask":
		// One grant per capability, device or setting.
		var out []string
		for _, f := range strings.Fields(value) {
			out = append(out, key+"="+f)
		}
		return out
	case "Network":
		switch {
		case value == "host", strings.HasPrefix(value, "container:"), strings.HasPrefix(value, "ns:"):
			return []string{"Network=" + value}
		}
		return nil
	case "UserNS":
		if value == "host" {
			return []string{"UserNS=host"}
		}
		return nil
	case "NoNewPrivileges":
		if value == "false" || value == "no" || value == "0" {
			return []string{"NoNewPrivileges=false"}
		}
		return nil
	case "Volume":
		src, _, _ := strings.Cut(value, ":")
		if hostPath(src) && !under(src, managed) {
			return []string{"Volume=" + filepath.Clean(src)}
		}
		return nil
	case "Mount":
		typ, src := "volume", ""
		for _, kv := range strings.Split(value, ",") {
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
			return nil
		case "bind":
			if hostPath(src) && under(src, managed) {
				return nil
			}
			return []string{"Volume=" + filepath.Clean(src)}
		}
		return []string{"Mount=" + value}
	}
	if safeKeys[key] {
		return nil
	}
	return []string{key + "=" + value}
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

// GrantID is the record id of the grant of option to service on nodeID.
func GrantID(nodeID, service, option string) string {
	sum := sha256.Sum256([]byte(option))
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

// checkGrants returns a GrantsError when a protected option in body has no
// live grant for this Service, this Node and this image's repository,
// signed by a Trusted Device.
func (m *Manager) checkGrants(ctx context.Context, spec *maitrev1.ServiceSpec, body string, managed []string) error {
	need := Protected(body, managed)
	if len(need) == 0 {
		return nil
	}
	self := m.store.NodeID()
	repo := ImageRepository(spec.GetImage())
	granted := map[string]bool{}
	recs, err := m.store.List(ctx, KindGrant)
	if err != nil {
		return err
	}
	for _, r := range recs {
		var g maitrev1.Grant
		if r.GetDeleted() || proto.Unmarshal(r.GetBody(), &g) != nil {
			continue
		}
		if g.GetNodeId() != self || g.GetService() != spec.GetName() || g.GetImageRepository() != repo || r.GetId() != GrantID(self, g.GetService(), g.GetOption()) {
			continue
		}
		if m.cfg.DeviceSigned != nil && !m.cfg.DeviceSigned(ctx, r) {
			continue
		}
		granted[g.GetOption()] = true
	}
	missing := &maitrev1.GrantsNeeded{NodeId: self, Service: spec.GetName(), ImageRepository: repo}
	for _, o := range need {
		if !granted[o] {
			missing.Options = append(missing.Options, o)
			missing.GrantIds = append(missing.GrantIds, GrantID(self, spec.GetName(), o))
		}
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
		body, err := renderBody(spec, vols)
		if err != nil {
			continue
		}
		if err := m.checkGrants(ctx, spec, body, managedDirs(vols, extra)); err != nil {
			m.logf("stopping %s: %v", spec.GetName(), err)
			m.uninstallUnit(ctx, spec.GetName())
		}
	}
}
