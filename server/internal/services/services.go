// Package services turns a ServiceSpec (built by the Console from a Catalog
// entry plus the user's choices, decision 78) into a
// running Podman container managed by systemd (Quadlet), and reports on it.
// It is the only code that touches Quadlet files or systemd.
package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/direct"
	"github.com/Onfire7/maitre-de-serveur/server/internal/layout"
	"github.com/Onfire7/maitre-de-serveur/server/internal/owner"
	"github.com/Onfire7/maitre-de-serveur/server/internal/quadlet"
	"github.com/Onfire7/maitre-de-serveur/server/internal/quadlet/parser"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/timezone"
)

// KindService is the Record kind for ServiceSpec bodies.
const KindService = "service"

// Network is the Quadlet network unit every Service joins (image/maitre.network).
const Network = "maitre.network"

// maitreNetworkStub stands in for image/maitre.network when the Node checks a
// unit with the Quadlet generator: the container references the network by
// name, so the generator needs a unit of that name present to resolve it.
const maitreNetworkStub = "[Network]\nNetworkName=maitre\nOptions=isolate=true\n"

// lanNetworkStub does the same for the home network (decision 327), which
// the Node writes from its own interface and subnet and so cannot be a
// fixed file in the image. The check only needs a unit of the name to
// resolve against; what it would really contain does not matter here.
const lanNetworkStub = "[Network]\nNetworkName=" + LANNetworkName + "\nDriver=macvlan\n"

// Runner executes systemd and podman commands; swapped for a fake in tests.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// ExecRunner runs real commands.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := execCommand(ctx, name, args...)
	return out, err
}

// Config locates things on the Node.
type Config struct {
	// Log for notable events; nil is fine.
	Log *slog.Logger
	// Remote reports a Service's tailnet URL and state (Milestone 5); nil
	// when the Node has no remote exposure.
	Remote func(name string) (url, state string)
	// Forward reports the router port forwarding a Service depends on
	// (decision 115); nil when the Node does no port mapping.
	Forward func(name string) string
	// UnitsDir is where Quadlet looks for *.container (default /etc/containers/systemd).
	UnitsDir string
	// SystemdDir is where plain systemd units go (default /etc/systemd/system):
	// a multi-container Service's grouping .target, which Quadlet does not
	// read, so one written beside the .container files is never a unit.
	SystemdDir string
	// PoolsDir holds one Pool per Space: volumes live at PoolsDir/<space>/<service>/<volume>.
	PoolsDir string
	// DefaultSpace returns the id of the Space new volumes land in, or "" when
	// the Fleet has none marked (install then fails with a clear message).
	DefaultSpace func(ctx context.Context) string
	// HostPortBase..HostPortBase+9999 are loopback ports the agent assigns to
	// Services' web ports for Caddy and health checks.
	HostPortBase int
	// Suffix is the mDNS/Caddy domain; DefaultSuffix unless overridden.
	Suffix string
	// HTTPClient probes health; nil means a 2-second default.
	HTTPClient *http.Client
	// SpaceOwner is a Space's owner id (decision 182), 0 when it has none.
	SpaceOwner func(ctx context.Context, spaceID string) uint32
	// Decision 189. DeviceSigned reports whether a grant Record is signed
	// by a Trusted Device; Authentic whether a Service Record is signed by
	// this Node. Nil accepts every Record (tests).
	DeviceSigned func(ctx context.Context, rec *maitrev1.Record) bool
	Authentic    func(rec *maitrev1.Record) bool
	// ImageStore picks where podman's image store belongs (decision 319):
	// the OS disk's data partition, or the largest Drive of the default
	// Pool, and "" when this Node has no Drive at all. Nil means the Node
	// cannot choose and the store stays where podman puts it.
	ImageStore func(ctx context.Context) (string, error)
	// FreeBytes is the free space of the filesystem holding a path, which
	// the guards before a pull and the prune both measure. Nil asks the
	// real filesystem; tests say what a store has left without one.
	FreeBytes func(dir string) uint64
	// LocalPool says whether a Space spans Nodes and, if so, where this
	// Node's own Drives of it are pooled ("" when it has none). A
	// Service's own folders in such a Space live there (decision 208), on
	// its Node's Drives, never a peer's over NFS. Nil: no Space spans.
	LocalPool func(ctx context.Context, space string) (dir string, spans bool)
	// ValidateUnits runs podman's Quadlet generator over each rendered unit
	// before writing it (plan C6). The agent sets it true; tests leave it
	// off so they do not depend on a generator being installed.
	ValidateUnits bool
	// LAN reports the Node's own place on the home network for a Service
	// given an address of its own there (decision 327, lan.go): the
	// interface the Node reaches the LAN through, its address, that
	// network in CIDR form, and its gateway. Nil means this Node cannot
	// offer such an address, and asking for one is refused with that.
	LAN func() (iface, address, subnet, gateway string, err error)
}

// DefaultSuffix is the domain Services are reachable under on the LAN:
// <name>.local. Two labels, because Linux desktops' default mDNS resolver
// (nss-mdns mdns_minimal) rejects deeper names such as <name>.maitre.local
// (decision 75).
const DefaultSuffix = "local"

// Mover queues a file move and reports when it finished: the file
// manager (files.Manager.MoveThen). Nil when the Node has none.
type Mover interface {
	MoveThen(ctx context.Context, req *maitrev1.MoveRequest, done func(error)) (*maitrev1.Job, error)
}

// Manager implements Service operations for one Node.
type Manager struct {
	// OnExposure runs after a Service's exposure changed (remote reconcile).
	OnExposure func()
	// Mover moves volumes between Spaces (MoveVolume); nil means it cannot.
	Mover Mover
	cfg   Config
	store *store.Store
	run   Runner
	// Decision 317: bytes each Service's images have cost to pull since
	// this agent started (pull.go).
	pullMu    sync.Mutex
	pullBytes map[string]uint64
	// The graphroot podman reports, resolved once (prune.go).
	storeMu sync.Mutex
	store_  string
}

