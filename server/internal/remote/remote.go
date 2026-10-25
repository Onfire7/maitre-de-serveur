// Package remote exposes Services beyond the LAN (Milestone 5, decisions
// 109 to 111): every Remote or Public Service gets its own tsnet node named
// after it, serving HTTPS with a certificate Tailscale issues and proxying
// to the Service's loopback port. Public adds Tailscale Funnel.
package remote

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/tailnet"
)

type served struct {
	node     *tailnet.Node
	exposure string
	port     uint32
	cancel   context.CancelFunc
	mu       sync.Mutex
	state    string
	url      string
}

// Manager keeps one tsnet node per exposed Service.
type Manager struct {
	st       *store.Store
	log      *slog.Logger
	stateDir string

	mu    sync.Mutex
	nodes map[string]*served
}

// New builds a Manager; stateDir is the config partition.
func New(st *store.Store, log *slog.Logger, stateDir string) *Manager {
	return &Manager{st: st, log: log, stateDir: stateDir, nodes: map[string]*served{}}
}

// Reconcile starts nodes for Services that need one and stops the rest.
func (m *Manager) Reconcile(ctx context.Context, specs []*maitrev1.ServiceSpec) {
	var cfg maitrev1.TailscaleConfig
	configured := true
	if _, err := m.st.Get(ctx, tailnet.KindTailscale, "fleet", &cfg); err != nil {
		configured = false
	}
	want := map[string]*maitrev1.ServiceSpec{}
	for _, sp := range specs {
		if sp.GetWebHostPort() != 0 && (sp.GetExposure() == "remote" || sp.GetExposure() == "public") {
			want[sp.GetName()] = sp
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, s := range m.nodes {
		sp, ok := want[name]
		if !ok || sp.GetExposure() != s.exposure || sp.GetWebHostPort() != s.port {
			s.cancel()
			_ = s.node.Close()
			delete(m.nodes, name)
		}
	}
	for name, sp := range want {
		if _, ok := m.nodes[name]; ok {
			continue
		}
		if !configured {
			m.log.Warn("service wants remote exposure but the Fleet has no Tailscale client", "service", name)
			continue
		}
		m.start(ctx, &cfg, sp)
	}
}

func (m *Manager) start(ctx context.Context, cfg *maitrev1.TailscaleConfig, sp *maitrev1.ServiceSpec) {
	name := sp.GetName()
	dir := tailnet.StateDir(m.stateDir, "svc-"+name)
	key := ""
	if _, err := os.Stat(filepath.Join(dir, "tailscaled.state")); err != nil {
		k, err := tailnet.AuthKey(ctx, cfg, false)
		if err != nil {
			m.log.Warn("tailscale auth key", "service", name, "err", err)
			return
		}
		key = k
	}
	nctx, cancel := context.WithCancel(ctx)
	s := &served{node: tailnet.Start(nctx, m.log, dir, name, key), exposure: sp.GetExposure(), port: sp.GetWebHostPort(), cancel: cancel, state: "starting"}
	m.nodes[name] = s
	go m.serve(nctx, s, name)
}

func (m *Manager) serve(ctx context.Context, s *served, name string) {
	// Wait for the node, then listen: Funnel for public, tailnet TLS otherwise.
	for ctx.Err() == nil {
		st := s.node.Status()
		if st.GetError() != "" {
			s.set("error: "+st.GetError(), "")
		} else if st.GetLoginUrl() != "" && st.GetState() != "Running" {
			s.set("needs login: "+st.GetLoginUrl(), "")
		}
		if st.GetState() == "Running" && st.GetDnsName() != "" {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	if ctx.Err() != nil {
		return
	}
	st := s.node.Status()
	host := strings.TrimSuffix(st.GetDnsName(), ".")
	var ln net.Listener
	var err error
	if s.exposure == "public" {
		ln, err = s.node.ListenFunnel(443)
	} else {
		ln, err = s.node.ListenTLS(443)
	}
	if err != nil {
		s.set("error: "+err.Error(), "")
		m.log.Warn("remote listener", "service", name, "err", err)
		return
	}
	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", s.port))
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Host = host
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", host)
	}
	srv := &http.Server{Handler: proxy, ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); _ = srv.Close() }()
	s.set("up", "https://"+host)
	m.log.Info("service reachable", "service", name, "exposure", s.exposure, "url", "https://"+host)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && ctx.Err() == nil {
		s.set("error: "+err.Error(), "")
	}
}

func (s *served) set(state, u string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state, s.url = state, u
}

// Status reports a Service's remote URL and node state ("" when none).
func (m *Manager) Status(name string) (url, state string) {
	m.mu.Lock()
	s, ok := m.nodes[name]
	m.mu.Unlock()
	if !ok {
		return "", ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.url, s.state
}

// Close stops every node.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, s := range m.nodes {
		s.cancel()
		_ = s.node.Close()
		delete(m.nodes, name)
	}
}
