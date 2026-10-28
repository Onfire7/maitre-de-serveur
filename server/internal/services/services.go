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
	"hash/fnv"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/direct"
	"github.com/Onfire7/maitre-de-serveur/server/internal/layout"
	"github.com/Onfire7/maitre-de-serveur/server/internal/owner"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// KindService is the Record kind for ServiceSpec bodies.
const KindService = "service"

// Network is the Quadlet network unit every Service joins (image/maitre.network).
const Network = "maitre.network"

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
}

// New builds a Manager.
func New(cfg Config, st *store.Store, run Runner) *Manager {
	if cfg.UnitsDir == "" {
		cfg.UnitsDir = "/etc/containers/systemd"
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
	if spec.GetImage() == "" {
		return nil, errors.New("service spec has no image")
	}
	// Decision 189: values land in unit file lines.
	if strings.ContainsAny(spec.GetImage(), "\n\r") {
		return nil, errors.New("the image name has a line break")
	}
	for k, v := range spec.GetSettings() {
		if strings.ContainsAny(k+v, "\n\r") {
			return nil, fmt.Errorf("setting %s has a line break", k)
		}
	}
	if strings.TrimSpace(spec.GetQuadlet()) == "" {
		return nil, errors.New("service spec has no quadlet")
	}
	if _, err := template.New(name).Option("missingkey=error").Parse(spec.GetQuadlet()); err != nil {
		return nil, fmt.Errorf("quadlet template: %w", err)
	}
	if _, err := m.store.Get(ctx, KindService, name, nil); err == nil {
		return nil, ErrExists
	}
	if spec.Settings == nil {
		spec.Settings = map[string]string{}
	}
	if tz, ok := spec.Settings["TZ"]; ok && (tz == "auto" || tz == "") {
		spec.Settings["TZ"] = localTimezone()
	}
	if spec.GetExposure() == "" {
		spec.Exposure = "private"
	}
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
	spec.WebHostPort = 0
	if spec.GetWebPort() != 0 {
		spec.WebHostPort = uint32(m.hostPort(ctx, name))
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
	if _, err := m.store.Put(ctx, KindService, name, spec, false); err != nil {
		return nil, err
	}
	if _, err := m.run.Run(ctx, "systemctl", "start", "--no-block", name+".service"); err != nil {
		return nil, fmt.Errorf("start: %w", err)
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
		return fmt.Errorf("%s: its record is not signed by this Node; remove and install it again", spec.GetName())
	}
	vols, extra, err := m.layout(ctx, spec)
	if err != nil {
		return err
	}
	body, err := renderBody(spec, vols)
	if err != nil {
		return err
	}
	if err := m.checkGrants(ctx, spec, body, managedDirs(vols, extra)); err != nil {
		return err
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
		}
	}
	for i, mt := range spec.GetMounts() {
		if err := share(mt.GetSpaceId(), extra.Mounts[i].Host); err != nil {
			return err
		}
	}
	unit, err := RenderWith(spec, vols, extra)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.cfg.UnitsDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(m.cfg.UnitsDir, spec.Name+".container"), unit, 0o644); err != nil {
		return err
	}
	if _, err := m.run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
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
		vols[v.GetName()] = filepath.Join(base, layout.VolumeDir(spec.GetName(), v.GetName()))
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
func (m *Manager) uninstallUnit(ctx context.Context, name string) {
	_, _ = m.run.Run(ctx, "systemctl", "stop", name+".service")
	if err := os.Remove(filepath.Join(m.cfg.UnitsDir, name+".container")); err == nil {
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
	}
	st, err := m.Status(ctx, &spec)
	if err != nil {
		return "", err
	}
	wasRunning := st.GetState() == maitrev1.ServiceStatus_STATE_RUNNING || st.GetState() == maitrev1.ServiceStatus_STATE_STARTING
	unit := spec.GetName() + ".service"
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
	tmpl, err := template.New(spec.GetName()).Option("missingkey=error").Parse(spec.GetQuadlet())
	if err != nil {
		return "", fmt.Errorf("quadlet template: %w", err)
	}
	data := map[string]any{
		"Image":    spec.GetImage(),
		"Name":     spec.GetName(),
		"Volumes":  volumes,
		"Settings": spec.GetSettings(),
		"Network":  Network,
		"Env":      map[string]string{},
	}
	var body bytes.Buffer
	if err := tmpl.Execute(&body, data); err != nil {
		return "", fmt.Errorf("quadlet template: %w", err)
	}
	return body.String(), nil
}

// RenderWith is Render with picked and extra folders.
func RenderWith(spec *maitrev1.ServiceSpec, volumes map[string]string, extra Extra) ([]byte, error) {
	rendered, err := renderBody(spec, volumes)
	if err != nil {
		return nil, err
	}
	if len(extra.Shared) > 0 {
		hosts := map[string]bool{}
		for name := range extra.Shared {
			hosts[volumes[name]] = true
		}
		rendered = shareVolumes(rendered, hosts)
	}
	// Maître's own [Container] lines go with the template's [Container]
	// lines; any other section the template has (granted, decision 189)
	// follows them.
	container, rest := splitContainer(rendered)
	body := bytes.NewBufferString(container)
	var out bytes.Buffer
	fmt.Fprintf(&out, "# Generated by Maître for Service %q (Catalog entry %q). Do not edit.\n", spec.GetName(), spec.GetCatalogId())
	fmt.Fprintf(&out, "[Unit]\nDescription=Maître Service %s\n", spec.GetName())
	if spec.GetInfrastructure() {
		// Infrastructure Services (DNS, DHCP) must be up before anything that
		// waits for the network.
		out.WriteString("Before=network-online.target\nDefaultDependencies=no\nAfter=basic.target maitre-pool.service\n")
	}
	out.WriteString("\n")
	out.Write(body.Bytes())
	if !strings.HasSuffix(body.String(), "\n") {
		out.WriteString("\n")
	}
	// Decision 189: no gaining privileges through setuid programs, unless
	// the template says otherwise (a protected option).
	if !hasKey(container, "NoNewPrivileges") {
		out.WriteString("NoNewPrivileges=true\n")
	}
	// The container's hostname is the Service's name, not Podman's random
	// id, so apps that name themselves after it (Jellyfin's server name)
	// keep one name across restarts. Not where the template names it or
	// shares the host's or a pod's UTS namespace.
	if !hasKey(container, "HostName") && !hasKey(container, "Pod") && !hasValue(container, "Network", "host") && !hasValue(container, "UTS", "host") {
		fmt.Fprintf(&out, "HostName=%s\n", spec.GetName())
	}
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
	out.WriteString("Label=dev.maitre.service=" + spec.GetName() + "\n")
	if rest != "" {
		out.WriteString("\n" + rest)
		if !strings.HasSuffix(rest, "\n") {
			out.WriteString("\n")
		}
	}
	out.WriteString("\n[Service]\nRestart=on-failure\nRestartSec=5\nTimeoutStartSec=900\n")
	out.WriteString("\n[Install]\nWantedBy=multi-user.target default.target\n")
	return out.Bytes(), nil
}

// splitContainer splits a rendered template into its [Container] lines
// (the header included) and every other section after them.
func splitContainer(body string) (container, rest string) {
	var c, r []string
	in := true
	for _, l := range strings.Split(body, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			in = t == "[Container]"
		}
		if in {
			c = append(c, l)
		} else {
			r = append(r, l)
		}
	}
	return strings.Join(c, "\n"), strings.TrimSpace(strings.Join(r, "\n"))
}