// New builds a Manager.
func New(cfg Config, st *store.Store, run Runner) *Manager {
	if cfg.UnitsDir == "" {
		cfg.UnitsDir = "/etc/containers/systemd"
	}
	if cfg.SystemdDir == "" {
		cfg.SystemdDir = "/etc/systemd/system"
	}
	if cfg.HostPortBase == 0 {
		cfg.HostPortBase = 20000
	}
	if cfg.Suffix == "" {
		cfg.Suffix = DefaultSuffix
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 2 * time.Second}
	}
	if run == nil {
		run = ExecRunner{}
	}
	return &Manager{cfg: cfg, store: st, run: run}
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`)

// ErrExists is returned when a Service name is taken.
var ErrExists = errors.New("a Service with that name already exists")

// ErrNotFound is returned for an unknown Service.
var ErrNotFound = errors.New("no such Service")

// Install creates, writes, and starts a Service from a complete spec.
func (m *Manager) Install(ctx context.Context, in *maitrev1.ServiceSpec) (*maitrev1.ServiceStatus, error) {
	if in == nil {
		return nil, errors.New("no service spec")
	}
	spec := proto.Clone(in).(*maitrev1.ServiceSpec)
	name := spec.GetName()
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("service name %q: use lowercase letters, digits and dashes", name)
	}
	// Decision 189: settings land in unit file lines.
	for k, v := range spec.GetSettings() {
		if strings.ContainsAny(k+v, "\n\r") {
			return nil, fmt.Errorf("setting %s has a line break", k)
		}
	}
	if err := validateContainers(spec); err != nil {
		return nil, err
	}
	normalizePages(spec)
	if err := validatePages(spec); err != nil {
		return nil, err
	}
	if err := validateOverrides(spec); err != nil {
		return nil, err
	}
	// A name a removed Service left behind (its tombstone, perhaps owned
	// by a Node since gone from the Fleet) is free to claim: the write
	// takes ownership, which peers admit (authz: only a live Service of
	// another Node blocks the name).
	claim := false
	switch cur, err := m.store.Raw(ctx, KindService, name); {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return nil, err
	case !cur.GetDeleted():
		return nil, ErrExists
	default:
		claim = true
	}
	if spec.Settings == nil {
		spec.Settings = map[string]string{}
	}
	// Decision 282: "auto" is kept as written and resolved when the unit
	// is rendered, so the Service follows the Fleet's time zone.
	if tz, ok := spec.Settings["TZ"]; ok && tz == "" {
		spec.Settings["TZ"] = timezone.Auto
	}
	if spec.GetExposure() == "" {
		spec.Exposure = "private"
	}
	// Decision 287: a Service can be installed with any of the four kinds
	// of access, checked as SetExposure checks a change.
	host, err := m.checkExposure(ctx, spec.GetName(), spec, spec.GetExposure(), spec.GetPublicHost())
	if err != nil {
		return nil, err
	}
	spec.PublicHost = host
	spec.NodeId = m.store.NodeID()
	spec.CreatedUnixMs = time.Now().UnixMilli()
	// Volume Spaces come from the Console (decision 107); missing ones take
	// the Space marked default for new volumes (decision 123: no built-in one).
	if spec.VolumeSpaces == nil {
		spec.VolumeSpaces = map[string]string{}
	}
	for _, v := range spec.GetVolumes() {
		if spec.VolumeSpaces[v.GetName()] == "" {
			def := ""
			if m.cfg.DefaultSpace != nil {
				def = m.cfg.DefaultSpace(ctx)
			}
			if def == "" {
				return nil, fmt.Errorf("no Storage Space is marked as the default for new volumes; create one first")
			}
			spec.VolumeSpaces[v.GetName()] = def
		}
	}
	if err := checkMounts(spec.GetVolumes(), spec.GetMounts()); err != nil {
		return nil, err
	}
	liftLANAddress(spec)
	if err := m.validateNetwork(ctx, spec); err != nil {
		return nil, err
	}
	// ADR 0010: an address of the Fleet's network, unique across every
	// Node, kept through a move; the Console may have chosen one.
	if spec.GetAddress() == "" {
		addr, err := m.allocateAddress(ctx)
		if err != nil {
			return nil, err
		}
		spec.Address = addr
	} else if err := m.checkAddress(ctx, name, spec.GetAddress()); err != nil {
		return nil, err
	}
	for _, p := range spec.GetPages() {
		p.HostPort = 0
	}
	// Each Page gets a loopback port of the Node (G4: on the Node's own
	// network the app's port is the Node's; Caddy, Tailscale and the
	// probe reach it there).
	if err := m.assignHostPorts(ctx, spec); err != nil {
		return nil, err
	}
	// Install writes no image; the unit pulls at its first start. Saying so
	// here is still the only chance to say it before the download rather
	// than as ENOSPC thirty-five seconds into it.
	var want []string
	for _, c := range components(spec) {
		want = append(want, c.Image)
	}
	if err := m.ensureRoomToPull(ctx, want...); err != nil {
		return nil, err
	}
	if err := m.apply(ctx, spec); err != nil {
		return nil, err
	}
	if spec.GetInfrastructure() {
		// Other devices will point at this Node; its address must not move.
		if addr, err := PinAddress(ctx, m.run); err != nil {
			return nil, fmt.Errorf("%s needs a fixed address for this Node, which could not be set: %w", name, err)
		} else if m.cfg.Log != nil {
			m.cfg.Log.Info("address pinned for infrastructure service", "service", name, "address", addr)
		}
	}
	if _, err := m.store.Put(ctx, KindService, name, spec, claim); err != nil {
		return nil, err
	}
	// A Service installed onto a Tunnel: the Tunnel's unit now publishes
	// its ports and wants it, and restarting the Tunnel starts it.
	if spec.GetNetworkKind() == NetworkTunnel {
		if err := m.reapplyTunnel(ctx, spec.GetNetworkTarget()); err != nil {
			return nil, err
		}
	}
	if _, err := m.run.Run(ctx, "systemctl", "start", "--no-block", lifecycleUnit(spec)); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	// Reached from outside the home from the start: the Tailscale serve or
	// Funnel, or the public host's route, is set up now, not at the next
	// Access change.
	if spec.GetExposure() != "private" && m.OnExposure != nil {
		m.OnExposure()
	}
	return m.Status(ctx, spec)
}

// poolDir is the Pool of a Space on this Node.
func (m *Manager) poolDir(space string) (string, error) {
	if m.cfg.PoolsDir == "" {
		return filepath.Join(os.TempDir(), "maitre-pools", space), nil // dev mode
	}
	base := filepath.Join(m.cfg.PoolsDir, space)
	if _, err := os.Stat(base); err != nil {
		return "", fmt.Errorf("Space %q has no Pool on this Node", space)
	}
	return base, nil
}

// ownBase is where a Service's own folders of a Space go: the Space's Pool,
// or for a Space that spans Nodes this Node's own Drives of it (decision
// 208), so a container's data stays on its Node: a peer's Drive over NFS
// is slow for a database and squashes root.
func (m *Manager) ownBase(ctx context.Context, space, pool string) (string, error) {
	if m.cfg.LocalPool == nil {
		return pool, nil
	}
	dir, spans := m.cfg.LocalPool(ctx, space)
	if !spans {
		return pool, nil
	}
	if dir == "" {
		return "", fmt.Errorf("this Node has no Drive in Space %s, and a Service's own folders stay on its Node's Drives", space)
	}
	return dir, nil
}

// folderIn resolves a picked folder of a Space ("" is its top), refusing
// ".." and hidden names.
func folderIn(base, folder string) (string, error) {
	// Decision 189: the folder lands in a unit file line; a line break or
	// colon there could add lines of its own.
	if strings.ContainsAny(folder, "\n\r\x00:") {
		return "", fmt.Errorf("%q is not a folder of the Space", folder)
	}
	for _, part := range strings.Split(strings.Trim(strings.TrimSpace(folder), "/"), "/") {
		if part == ".." || (part != "" && strings.HasPrefix(part, ".")) {
			return "", fmt.Errorf("%q is not a folder of the Space", folder)
		}
	}
	return filepath.Join(base, filepath.Clean("/"+folder)), nil
}

func (m *Manager) owner(ctx context.Context, space string) uint32 {
	if m.cfg.SpaceOwner == nil {
		return 0
	}
	return m.cfg.SpaceOwner(ctx, space)
}

// apply renders the Quadlet unit, creates volume directories, reloads systemd.
// A volume has its own folder, <pools>/<space>/<service>/<volume>, or a
// folder the user picked (170); extra folders (171) mount as well. Picked
// folders get their Space's owner (173) and mount shared (:z, 172).
//
// Decision 189: the unit is written only when the Service's Record is this
// Node's own and every protected option in it has a grant; otherwise the
// unit already installed, if any, stays as it was.
func (m *Manager) apply(ctx context.Context, spec *maitrev1.ServiceSpec) error {
	if rec, err := m.store.Raw(ctx, KindService, spec.GetName()); err == nil && !rec.GetDeleted() && m.cfg.Authentic != nil && !m.cfg.Authentic(rec) {
		// notOurs carries the message, so the guards that run before a pull
		// (pull.go) and this one cannot drift apart.
		return m.notOurs(spec)
	}
	vols, extra, err := m.layout(ctx, spec)
	if err != nil {
		return err
	}
	if err := m.checkGrants(ctx, spec, vols, extra, managedDirs(vols, extra)); err != nil {
		return err
	}
	// The home network before the Service that joins it, for the reason
	// ensureNetwork gives about the shared one: a Node that cannot make
	// it says so here, once, instead of leaving the Service reporting
	// "Dependency failed" with the cause nowhere.
	if spec.GetNetworkKind() == NetworkLAN {
		if err := m.ensureLANNetwork(ctx); err != nil {
			return err
		}
	}
	share := func(space, dir string) error {
		return owner.Share(ctx, dir, m.owner(ctx, space), func(err error) {
			if err != nil && m.cfg.Log != nil {
				m.cfg.Log.Warn("shared folder owner", "dir", dir, "err", err)
			}
		})
	}
	for _, v := range spec.GetVolumes() {
		dir, space := vols[v.GetName()], spec.GetVolumeSpaces()[v.GetName()]
		if extra.Shared[v.GetName()] {
			if err := share(space, dir); err != nil {
				return err
			}
		} else if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		} else if err := owner.Admit(dir, m.owner(ctx, space)); err != nil && m.cfg.Log != nil {
			// Decision 216: files can still not be moved in; the Service runs.
			m.cfg.Log.Warn("own folder access", "dir", dir, "err", err)
		}
	}
	for i, mt := range spec.GetMounts() {
		if err := share(mt.GetSpaceId(), extra.Mounts[i].Host); err != nil {
			return err
		}
	}
	set, err := m.renderUnits(ctx, spec, vols, extra)
	if err != nil {
		return err
	}
	if m.cfg.ValidateUnits {
		if err := validateUnits(ctx, spec.GetName(), set); err != nil && !errors.Is(err, quadlet.ErrNoGenerator) {
			return err
		}
	}
	for _, dir := range []string{m.cfg.UnitsDir, m.cfg.SystemdDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	for name, body := range set {
		if err := os.WriteFile(m.unitPath(name), []byte(body), 0o644); err != nil {
			return err
		}
		if isSystemdUnit(name) {
			// Nodes before this fix wrote the target where Quadlet ignored it.
			_ = os.Remove(filepath.Join(m.cfg.UnitsDir, name))
		}
	}
	if _, err := m.run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	// Quadlet's generator wants its units at boot by itself; a plain unit's
	// [Install] counts only once enabled.
	for name := range set {
		if isSystemdUnit(name) {
			if _, err := m.run.Run(ctx, "systemctl", "enable", name); err != nil {
				return fmt.Errorf("enable %s: %w", name, err)
			}
		}
	}
	return nil
}

// RepairUnits writes the units of every Service of this Node whose
// grouping target is missing from the systemd directory: Nodes before the
// fix in isSystemdUnit wrote it among the Quadlet files, where no unit came
// of it, and every one of their multi-container Services answered a start
// with "Unit <name>.target not found". Runs at start; a repaired Service is
// started again as its unit's [Install] would have at boot.
func (m *Manager) RepairUnits(ctx context.Context) {
	specs, err := store.ListLive(ctx, m.store, KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
	if err != nil {
		return
	}
	for _, spec := range specs {
		if spec.GetNodeId() != m.store.NodeID() || !isMulti(spec) {
			continue
		}
		target := lifecycleUnit(spec)
		if _, err := os.Stat(m.unitPath(target)); err == nil {
			continue
		}
		if err := m.apply(ctx, spec); err != nil {
			m.logf("repairing %s: %v", spec.GetName(), err)
			continue
		}
		m.logf("repaired %s: its %s is now a unit", spec.GetName(), target)
		_, _ = m.run.Run(ctx, "systemctl", "start", "--no-block", target)
	}
}

// isSystemdUnit says whether a unit file is systemd's own kind, which goes
// in SystemdDir, rather than a Quadlet file Podman's generator turns into
// one. Quadlet reads only .container, .volume, .network, .pod, .kube,
// .image and .build; a .target in its directory is silently ignored, and
// "Unit <name>.target not found" was every multi-container Service's start.
func isSystemdUnit(name string) bool {
	return strings.HasSuffix(name, ".target")
}

// unitPath is where a Service's unit file lives on this Node.
func (m *Manager) unitPath(name string) string {
	if isSystemdUnit(name) {
		return filepath.Join(m.cfg.SystemdDir, name)
	}
	return filepath.Join(m.cfg.UnitsDir, name)
}

// renderUnits is every unit file of a Service, by file name. A
// single-container Service is one <name>.container as before; a
// multi-container one is a unit per component plus its network and grouping
// target (plan C1).
func (m *Manager) renderUnits(ctx context.Context, spec *maitrev1.ServiceSpec, vols map[string]string, extra Extra) (map[string]string, error) {
	set, err := m.renderAnnotated(ctx, spec, vols, extra)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(set))
	for name, u := range set {
		out[name] = u.String()
	}
	return out, nil
}

// renderAnnotated is renderUnits with each line's source kept (ADR 0010).
func (m *Manager) renderAnnotated(ctx context.Context, spec *maitrev1.ServiceSpec, vols map[string]string, extra Extra) (map[string]unit, error) {
	spec = m.withTimezone(ctx, spec)
	plan := m.planNetwork(ctx, spec)
	if isMulti(spec) {
		return renderMulti(spec, vols, extra, plan)
	}
	u, err := renderContainer(spec, components(spec)[0], vols, extra, plan)
	if err != nil {
		return nil, err
	}
	return map[string]unit{spec.GetName() + ".container": u}, nil
}

// validateUnits checks rendered units with podman's own Quadlet generator
// before they are written, so a unit Podman would reject fails with
// Podman's message instead of silently never starting (plan C6). It returns
// quadlet.ErrNoGenerator where none is installed (dev and test hosts, not
// the Node). The shared maitre.network is a real file on the Node; a stub
// lets the generator resolve the reference during the check.
func validateUnits(ctx context.Context, name string, set map[string]string) error {
	check := map[string]string{Network: maitreNetworkStub, LANNetwork: lanNetworkStub}
	for k, v := range set {
		check[k] = v
	}
	if err := quadlet.DryRun(ctx, check); err != nil {
		if errors.Is(err, quadlet.ErrNoGenerator) {
			return err
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// layout works out, without touching anything, the host folder of each
// volume and the extra folders and groups of a Service.
func (m *Manager) layout(ctx context.Context, spec *maitrev1.ServiceSpec) (map[string]string, Extra, error) {
	vols := map[string]string{}
	extra := Extra{Shared: map[string]bool{}}
	groups := map[uint32]bool{}
	join := func(space string) {
		if id := m.owner(ctx, space); id != 0 {
			groups[id] = true
		}
	}
	for _, v := range spec.GetVolumes() {
		if !plainName(v.GetName()) {
			return nil, extra, fmt.Errorf("volume name %q", v.GetName())
		}
		space := spec.GetVolumeSpaces()[v.GetName()]
		if space == "" {
			return nil, extra, fmt.Errorf("volume %s has no Space", v.GetName())
		}
		base, err := m.poolDir(space)
		if err != nil {
			return nil, extra, fmt.Errorf("volume %s: %w", v.GetName(), err)
		}
		if folder, ok := spec.GetVolumeFolders()[v.GetName()]; ok {
			dir, err := folderIn(base, folder)
			if err != nil {
				return nil, extra, fmt.Errorf("volume %s: %w", v.GetName(), err)
			}
			join(space)
			vols[v.GetName()] = dir
			extra.Shared[v.GetName()] = true
			continue
		}
		own, err := m.ownBase(ctx, space, base)
		if err != nil {
			return nil, extra, fmt.Errorf("volume %s: %w", v.GetName(), err)
		}
		vols[v.GetName()] = filepath.Join(own, layout.VolumeDir(spec.GetName(), v.GetName()))
	}
	for _, mt := range spec.GetMounts() {
		base, err := m.poolDir(mt.GetSpaceId())
		if err != nil {
			return nil, extra, fmt.Errorf("folder for %s: %w", mt.GetPath(), err)
		}
		dir, err := folderIn(base, mt.GetFolder())
		if err != nil {
			return nil, extra, err
		}
		join(mt.GetSpaceId())
		extra.Mounts = append(extra.Mounts, Mount{Host: dir, Path: mt.GetPath(), ReadOnly: mt.GetReadOnly()})
	}
	for id := range groups {
		extra.Groups = append(extra.Groups, id)
	}
	sort.Slice(extra.Groups, func(i, j int) bool { return extra.Groups[i] < extra.Groups[j] })
	return vols, extra, nil
}

// managedDirs are the host folders Maître mounts for a Service itself.
func managedDirs(vols map[string]string, extra Extra) []string {
	var out []string
	for _, d := range vols {
		out = append(out, d)
	}
	for _, mt := range extra.Mounts {
		out = append(out, mt.Host)
	}
	return out
}

// uninstallUnit stops a Service and removes its unit, keeping its Record
// and data, so it does not start again until it is fixed.
func (m *Manager) uninstallUnit(ctx context.Context, spec *maitrev1.ServiceSpec) {
	_, _ = m.run.Run(ctx, "systemctl", "stop", lifecycleUnit(spec))
	removed := false
	for _, f := range unitFiles(spec) {
		if isSystemdUnit(f) {
			_, _ = m.run.Run(ctx, "systemctl", "disable", f)
		}
		if err := os.Remove(m.unitPath(f)); err == nil {
			removed = true
		}
	}
	if removed {
		_, _ = m.run.Run(ctx, "systemctl", "daemon-reload")
	}
}

// plainName rejects names that could break a unit file line.
func plainName(s string) bool {
	return s != "" && !strings.ContainsAny(s, "\n\r\x00:/")
}

// MoveVolume moves one volume of a Service to another Space on this Node
// (docs/node-needs.md). The Service stops, the folder moves as a file
// manager job, the spec follows, and the Service starts again if it was
// running. A failed move leaves the volume where it was and restarts the
// Service on it.
func (m *Manager) MoveVolume(ctx context.Context, name, volume, toSpace string) (string, error) {
	if m.Mover == nil {
		return "", errors.New("this Node has no file manager")
	}
	var spec maitrev1.ServiceSpec
	if _, err := m.store.Get(ctx, KindService, name, &spec); err != nil {
		return "", ErrNotFound
	}
	var vol *maitrev1.ServiceVolume
	for _, v := range spec.GetVolumes() {
		if v.GetName() == volume {
			vol = v
		}
	}
	if vol == nil {
		return "", fmt.Errorf("%s has no volume %q", name, volume)
	}
	from := spec.GetVolumeSpaces()[volume]
	if from == "" {
		return "", fmt.Errorf("volume %s has no Space", volume)
	}
	if from == toSpace {
		return "", fmt.Errorf("volume %s is already in Space %s", volume, toSpace)
	}
	if m.cfg.PoolsDir != "" {
		if _, err := os.Stat(filepath.Join(m.cfg.PoolsDir, toSpace)); err != nil {
			return "", fmt.Errorf("Space %q has no Pool on this Node", toSpace)
		}
		if _, err := m.ownBase(ctx, toSpace, filepath.Join(m.cfg.PoolsDir, toSpace)); err != nil {
			return "", err
		}
	}
	st, err := m.Status(ctx, &spec)
	if err != nil {
		return "", err
	}
	wasRunning := st.GetState() == maitrev1.ServiceStatus_STATE_RUNNING || st.GetState() == maitrev1.ServiceStatus_STATE_STARTING
	unit := lifecycleUnit(&spec)
	if _, err := m.run.Run(ctx, "systemctl", "stop", unit); err != nil {
		return "", fmt.Errorf("stop %s: %w", name, err)
	}
	// To <Space>/.Services/<Service>/<volume> (205), from there or, for a
	// Service installed before, from <Space>/<Service>/<volume>.
	to := layout.VolumeDir(name, volume)
	path := to
	if m.cfg.PoolsDir != "" {
		if _, err := os.Stat(filepath.Join(m.cfg.PoolsDir, from, to)); err != nil {
			if _, err := os.Stat(filepath.Join(m.cfg.PoolsDir, from, layout.LegacyVolumeDir(name, volume))); err == nil {
				path = layout.LegacyVolumeDir(name, volume)
			}
		}
	}
	req := &maitrev1.MoveRequest{From: &maitrev1.Scope{SpaceId: from}, Path: path, ToSpace: toSpace, ToPath: to}
	bg := context.WithoutCancel(ctx)
	job, err := m.Mover.MoveThen(ctx, req, func(moveErr error) {
		if moveErr == nil {
			var cur maitrev1.ServiceSpec
			if _, err := m.store.Get(bg, KindService, name, &cur); err == nil {
				if cur.VolumeSpaces == nil {
					cur.VolumeSpaces = map[string]string{}
				}
				cur.VolumeSpaces[volume] = toSpace
				if err := m.apply(bg, &cur); err != nil {
					m.logf("move volume: apply %s: %v", name, err)
				} else if _, err := m.store.Put(bg, KindService, name, &cur, false); err != nil {
					m.logf("move volume: store %s: %v", name, err)
				}
			}
		} else {
			m.logf("move volume %s/%s to %s: %v; the volume stays in %s", name, volume, toSpace, moveErr, from)
		}
		if wasRunning {
			if _, err := m.run.Run(bg, "systemctl", "start", "--no-block", unit); err != nil {
				m.logf("move volume: start %s: %v", name, err)
			}
		}
	})
	if err != nil {
		if wasRunning {
			_, _ = m.run.Run(ctx, "systemctl", "start", "--no-block", unit)
		}
		return "", err
	}
	return job.GetJobId(), nil
}

func (m *Manager) logf(format string, args ...any) {
	if m.cfg.Log != nil {
		m.cfg.Log.Warn(fmt.Sprintf(format, args...))
	}
}

// Render produces the full Quadlet unit for a Service.
func Render(spec *maitrev1.ServiceSpec, volumes map[string]string) ([]byte, error) {
	return RenderWith(spec, volumes, Extra{})
}

// Extra is what a unit gets beyond its Catalog template: which volumes are
// picked, shared folders (their Volume= lines get :z instead of :Z), extra
// folders, and the Space groups the container joins (172, 171, 182).
type Extra struct {
	Shared map[string]bool
	Mounts []Mount
	Groups []uint32
}

// Mount is an extra folder in a unit.
type Mount struct {
	Host, Path string
	ReadOnly   bool
}

// renderBody executes a Service's Catalog template: its [Container] body.
func renderBody(spec *maitrev1.ServiceSpec, volumes map[string]string) (string, error) {
	return renderTemplate(spec.GetName(), spec.GetQuadlet(), spec.GetImage(), spec, volumes)
}

// renderTemplate executes a [Container] Go template with the data a Catalog
// entry may use (catalog/FORMAT.md). Shared by single-container Services and
// by each component of a multi-container one.
func renderTemplate(name, quadletTmpl, image string, spec *maitrev1.ServiceSpec, volumes map[string]string) (string, error) {
	settings := spec.GetSettings()
	tmpl, err := template.New(name).Option("missingkey=error").Parse(quadletTmpl)
	if err != nil {
		return "", fmt.Errorf("quadlet template: %w", err)
	}
	// Quadlet copies [Container] values into ExecStart as they are, where
	// systemd would expand $VAR and %-specifiers in what the user typed; a
	// setting's value reaches the container as typed only with $ and %
	// doubled.
	escaped := make(map[string]string, len(settings))
	for k, v := range settings {
		escaped[k] = settingEscape.Replace(v)
	}
	data := map[string]any{
		"Image":    image,
		"Name":     name,
		"Volumes":  volumes,
		"Settings": escaped,
		"Network":  Network,
		"Env":      map[string]string{},
		// The entry's optional features, on or off (G9). A feature the
		// spec does not name fails the template, as a missing setting does.
		"Grants": spec.GetOptionalGrants(),
	}
	var body bytes.Buffer
	if err := tmpl.Execute(&body, data); err != nil {
		return "", fmt.Errorf("quadlet template: %w", err)
	}
	return body.String(), nil
}

var settingEscape = strings.NewReplacer("$", "$$", "%", "%%")

// RenderWith is Render with picked and extra folders: a single
// container's unit, without the Fleet (no Tunnel joins it).
func RenderWith(spec *maitrev1.ServiceSpec, volumes map[string]string, extra Extra) ([]byte, error) {
	if isMulti(spec) {
		return nil, errors.New("RenderWith renders a Service of one container")
	}
	u, err := renderContainer(spec, components(spec)[0], volumes, extra, netPlan{kind: spec.GetNetworkKind(), target: spec.GetNetworkTarget()})
	if err != nil {
		return nil, err
	}
	return []byte(u.String()), nil
}

// lookupIs reports whether the [Container] group sets key to value, read as
// Quadlet reads it (last instance wins, continuations and quoting handled).
func lookupIs(unit *parser.UnitFile, key, value string) bool {
	v, ok := unit.Lookup("Container", key)
	return ok && strings.TrimSpace(v) == value
}

// shareVolumes turns the private SELinux label (:Z) of Volume= lines whose
// host folder is in hosts into the shared one (:z), adding it if absent.
func shareVolumes(body string, hosts map[string]bool) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		rest, ok := strings.CutPrefix(strings.TrimSpace(l), "Volume=")
		if !ok {
			continue
		}
		parts := strings.SplitN(rest, ":", 3)
		if len(parts) < 2 || !hosts[parts[0]] {
			continue
		}
		var opts []string
		if len(parts) == 3 {
			for _, o := range strings.Split(parts[2], ",") {
				if o != "Z" && o != "z" && o != "" {
					opts = append(opts, o)
				}
			}
		}
		opts = append(opts, "z")
		lines[i] = "Volume=" + parts[0] + ":" + parts[1] + ":" + strings.Join(opts, ",")
	}
	return strings.Join(lines, "\n")
}

// hostNetworked reports whether a single-container Service's template puts
// it on the Node's own network (Network=host, a granted option, G4).
// Volumes are not placed yet, so placeholders stand in for them.
func hostNetworked(spec *maitrev1.ServiceSpec) (bool, error) {
	if isMulti(spec) {
		return false, nil
	}
	vols := map[string]string{}
	for _, v := range spec.GetVolumes() {
		vols[v.GetName()] = "/maitre-volume"
	}
	body, err := renderBody(spec, vols)
	if err != nil {
		return false, err
	}
	unit := parser.NewUnitFile()
	if err := unit.Parse(body); err != nil {
		return false, fmt.Errorf("quadlet unit: %w", err)
	}
	return lookupIs(unit, "Network", "host"), nil
}

// nodePorts are ports the Node itself holds: SSH, Caddy, NFS and its
// portmapper, Caddy's admin API, mDNS and the agent.
var nodePorts = map[uint32]string{22: "SSH", 80: "Maître's web server", 111: "NFS", 443: "Maître's web server", 2019: "Maître's web server", 2049: "NFS", 5353: "mDNS", 7443: "the Maître agent"}

// portFree refuses a port of the Node that another Service, or the Node
// itself, already holds, naming who.
func (m *Manager) portFree(ctx context.Context, name string, port uint32) error {
	if who, ok := nodePorts[port]; ok {
		return fmt.Errorf("%s serves its page on port %d of the Node itself, which %s uses", name, port, who)
	}
	specs, _ := store.ListLive(ctx, m.store, KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
	for _, s := range specs {
		if s.GetName() == name || s.GetNodeId() != m.store.NodeID() {
			continue
		}
		taken := s.GetWebHostPort() == port
		for _, hp := range s.GetHostPorts() {
			taken = taken || hp == fmt.Sprintf("%d/tcp", port)
		}
		if taken {
			return fmt.Errorf("%s serves its page on port %d of the Node itself, which %s already uses", name, port, s.GetName())
		}
	}
	return nil
}

// Control starts, stops, restarts, or removes a Service.
func (m *Manager) Control(ctx context.Context, req *maitrev1.ControlServiceRequest) (*maitrev1.ServiceStatus, error) {
	var spec maitrev1.ServiceSpec
	if _, err := m.store.Get(ctx, KindService, req.GetName(), &spec); err != nil {
		return nil, ErrNotFound
	}
	unit := lifecycleUnit(&spec)
	switch req.GetAction() {
	case maitrev1.ControlServiceRequest_ACTION_START:
		// A unit held down by its start limit (render.go) refuses a plain
		// start with "start request repeated too quickly", which would make
		// the Console's Start look broken. Asking again is the user saying
		// to try now, so the counter is cleared first.
		//
		// Every unit of the Service, not only the one the lifecycle hangs
		// off: in a Service of several containers that is the .target, and
		// clearing a target clears nothing for the components under it. A
		// component that burnt its five starts -- vikunja's app did on
		// Avalon on 2026-11-06, in twenty-five seconds, while MariaDB was
		// still opening its socket -- then stayed down for the hour with
		// Start and Restart both answering as though they had done
		// something, and the restart counter frozen to prove they had not.
		for _, u := range append(componentServiceUnits(&spec), unit) {
			_, _ = m.run.Run(ctx, "systemctl", "reset-failed", u)
		}
		_, err := m.run.Run(ctx, "systemctl", "start", "--no-block", unit)
		if err != nil {
			return nil, err
		}
	case maitrev1.ControlServiceRequest_ACTION_STOP:
		if _, err := m.run.Run(ctx, "systemctl", "stop", unit); err != nil {
			return nil, err
		}
	case maitrev1.ControlServiceRequest_ACTION_RESTART:
		// Restart is the user asking for it now too, so the same clearing
		// as Start above: a component held by its start limit would make
		// Restart a no-op that reports success.
		for _, u := range append(componentServiceUnits(&spec), unit) {
			_, _ = m.run.Run(ctx, "systemctl", "reset-failed", u)
		}
		if _, err := m.run.Run(ctx, "systemctl", "restart", "--no-block", unit); err != nil {
			return nil, err
		}
	case maitrev1.ControlServiceRequest_ACTION_UPDATE:
		changed, err := m.Update(ctx, &spec)
		if err != nil {
			return nil, err
		}
		if !changed {
			st, err := m.Status(ctx, &spec)
			if st != nil {
				st.Message = "already on the newest image"
			}
			return st, err
		}
	case maitrev1.ControlServiceRequest_ACTION_ROLLBACK:
		rolled := false
		for _, h := range imageHolders(&spec) {
			if *h.prev == "" {
				continue
			}
			*h.image, *h.prev = *h.prev, *h.image
			rolled = true
		}
		if !rolled {
			return nil, fmt.Errorf("%s has no previous image to go back to", spec.GetName())
		}
		if err := m.apply(ctx, &spec); err != nil {
			return nil, err
		}
		if _, err := m.store.Put(ctx, KindService, spec.GetName(), &spec, false); err != nil {
			return nil, err
		}
		if _, err := m.run.Run(ctx, "systemctl", "restart", "--no-block", unit); err != nil {
			return nil, err
		}
	case maitrev1.ControlServiceRequest_ACTION_REMOVE:
		// ADR 0010: Services on its network or Tunnel would break; they
		// leave it first, in their Setup.
		if deps := m.Dependants(ctx, spec.GetName()); len(deps) > 0 {
			return nil, fmt.Errorf("%s cannot be removed while %s %s it; change their Setup first", spec.GetName(), strings.Join(deps, ", "), map[bool]string{true: "use", false: "uses"}[len(deps) > 1])
		}
		_, _ = m.run.Run(ctx, "systemctl", "stop", unit)
		for _, f := range unitFiles(&spec) {
			if isSystemdUnit(f) {
				_, _ = m.run.Run(ctx, "systemctl", "disable", f)
			}
			if err := os.Remove(m.unitPath(f)); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
		}
		if _, err := m.run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
			return nil, err
		}
		if req.GetDeleteData() && m.cfg.PoolsDir != "" {
			// Every volume, on whichever Space's Pool it lives.
			for _, v := range spec.GetVolumes() {
				if _, picked := spec.GetVolumeFolders()[v.GetName()]; picked {
					continue // a folder the user picked is never deleted (174)
				}
				base := filepath.Join(m.cfg.PoolsDir, spec.GetVolumeSpaces()[v.GetName()])
				if err := os.RemoveAll(filepath.Join(base, layout.VolumeDir(spec.GetName(), v.GetName()))); err != nil {
					return nil, err
				}
				_ = os.Remove(filepath.Join(base, layout.ServiceDir(spec.GetName())))
			}
		}
		if _, err := m.store.Delete(ctx, KindService, spec.Name, false); err != nil {
			return nil, err
		}
		if spec.GetNetworkKind() == NetworkTunnel {
			// The Tunnel no longer publishes for it.
			if err := m.reapplyTunnel(ctx, spec.GetNetworkTarget()); err != nil {
				m.logf("%s: after removing %s: %v", spec.GetNetworkTarget(), spec.GetName(), err)
			}
		}
		return &maitrev1.ServiceStatus{Spec: &spec, State: maitrev1.ServiceStatus_STATE_REMOVING}, nil
	default:
		return nil, fmt.Errorf("unknown action")
	}
	return m.Status(ctx, &spec)
}

// Update pulls the newest image behind the Service's tag; when its digest
// differs from the pinned one, the Service is re-rendered on the new digest
// and restarted, with the old digest kept for Rollback. Returns whether
// anything changed.
func (m *Manager) Update(ctx context.Context, spec *maitrev1.ServiceSpec) (bool, error) {
	// Before the network: a Service this Node would refuse to apply is not
	// one to spend a download on (decision 315).
	if err := m.applicable(ctx, spec); err != nil {
		return false, err
	}
	changed := false
	for _, h := range imageHolders(spec) {
		tagRef, _, _ := strings.Cut(*h.image, "@")
		if tagRef == "" {
			continue
		}
		// Ask the registry what the tag names rather than download to find
		// out. Nothing moves when the answer is the digest already pinned.
		if m.pinnedIsCurrent(ctx, *h.image) {
			continue
		}
		if err := m.ensureRoomToPull(ctx); err != nil {
			return false, err
		}
		if err := m.countPull(spec.GetName(), func() error {
			_, err := m.run.Run(ctx, "podman", "pull", "-q", tagRef)
			return err
		}); err != nil {
			return false, fmt.Errorf("pull %s: %w", tagRef, err)
		}
		out, err := m.run.Run(ctx, "podman", "image", "inspect", "--format", "{{.Digest}}", tagRef)
		if err != nil {
			return false, err
		}
		digest := strings.TrimSpace(out)
		newRef := tagRef + "@" + digest
		if newRef == *h.image || digest == "" {
			continue
		}
		*h.prev = *h.image
		*h.image = newRef
		changed = true
	}
	if !changed {
		return false, nil
	}
	if err := m.apply(ctx, spec); err != nil {
		return false, err
	}
	if _, err := m.store.Put(ctx, KindService, spec.GetName(), spec, false); err != nil {
		return false, err
	}
	if _, err := m.run.Run(ctx, "systemctl", "restart", "--no-block", lifecycleUnit(spec)); err != nil {
		return false, err
	}
	return true, nil
}

// AutoUpdate runs Update on every Service of this Node that asked for it.
// Decision 318: another Node's Services are not this Node's to pull for, and
// decision 315: a Service that is not running is left until it is started,
// so a daily check costs nothing for what nothing is waiting on.
func (m *Manager) AutoUpdate(ctx context.Context) {
	specs, err := m.mine(ctx)
	if err != nil {
		return
	}
	for _, sp := range specs {
		if !sp.GetAutoUpdate() {
			continue
		}
		if st, err := m.Status(ctx, sp); err != nil || st.GetState() != maitrev1.ServiceStatus_STATE_RUNNING {
			continue
		}
		if changed, err := m.Update(ctx, sp); err != nil && m.cfg.Log != nil {
			m.cfg.Log.Warn("service auto-update", "service", sp.GetName(), "err", err)
		} else if changed && m.cfg.Log != nil {
			m.cfg.Log.Info("service updated", "service", sp.GetName(), "image", sp.GetImage())
		}
	}
}

// checkMounts refuses extra folders whose container path is not absolute or
// is used twice (by a volume or another folder).
func checkMounts(vols []*maitrev1.ServiceVolume, mounts []*maitrev1.ServiceMount) error {
	paths := map[string]bool{}
	for _, v := range vols {
		paths[v.GetPath()] = true
	}
	for _, mt := range mounts {
		p := mt.GetPath()
		if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, ":\n") || p == "/" {
			return fmt.Errorf("%q is not a path inside the container", p)
		}
		if paths[p] {
			return fmt.Errorf("%s is already used inside the container", p)
		}
		paths[p] = true
	}
	return nil
}

// SetFolders points volumes at picked folders and sets the extra folders
// (decisions 170, 171, 174), then applies and restarts the Service if it was
// running. Folders no longer used are left as they are.
func (m *Manager) SetFolders(ctx context.Context, req *maitrev1.SetFoldersRequest) (*maitrev1.ServiceStatus, error) {
	var spec maitrev1.ServiceSpec
	if _, err := m.store.Get(ctx, KindService, req.GetName(), &spec); err != nil {
		return nil, ErrNotFound
	}
	if spec.GetNodeId() != "" && spec.GetNodeId() != m.store.NodeID() {
		return nil, fmt.Errorf("%s runs on another Node; ask that one", req.GetName())
	}
	has := map[string]bool{}
	for _, v := range spec.GetVolumes() {
		has[v.GetName()] = true
	}
	for name := range req.GetVolumeFolders() {
		if !has[name] {
			return nil, fmt.Errorf("%s has no volume %s", req.GetName(), name)
		}
	}
	for name, space := range req.GetVolumeSpaces() {
		if !has[name] {
			return nil, fmt.Errorf("%s has no volume %s", req.GetName(), name)
		}
		if _, picked := req.GetVolumeFolders()[name]; !picked && space != spec.GetVolumeSpaces()[name] {
			return nil, fmt.Errorf("volume %s keeps its own folder; move it with MoveVolume to change its Space", name)
		}
	}
	if err := checkMounts(spec.GetVolumes(), req.GetMounts()); err != nil {
		return nil, err
	}
	if spec.VolumeSpaces == nil {
		spec.VolumeSpaces = map[string]string{}
	}
	for name, space := range req.GetVolumeSpaces() {
		spec.VolumeSpaces[name] = space
	}
	spec.VolumeFolders = req.GetVolumeFolders()
	spec.Mounts = req.GetMounts()
	if err := m.apply(ctx, &spec); err != nil {
		return nil, err
	}
	if _, err := m.store.Put(ctx, KindService, spec.GetName(), &spec, false); err != nil {
		return nil, err
	}
	// try-restart: only if it was running (a stopped Service stays stopped).
	if _, err := m.run.Run(ctx, "systemctl", "try-restart", "--no-block", lifecycleUnit(&spec)); err != nil {
		return nil, err
	}
	return m.Status(ctx, &spec)
}

// SetSettings replaces a Service's settings, and its auto update when
// given (decision 193). The unit is rendered from the new settings before
// anything is saved, so a setting the container definition needs cannot
// go missing; the Service restarts if it was running.
func (m *Manager) SetSettings(ctx context.Context, req *maitrev1.SetSettingsRequest) (*maitrev1.ServiceStatus, error) {
	var spec maitrev1.ServiceSpec
	if _, err := m.store.Get(ctx, KindService, req.GetName(), &spec); err != nil {
		return nil, ErrNotFound
	}
	if spec.GetNodeId() != "" && spec.GetNodeId() != m.store.NodeID() {
		return nil, fmt.Errorf("%s runs on another Node; ask that one", req.GetName())
	}
	settings := map[string]string{}
	for k, v := range req.GetSettings() {
		// Decision 189: values land in unit file lines.
		if !settingKey.MatchString(k) {
			return nil, fmt.Errorf("setting name %q: use letters, digits and underscores", k)
		}
		if strings.ContainsAny(v, "\n\r\x00") {
			return nil, fmt.Errorf("setting %s has a line break", k)
		}
		settings[k] = v
	}
	if tz, ok := settings["TZ"]; ok && tz == "" {
		settings["TZ"] = timezone.Auto
	}
	spec.Settings = settings
	if req.AutoUpdate != nil {
		spec.AutoUpdate = req.GetAutoUpdate()
	}
	if err := m.apply(ctx, &spec); err != nil {
		return nil, err
	}
	if _, err := m.store.Put(ctx, KindService, spec.GetName(), &spec, false); err != nil {
		return nil, err
	}
	if _, err := m.run.Run(ctx, "systemctl", "try-restart", "--no-block", lifecycleUnit(&spec)); err != nil {
		return nil, err
	}
	return m.Status(ctx, &spec)
}

// SetImage runs a Service on another image (decision 195): the reference
// is pulled and pinned to its digest (a digest given is kept as it is),
// the old image kept for Rollback, and the Service restarted if it was
// running. Another repository needs its own grants (189); the render
// refuses until a Trusted Device allows them, and nothing changes.
func (m *Manager) SetImage(ctx context.Context, name, image, component string) (*maitrev1.ServiceStatus, error) {
	image = strings.TrimSpace(image)
	if !imageRef.MatchString(image) {
		return nil, fmt.Errorf("%q is not an image reference such as docker.io/library/nginx:1.27", image)
	}
	var spec maitrev1.ServiceSpec
	if _, err := m.store.Get(ctx, KindService, name, &spec); err != nil {
		return nil, ErrNotFound
	}
	if spec.GetNodeId() != "" && spec.GetNodeId() != m.store.NodeID() {
		return nil, fmt.Errorf("%s runs on another Node; ask that one", name)
	}
	// Which container(s) the new image applies to: the one named, or in a
	// multi-container Service every component already on the same repository.
	var targets []imageHolder
	holders := imageHolders(&spec)
	switch {
	case component != "":
		for _, h := range holders {
			if h.name == component {
				targets = append(targets, h)
			}
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("%s has no component %q", name, component)
		}
	case isMulti(&spec):
		repo := ImageRepository(image)
		for _, h := range holders {
			if ImageRepository(*h.image) == repo {
				targets = append(targets, h)
			}
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("%s has no component running %s; name one with component", name, repo)
		}
	default:
		targets = holders
	}
	if err := m.applicable(ctx, &spec); err != nil {
		return nil, err
	}
	pinned := image
	if !strings.Contains(image, "@") {
		if err := m.ensureRoomToPull(ctx); err != nil {
			return nil, err
		}
		if err := m.countPull(name, func() error {
			_, err := m.run.Run(ctx, "podman", "pull", "-q", image)
			return err
		}); err != nil {
			return nil, fmt.Errorf("pull %s: %w", image, err)
		}
		out, err := m.run.Run(ctx, "podman", "image", "inspect", "--format", "{{.Digest}}", image)
		if err != nil {
			return nil, err
		}
		digest := strings.TrimSpace(out)
		if digest == "" {
			return nil, fmt.Errorf("%s has no digest after pulling", image)
		}
		pinned = image + "@" + digest
	} else if !m.pinnedIsCurrent(ctx, image) {
		if err := m.ensureRoomToPull(ctx); err != nil {
			return nil, err
		}
		if err := m.countPull(name, func() error {
			_, err := m.run.Run(ctx, "podman", "pull", "-q", image)
			return err
		}); err != nil {
			return nil, fmt.Errorf("pull %s: %w", image, err)
		}
	}
	changed := false
	for _, h := range targets {
		if *h.image == pinned {
			continue
		}
		*h.prev = *h.image
		*h.image = pinned
		changed = true
	}
	if !changed {
		return m.Status(ctx, &spec)
	}
	if err := m.apply(ctx, &spec); err != nil {
		return nil, err
	}
	if _, err := m.store.Put(ctx, KindService, name, &spec, false); err != nil {
		return nil, err
	}
	if _, err := m.run.Run(ctx, "systemctl", "try-restart", "--no-block", lifecycleUnit(&spec)); err != nil {
		return nil, err
	}
	return m.Status(ctx, &spec)
}

// imageRef is a registry/path[:tag][@sha256:digest] reference: lower case
// path parts, nothing that could break a unit file line (189).
var imageRef = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*(\.[a-z0-9-]+)*(:[0-9]{1,5})?(/[a-z0-9]+([._-]+[a-z0-9]+)*)+(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(@sha256:[a-f0-9]{64})?$`)

var settingKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// SetExposure changes where a Service is reachable. Infrastructure
// Services never go public (the plan's rule: they bind the Node itself).
func (m *Manager) SetExposure(ctx context.Context, name, exposure, publicHost string) (*maitrev1.ServiceStatus, error) {
	var spec maitrev1.ServiceSpec
	if _, err := m.store.Get(ctx, KindService, name, &spec); err != nil {
		return nil, ErrNotFound
	}
	host, err := m.checkExposure(ctx, name, &spec, exposure, publicHost)
	if err != nil {
		return nil, err
	}
	spec.Exposure = exposure
	spec.PublicHost = host
	if _, err := m.store.Put(ctx, KindService, name, &spec, false); err != nil {
		return nil, err
	}
	if m.OnExposure != nil {
		m.OnExposure()
	}
	return m.Status(ctx, &spec)
}

// checkExposure says whether spec may have this exposure, at install or on
// a change, and gives the public host as it is kept: lower-case, and empty
// unless the exposure is "direct".
func (m *Manager) checkExposure(ctx context.Context, name string, spec *maitrev1.ServiceSpec, exposure, publicHost string) (string, error) {
	switch exposure {
	case "private", "remote", "public":
		publicHost = ""
	case "direct":
		publicHost = strings.ToLower(strings.TrimSpace(publicHost))
		if !direct.ValidHost(publicHost) {
			return "", fmt.Errorf("a public address needs a full host name such as photos.example.com")
		}
	default:
		return "", fmt.Errorf("exposure must be private, remote, public or direct")
	}
	if (exposure == "public" || exposure == "direct") && spec.GetInfrastructure() {
		return "", fmt.Errorf("%s is network infrastructure and cannot be public", name)
	}
	if exposure != "private" && len(pagesOf(spec)) == 0 {
		return "", fmt.Errorf("%s has no Page to expose", name)
	}
	if publicHost != "" {
		others, _ := store.ListLive(ctx, m.store, KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
		for _, o := range others {
			if o.GetName() != name && o.GetPublicHost() == publicHost {
				return "", fmt.Errorf("%s already answers at %s", o.GetName(), publicHost)
			}
		}
	}
	return publicHost, nil
}

// List returns every Service on this Node with status.
// mine is the live Services this Node owns. Decision 318: a Service Record
// is Fleet-wide, so every Node stores every Node's Services; anything that
// acts on a Service, reports it, or spends bandwidth for it must take its
// own. Checks that are Fleet-wide by nature — an address unique across every
// Node, a name claim — keep using store.ListLive directly.
func (m *Manager) mine(ctx context.Context) ([]*maitrev1.ServiceSpec, error) {
	specs, err := store.ListLive(ctx, m.store, KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
	if err != nil {
		return nil, err
	}
	self := m.store.NodeID()
	out := make([]*maitrev1.ServiceSpec, 0, len(specs))
	for _, s := range specs {
		if s.GetNodeId() == self {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *Manager) List(ctx context.Context) ([]*maitrev1.ServiceStatus, error) {
	specs, err := m.mine(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*maitrev1.ServiceStatus, 0, len(specs))
	for _, s := range specs {
		st, err := m.Status(ctx, s)
		if err != nil {
			st = &maitrev1.ServiceStatus{Spec: s, State: maitrev1.ServiceStatus_STATE_FAILED, Message: err.Error()}
		}
		out = append(out, st)
	}
	return out, nil
}

// Status asks systemd about the unit and probes health.
// SetPortForwarding (decision 115) turns router port forwarding of a
// Service's host_ports on or off. Only a Service that declares host ports
// can ask; network infrastructure never does.
func (m *Manager) SetPortForwarding(ctx context.Context, name string, enabled bool) (*maitrev1.ServiceStatus, error) {
	var spec maitrev1.ServiceSpec
	if _, err := m.store.Get(ctx, KindService, name, &spec); err != nil {
		return nil, ErrNotFound
	}
	if enabled && len(spec.GetHostPorts()) == 0 {
		return nil, fmt.Errorf("%s has no ports to forward", name)
	}
	if enabled && spec.GetInfrastructure() {
		return nil, fmt.Errorf("%s is network infrastructure and stays at home", name)
	}
	spec.ForwardPorts = enabled
	if _, err := m.store.Put(ctx, KindService, name, &spec, false); err != nil {
		return nil, err
	}
	if m.OnExposure != nil {
		m.OnExposure()
	}
	return m.Status(ctx, &spec)
}

func (m *Manager) Status(ctx context.Context, spec *maitrev1.ServiceSpec) (*maitrev1.ServiceStatus, error) {
	st := &maitrev1.ServiceStatus{Spec: spec}
	// Decision 317: a Service that shares another's network namespace says
	// whose, so the Console can show the figure marked as shared rather
	// than splitting it dishonestly or hiding it.
	if owner := netOwner(spec); owner != spec.GetName() && owner != "" {
		st.NetworkSharedWith = owner
	}
	if m.cfg.Forward != nil {
		if spec.GetForwardPorts() {
			st.ForwardState = m.cfg.Forward(spec.GetName())
		} else if spec.GetExposure() == direct.Exposure {
			st.ForwardState = m.cfg.Forward(direct.ForwardOwner)
		}
	}
	if isMulti(spec) {
		if err := m.multiStatus(ctx, spec, st); err != nil {
			return nil, err
		}
	} else {
		state, restarts, started, msg, err := m.showUnit(ctx, spec.GetName()+".service")
		if err != nil {
			return nil, fmt.Errorf("systemctl show: %w", err)
		}
		st.State, st.Restarts, st.StartedUnixMs, st.Message = state, restarts, started, msg
	}
	// A Service on a Tunnel stops with it (BindsTo): the Tunnel's failure
	// is its own, in the Console's words.
	if spec.GetNetworkKind() == NetworkTunnel && st.State != maitrev1.ServiceStatus_STATE_RUNNING {
		if tstate, _, _, tmsg, err := m.showUnit(ctx, spec.GetNetworkTarget()+".service"); err == nil && tstate == maitrev1.ServiceStatus_STATE_FAILED {
			st.State = tstate
			st.Message = "through " + spec.GetNetworkTarget() + ": " + tmsg
		}
	}
	for i, p := range pagesOf(spec) {
		ps := &maitrev1.PageStatus{Name: p.GetName()}
		host := PageHost(spec, p)
		ps.Url = fmt.Sprintf("http://%s.%s%s", host, m.cfg.Suffix, p.GetPath())
		if m.cfg.Remote != nil {
			if u, state := m.cfg.Remote(host); u != "" || state != "" {
				if u != "" {
					u += p.GetPath()
				}
				ps.RemoteUrl, ps.RemoteState = u, state
			}
		}
		if st.State == maitrev1.ServiceStatus_STATE_RUNNING && p.GetHostPort() != 0 {
			ps.Healthy = m.probe(ctx, p)
		}
		st.Pages = append(st.Pages, ps)
		if i == 0 {
			st.Url, st.RemoteUrl, st.RemoteState, st.Healthy = ps.Url, ps.RemoteUrl, ps.RemoteState, ps.Healthy
		}
	}
	return st, nil
}

// showUnit reads one systemd unit's state, restart count, start time and, on
// failure, a reason.
func (m *Manager) showUnit(ctx context.Context, unit string) (state maitrev1.ServiceStatus_State, restarts uint32, startedUnixMs int64, message string, err error) {
	out, err := m.run.Run(ctx, "systemctl", "show", unit,
		"-p", "LoadState", "-p", "ActiveState", "-p", "SubState", "-p", "NRestarts", "-p", "Result", "-p", "ExecMainStartTimestamp")
	if err != nil {
		return 0, 0, 0, "", err
	}
	props := parseProps(out)
	// Decision 318: systemd answers for a unit it does not have with
	// ActiveState=inactive, which would read as a Service deliberately
	// stopped. The Record is this Node's but nothing was ever written for
	// it, so say so: Start cannot help, applying it again can.
	if props["LoadState"] == "not-found" {
		return maitrev1.ServiceStatus_STATE_STOPPED, 0, 0, "not installed on this Node", nil
	}
	switch props["ActiveState"] {
	case "active":
		state = maitrev1.ServiceStatus_STATE_RUNNING
	case "activating", "reloading":
		state = maitrev1.ServiceStatus_STATE_STARTING
	case "failed":
		state = maitrev1.ServiceStatus_STATE_FAILED
		message = "failed (" + props["Result"] + ")"
	case "deactivating":
		state = maitrev1.ServiceStatus_STATE_REMOVING
	default:
		state = maitrev1.ServiceStatus_STATE_STOPPED
	}
	if n, e := strconv.Atoi(props["NRestarts"]); e == nil {
		restarts = uint32(n)
	}
	if t, e := time.Parse("Mon 2006-01-02 15:04:05 MST", props["ExecMainStartTimestamp"]); e == nil {
		startedUnixMs = t.UnixMilli()
	}
	return state, restarts, startedUnixMs, message, nil
}

// multiStatus fills a multi-container Service's per-component statuses and
// rolls them up into the Service-wide state (plan C1). Failed wins and names
// the component; otherwise starting, then running only when every component
// runs. Restarts sum; the Service's start is its latest component's.
func (m *Manager) multiStatus(ctx context.Context, spec *maitrev1.ServiceSpec, st *maitrev1.ServiceStatus) error {
	anyFailed, anyStarting, allRunning := false, false, true
	for _, c := range components(spec) {
		state, restarts, started, msg, err := m.showUnit(ctx, unitBase(spec.GetName(), c)+".service")
		if err != nil {
			return fmt.Errorf("systemctl show %s: %w", c.Name, err)
		}
		st.Components = append(st.Components, &maitrev1.ComponentStatus{
			Name: c.Name, State: state, Restarts: restarts, Message: msg,
			Healthy: state == maitrev1.ServiceStatus_STATE_RUNNING,
		})
		st.Restarts += restarts
		if started > st.StartedUnixMs {
			st.StartedUnixMs = started
		}
		switch state {
		case maitrev1.ServiceStatus_STATE_FAILED:
			anyFailed = true
			if st.Message == "" {
				st.Message = c.Name + " " + msg
			}
		case maitrev1.ServiceStatus_STATE_STARTING:
			anyStarting = true
			allRunning = false
		case maitrev1.ServiceStatus_STATE_RUNNING:
		default:
			allRunning = false
		}
	}
	switch {
	case anyFailed:
		st.State = maitrev1.ServiceStatus_STATE_FAILED
	case anyStarting:
		st.State = maitrev1.ServiceStatus_STATE_STARTING
	case allRunning:
		st.State = maitrev1.ServiceStatus_STATE_RUNNING
	default:
		st.State = maitrev1.ServiceStatus_STATE_STOPPED
	}
	return nil
}

func (m *Manager) probe(ctx context.Context, p *maitrev1.ServicePage) bool {
	path := "/"
	if p.GetHealthHttp() != "" {
		path = p.GetHealthHttp()
	} else if p.GetPath() != "" {
		path = p.GetPath()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", p.GetHostPort(), path), nil)
	if err != nil {
		return false
	}
	res, err := m.cfg.HTTPClient.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode < 500
}

// Logs returns the last n journal lines for a Service. For a multi-container
// Service, component selects one container's journal; empty interleaves them
// all (journalctl merges multiple -u by time).
func (m *Manager) Logs(ctx context.Context, name, component string, n uint32) ([]string, error) {
	var spec maitrev1.ServiceSpec
	if _, err := m.store.Get(ctx, KindService, name, &spec); err != nil {
		return nil, ErrNotFound
	}
	if n == 0 {
		n = 200
	}
	args := []string{"-n", strconv.Itoa(int(n)), "--no-pager", "-o", "short-iso"}
	units, err := LogUnits(&spec, component)
	if err != nil {
		return nil, err
	}
	for _, u := range units {
		args = append(args, "-u", u)
	}
	out, err := m.run.Run(ctx, "journalctl", args...)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	return lines, nil
}

// LogUnits is the journal unit(s) for a Logs request: the one Service unit,
// one named component, or every component.
func LogUnits(spec *maitrev1.ServiceSpec, component string) ([]string, error) {
	if !isMulti(spec) {
		return []string{spec.GetName() + ".service"}, nil
	}
	if component != "" {
		for _, c := range spec.GetComponents() {
			if c.GetName() == component {
				return []string{spec.GetName() + "-" + component + ".service"}, nil
			}
		}
		return nil, fmt.Errorf("%s has no component %q", spec.GetName(), component)
	}
	return componentServiceUnits(spec), nil
}

// WebHostPort returns the loopback port of a Service's first Page.
func WebHostPort(spec *maitrev1.ServiceSpec) int {
	if pages := pagesOf(spec); len(pages) > 0 {
		return int(pages[0].GetHostPort())
	}
	return 0
}

func parseProps(out string) map[string]string {
	props := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = strings.TrimSpace(v)
		}
	}
	return props
}

// withTimezone is spec with a TZ setting of "auto" (or none) resolved to
// the Fleet's time zone, else this Node's (decision 282). The stored spec
// keeps "auto", so a later change of the Fleet's zone reaches the unit at
// the next render; see FollowTimezone.
func (m *Manager) withTimezone(ctx context.Context, spec *maitrev1.ServiceSpec) *maitrev1.ServiceSpec {
	if !followsTimezone(spec) {
		return spec
	}
	out := proto.Clone(spec).(*maitrev1.ServiceSpec)
	out.Settings["TZ"] = timezone.Resolve(ctx, m.store)
	return out
}

// followsTimezone reports whether a Service's TZ setting follows the Fleet's.
func followsTimezone(spec *maitrev1.ServiceSpec) bool {
	tz, ok := spec.GetSettings()["TZ"]
	return ok && (tz == timezone.Auto || tz == "")
}

// FollowTimezone renders again, and restarts if running, every Service on
// this Node whose TZ setting is "auto", after the Fleet's time zone
// changed (decision 282). A Service that fails to render is logged and
// left as it is.
func (m *Manager) FollowTimezone(ctx context.Context) {
	specs, err := store.ListLive(ctx, m.store, KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
	if err != nil {
		return
	}
	for _, spec := range specs {
		if spec.GetNodeId() != m.store.NodeID() || !followsTimezone(spec) {
			continue
		}
		if err := m.apply(ctx, spec); err != nil {
			m.logf("%s: time zone: %v", spec.GetName(), err)
			continue
		}
		if _, err := m.run.Run(ctx, "systemctl", "try-restart", "--no-block", lifecycleUnit(spec)); err != nil {
			m.logf("%s: restart for the time zone: %v", spec.GetName(), err)
		}
	}
}
