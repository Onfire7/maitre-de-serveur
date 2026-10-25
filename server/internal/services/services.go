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
	"strconv"
	"strings"
	"text/template"
	"time"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
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
	// UnitsDir is where Quadlet looks for *.container (default /etc/containers/systemd).
	UnitsDir string
	// PoolDir is the default Tier's Pool; volumes live at PoolDir/<service>/<volume>.
	PoolDir string
	// HostPortBase..HostPortBase+9999 are loopback ports the agent assigns to
	// Services' web ports for Caddy and health checks.
	HostPortBase int
	// Suffix is the mDNS/Caddy domain; DefaultSuffix unless overridden.
	Suffix string
	// HTTPClient probes health; nil means a 2-second default.
	HTTPClient *http.Client
}

// DefaultSuffix is the domain Services are reachable under on the LAN:
// <name>.local. Two labels, because Linux desktops' default mDNS resolver
// (nss-mdns mdns_minimal) rejects deeper names such as <name>.maitre.local
// (decision 75).
const DefaultSuffix = "local"

// Manager implements Service operations for one Node.
type Manager struct {
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
	spec.VolumeTiers = map[string]string{}
	for _, v := range spec.GetVolumes() {
		spec.VolumeTiers[v.GetName()] = "default"
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

// apply renders the Quadlet unit, creates volume directories, reloads systemd.
func (m *Manager) apply(ctx context.Context, spec *maitrev1.ServiceSpec) error {
	vols := map[string]string{}
	for _, v := range spec.GetVolumes() {
		dir := filepath.Join(m.cfg.PoolDir, spec.GetName(), v.GetName())
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		vols[v.GetName()] = dir
	}
	unit, err := Render(spec, vols)
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

// Render produces the full Quadlet unit for a Service.
func Render(spec *maitrev1.ServiceSpec, volumes map[string]string) ([]byte, error) {
	tmpl, err := template.New(spec.GetName()).Option("missingkey=error").Parse(spec.GetQuadlet())
	if err != nil {
		return nil, fmt.Errorf("quadlet template: %w", err)
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
		return nil, fmt.Errorf("quadlet template: %w", err)
	}
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
	if spec.GetWebPort() != 0 && spec.GetWebHostPort() != 0 {
		fmt.Fprintf(&out, "PublishPort=127.0.0.1:%d:%d\n", spec.GetWebHostPort(), spec.GetWebPort())
	}
	out.WriteString("AutoUpdate=registry\nLabel=dev.maitre.service=" + spec.GetName() + "\n")
	out.WriteString("\n[Service]\nRestart=on-failure\nRestartSec=5\nTimeoutStartSec=900\n")
	out.WriteString("\n[Install]\nWantedBy=multi-user.target default.target\n")
	return out.Bytes(), nil
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
	case maitrev1.ControlServiceRequest_ACTION_REMOVE:
		_, _ = m.run.Run(ctx, "systemctl", "stop", unit)
		if err := os.Remove(filepath.Join(m.cfg.UnitsDir, spec.Name+".container")); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if _, err := m.run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
			return nil, err
		}
		if req.GetDeleteData() && m.cfg.PoolDir != "" {
			if err := os.RemoveAll(filepath.Join(m.cfg.PoolDir, spec.Name)); err != nil {
				return nil, err
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
func (m *Manager) Status(ctx context.Context, spec *maitrev1.ServiceSpec) (*maitrev1.ServiceStatus, error) {
	out, err := m.run.Run(ctx, "systemctl", "show", spec.GetName()+".service",
		"-p", "ActiveState", "-p", "SubState", "-p", "NRestarts", "-p", "Result", "-p", "ExecMainStartTimestamp")
	if err != nil {
		return nil, fmt.Errorf("systemctl show: %w", err)
	}
	props := parseProps(out)
	st := &maitrev1.ServiceStatus{Spec: spec}
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
