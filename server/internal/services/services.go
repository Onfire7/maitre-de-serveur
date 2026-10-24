// Package services turns a Catalog entry plus the user's choices into a
// running Podman container managed by systemd (Quadlet), and reports on it.
// It is the only code that touches Quadlet files or systemd.
package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
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
	// UnitsDir is where Quadlet looks for *.container (default /etc/containers/systemd).
	UnitsDir string
	// PoolDir is the default Tier's Pool; volumes live at PoolDir/<service>/<volume>.
	PoolDir string
	// HostPortBase..HostPortBase+9999 are loopback ports the agent assigns to
	// Services' web ports for Caddy and health checks.
	HostPortBase int
	// Suffix is the mDNS/Caddy domain, e.g. "maitre.local".
	Suffix string
	// HTTPClient probes health; nil means a 2-second default.
	HTTPClient *http.Client
}

// Manager implements Service operations for one Node.
type Manager struct {
	cfg     Config
	store   *store.Store
	catalog func() *catalog.Index
	run     Runner
}

// New builds a Manager. catalogFn returns the current index (may change as
// the Catalog refreshes).
func New(cfg Config, st *store.Store, catalogFn func() *catalog.Index, run Runner) *Manager {
	if cfg.UnitsDir == "" {
		cfg.UnitsDir = "/etc/containers/systemd"
	}
	if cfg.HostPortBase == 0 {
		cfg.HostPortBase = 20000
	}
	if cfg.Suffix == "" {
		cfg.Suffix = "maitre.local"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 2 * time.Second}
	}
	if run == nil {
		run = ExecRunner{}
	}
	return &Manager{cfg: cfg, store: st, catalog: catalogFn, run: run}
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`)

// ErrExists is returned when a Service name is taken.
var ErrExists = errors.New("a Service with that name already exists")

// ErrNotFound is returned for an unknown Service.
var ErrNotFound = errors.New("no such Service")

// Install creates, writes, and starts a Service.
func (m *Manager) Install(ctx context.Context, req *maitrev1.InstallServiceRequest) (*maitrev1.ServiceStatus, error) {
	idx := m.catalog()
	if idx == nil {
		return nil, errors.New("no Catalog available on this Node")
	}
	entry := idx.Find(req.GetCatalogId())
	if entry == nil {
		return nil, fmt.Errorf("catalog entry %q not found", req.GetCatalogId())
	}
	name := req.GetName()
	if name == "" {
		name = entry.ID
	}
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("service name %q: use lowercase letters, digits and dashes", name)
	}
	if _, err := m.store.Get(ctx, KindService, name, nil); err == nil {
		return nil, ErrExists
	}
	exposure := req.GetExposure()
	if exposure == "" {
		exposure = entry.Exposure
	}
	settings := map[string]string{}
	for _, s := range entry.Settings {
		settings[s.Key] = s.Default
	}
	for k, v := range req.GetSettings() {
		settings[k] = v
	}
	if tz, ok := settings["TZ"]; ok && (tz == "auto" || tz == "") {
		settings["TZ"] = localTimezone()
	}
	spec := &maitrev1.ServiceSpec{
		Name:          name,
		CatalogId:     entry.ID,
		Image:         entry.Image,
		NodeId:        m.store.NodeID(),
		Settings:      settings,
		Exposure:      exposure,
		VolumeTiers:   map[string]string{},
		CreatedUnixMs: time.Now().UnixMilli(),
	}
	for _, v := range entry.Volumes {
		spec.VolumeTiers[v.Name] = "default"
	}
	if entry.Web != nil {
		spec.WebHostPort = uint32(m.hostPort(ctx, name))
	}
	if err := m.apply(ctx, entry, spec); err != nil {
		return nil, err
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
func (m *Manager) apply(ctx context.Context, entry *catalog.Manifest, spec *maitrev1.ServiceSpec) error {
	vols := map[string]string{}
	for _, v := range entry.Volumes {
		dir := filepath.Join(m.cfg.PoolDir, spec.Name, v.Name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		vols[v.Name] = dir
	}
	unit, err := Render(entry, spec, vols, m.cfg.HostPortBase)
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
func Render(entry *catalog.Manifest, spec *maitrev1.ServiceSpec, volumes map[string]string, _ int) ([]byte, error) {
	tmpl, err := template.New(entry.ID).Option("missingkey=error").Parse(entry.Quadlet)
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
	fmt.Fprintf(&out, "# Generated by Maître for Service %q from Catalog entry %q. Do not edit.\n", spec.GetName(), entry.ID)
	fmt.Fprintf(&out, "[Unit]\nDescription=Maître Service %s\n", spec.GetName())
	if entry.Infrastructure {
		// Infrastructure Services (DNS, DHCP) must be up before anything that
		// waits for the network.
		out.WriteString("Before=network-online.target\nDefaultDependencies=no\nAfter=basic.target maitre-pool.service\n")
	}
	out.WriteString("\n")
	out.Write(body.Bytes())
	if !strings.HasSuffix(body.String(), "\n") {
		out.WriteString("\n")
	}
	if entry.Web != nil && spec.GetWebHostPort() != 0 {
		fmt.Fprintf(&out, "PublishPort=127.0.0.1:%d:%d\n", spec.GetWebHostPort(), entry.Web.Port)
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
	if idx := m.catalog(); idx != nil {
		if entry := idx.Find(spec.GetCatalogId()); entry != nil && entry.Web != nil {
			st.Url = fmt.Sprintf("http://%s.%s%s", spec.GetName(), m.cfg.Suffix, entry.Web.Path)
			if st.State == maitrev1.ServiceStatus_STATE_RUNNING && spec.GetWebHostPort() != 0 {
				st.Healthy = m.probe(ctx, spec.GetWebHostPort(), entry)
			}
		}
	}
	return st, nil
}

func (m *Manager) probe(ctx context.Context, hostPort uint32, entry *catalog.Manifest) bool {
	path := "/"
	if entry.Health != nil && entry.Health.HTTP != "" {
		path = entry.Health.HTTP
	} else if entry.Web != nil && entry.Web.Path != "" {
		path = entry.Web.Path
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