// hasKey reports whether a unit body sets key.
func hasKey(body, key string) bool {
	for _, l := range strings.Split(body, "\n") {
		if k, _, ok := strings.Cut(strings.TrimSpace(l), "="); ok && strings.TrimSpace(k) == key {
			return true
		}
	}
	return false
}

// hasValue reports whether a unit body sets key to value.
func hasValue(body, key, value string) bool {
	for _, l := range strings.Split(body, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok && strings.TrimSpace(k) == key && strings.TrimSpace(v) == value {
			return true
		}
	}
	return false
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

// hostPort picks a stable loopback port for a Service's web port, avoiding
// ports already used by other Services.
func (m *Manager) hostPort(ctx context.Context, name string) int {
	used := map[int]bool{}
	specs, _ := store.ListLive(ctx, m.store, KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
	for _, s := range specs {
		used[int(s.GetWebHostPort())] = true
	}
	h := fnv.New32a()
	h.Write([]byte(name))
	p := m.cfg.HostPortBase + int(h.Sum32()%10000)
	for used[p] {
		p = m.cfg.HostPortBase + (p-m.cfg.HostPortBase+1)%10000
	}
	return p
}

// Control starts, stops, restarts, or removes a Service.
func (m *Manager) Control(ctx context.Context, req *maitrev1.ControlServiceRequest) (*maitrev1.ServiceStatus, error) {
	var spec maitrev1.ServiceSpec
	if _, err := m.store.Get(ctx, KindService, req.GetName(), &spec); err != nil {
		return nil, ErrNotFound
	}
	unit := spec.Name + ".service"
	switch req.GetAction() {
	case maitrev1.ControlServiceRequest_ACTION_START:
		_, err := m.run.Run(ctx, "systemctl", "start", "--no-block", unit)
		if err != nil {
			return nil, err
		}
	case maitrev1.ControlServiceRequest_ACTION_STOP:
		if _, err := m.run.Run(ctx, "systemctl", "stop", unit); err != nil {
			return nil, err
		}
	case maitrev1.ControlServiceRequest_ACTION_RESTART:
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
		if spec.GetPreviousImage() == "" {
			return nil, fmt.Errorf("%s has no previous image to go back to", spec.GetName())
		}
		spec.Image, spec.PreviousImage = spec.GetPreviousImage(), spec.GetImage()
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
		_, _ = m.run.Run(ctx, "systemctl", "stop", unit)
		if err := os.Remove(filepath.Join(m.cfg.UnitsDir, spec.Name+".container")); err != nil && !os.IsNotExist(err) {
			return nil, err
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
	tagRef, _, _ := strings.Cut(spec.GetImage(), "@")
	if _, err := m.run.Run(ctx, "podman", "pull", "-q", tagRef); err != nil {
		return false, fmt.Errorf("pull %s: %w", tagRef, err)
	}
	out, err := m.run.Run(ctx, "podman", "image", "inspect", "--format", "{{.Digest}}", tagRef)
	if err != nil {
		return false, err
	}
	digest := strings.TrimSpace(out)
	newRef := tagRef + "@" + digest
	if newRef == spec.GetImage() || digest == "" {
		return false, nil
	}
	spec.PreviousImage = spec.GetImage()
	spec.Image = newRef
	if err := m.apply(ctx, spec); err != nil {
		return false, err
	}
	if _, err := m.store.Put(ctx, KindService, spec.GetName(), spec, false); err != nil {
		return false, err
	}
	if _, err := m.run.Run(ctx, "systemctl", "restart", "--no-block", spec.GetName()+".service"); err != nil {
		return false, err
	}
	return true, nil
}

// AutoUpdate runs Update on every Service that asked for it.
func (m *Manager) AutoUpdate(ctx context.Context) {
	specs, err := store.ListLive(ctx, m.store, KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
	if err != nil {
		return
	}
	for _, sp := range specs {
		if !sp.GetAutoUpdate() {
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
	if _, err := m.run.Run(ctx, "systemctl", "try-restart", "--no-block", spec.GetName()+".service"); err != nil {
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
	if tz, ok := settings["TZ"]; ok && tz == "auto" {
		settings["TZ"] = localTimezone()
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
	if _, err := m.run.Run(ctx, "systemctl", "try-restart", "--no-block", spec.GetName()+".service"); err != nil {
		return nil, err
	}
	return m.Status(ctx, &spec)
}

// SetImage runs a Service on another image (decision 195): the reference
// is pulled and pinned to its digest (a digest given is kept as it is),
// the old image kept for Rollback, and the Service restarted if it was
// running. Another repository needs its own grants (189); the render
// refuses until a Trusted Device allows them, and nothing changes.
func (m *Manager) SetImage(ctx context.Context, name, image string) (*maitrev1.ServiceStatus, error) {
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
	pinned := image
	if !strings.Contains(image, "@") {
		if _, err := m.run.Run(ctx, "podman", "pull", "-q", image); err != nil {
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
	} else if _, err := m.run.Run(ctx, "podman", "pull", "-q", image); err != nil {
		return nil, fmt.Errorf("pull %s: %w", image, err)
	}
	if pinned == spec.GetImage() {
		return m.Status(ctx, &spec)
	}
	spec.PreviousImage = spec.GetImage()
	spec.Image = pinned
	if err := m.apply(ctx, &spec); err != nil {
		return nil, err
	}
	if _, err := m.store.Put(ctx, KindService, name, &spec, false); err != nil {
		return nil, err
	}
	if _, err := m.run.Run(ctx, "systemctl", "try-restart", "--no-block", name+".service"); err != nil {
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
	switch exposure {
	case "private", "remote", "public":
		publicHost = ""
	case "direct":
		publicHost = strings.ToLower(strings.TrimSpace(publicHost))
		if !direct.ValidHost(publicHost) {
			return nil, fmt.Errorf("a public address needs a full host name such as photos.example.com")
		}
	default:
		return nil, fmt.Errorf("exposure must be private, remote, public or direct")
	}
	var spec maitrev1.ServiceSpec
	if _, err := m.store.Get(ctx, KindService, name, &spec); err != nil {
		return nil, ErrNotFound
	}
	if (exposure == "public" || exposure == "direct") && spec.GetInfrastructure() {
		return nil, fmt.Errorf("%s is network infrastructure and cannot be public", name)
	}
	if exposure != "private" && spec.GetWebPort() == 0 {
		return nil, fmt.Errorf("%s has no web page to expose", name)
	}
	if publicHost != "" {
		others, _ := store.ListLive(ctx, m.store, KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
		for _, o := range others {
			if o.GetName() != name && o.GetPublicHost() == publicHost {
				return nil, fmt.Errorf("%s already answers at %s", o.GetName(), publicHost)
			}
		}
	}
	spec.Exposure = exposure
	spec.PublicHost = publicHost
	if _, err := m.store.Put(ctx, KindService, name, &spec, false); err != nil {
		return nil, err
	}
	if m.OnExposure != nil {
		m.OnExposure()
	}
	return m.Status(ctx, &spec)
}

// List returns every Service on this Node with status.
func (m *Manager) List(ctx context.Context) ([]*maitrev1.ServiceStatus, error) {
	specs, err := store.ListLive(ctx, m.store, KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
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
	out, err := m.run.Run(ctx, "systemctl", "show", spec.GetName()+".service",
		"-p", "ActiveState", "-p", "SubState", "-p", "NRestarts", "-p", "Result", "-p", "ExecMainStartTimestamp")
	if err != nil {
		return nil, fmt.Errorf("systemctl show: %w", err)
	}
	props := parseProps(out)
	st := &maitrev1.ServiceStatus{Spec: spec}
	if m.cfg.Forward != nil {
		if spec.GetForwardPorts() {
			st.ForwardState = m.cfg.Forward(spec.GetName())
		} else if spec.GetExposure() == direct.Exposure {
			st.ForwardState = m.cfg.Forward(direct.ForwardOwner)
		}
	}
	switch props["ActiveState"] {
	case "active":
		st.State = maitrev1.ServiceStatus_STATE_RUNNING
	case "activating", "reloading":
		st.State = maitrev1.ServiceStatus_STATE_STARTING
	case "failed":
		st.State = maitrev1.ServiceStatus_STATE_FAILED
		st.Message = "failed (" + props["Result"] + ")"
	case "deactivating":
		st.State = maitrev1.ServiceStatus_STATE_REMOVING
	default:
		st.State = maitrev1.ServiceStatus_STATE_STOPPED
	}
	if n, err := strconv.Atoi(props["NRestarts"]); err == nil {
		st.Restarts = uint32(n)
	}
	if t, err := time.Parse("Mon 2006-01-02 15:04:05 MST", props["ExecMainStartTimestamp"]); err == nil {
		st.StartedUnixMs = t.UnixMilli()
	}
	if m.cfg.Remote != nil && spec.GetWebPort() != 0 {
		if u, state := m.cfg.Remote(spec.GetName()); u != "" || state != "" {
			if u != "" {
				u += spec.GetWebPath()
			}
			st.RemoteUrl, st.RemoteState = u, state
		}
	}
	if spec.GetWebPort() != 0 {
		st.Url = fmt.Sprintf("http://%s.%s%s", spec.GetName(), m.cfg.Suffix, spec.GetWebPath())
		if st.State == maitrev1.ServiceStatus_STATE_RUNNING && spec.GetWebHostPort() != 0 {
			st.Healthy = m.probe(ctx, spec.GetWebHostPort(), spec)
		}
	}
	return st, nil
}

func (m *Manager) probe(ctx context.Context, hostPort uint32, spec *maitrev1.ServiceSpec) bool {
	path := "/"
	if spec.GetHealthHttp() != "" {
		path = spec.GetHealthHttp()
	} else if spec.GetWebPath() != "" {
		path = spec.GetWebPath()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", hostPort, path), nil)
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

// Logs returns the last n journal lines for a Service.
func (m *Manager) Logs(ctx context.Context, name string, n uint32) ([]string, error) {
	if _, err := m.store.Get(ctx, KindService, name, nil); err != nil {
		return nil, ErrNotFound
	}
	if n == 0 {
		n = 200
	}
	out, err := m.run.Run(ctx, "journalctl", "-u", name+".service", "-n", strconv.Itoa(int(n)), "--no-pager", "-o", "short-iso")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	return lines, nil
}

// WebHostPort returns the loopback port Caddy should proxy to for a Service.
func WebHostPort(spec *maitrev1.ServiceSpec) int { return int(spec.GetWebHostPort()) }

func parseProps(out string) map[string]string {
	props := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = strings.TrimSpace(v)
		}
	}
	return props
}

func localTimezone() string {
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(link, "zoneinfo/"); i >= 0 {
			return link[i+len("zoneinfo/"):]
		}
	}
	return "UTC"
}
