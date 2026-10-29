// Package direct is Public through the user's own domain (decision 112):
// Caddy on the Node answers https://<public_host> with a certificate it
// obtains from Let's Encrypt by itself, and proxies to the Service. This
// package does the two things Caddy cannot: learn the Node's public address
// from a public IP service, so the Console can say what DNS record to make,
// and watch each host so the Console can say what is still missing.
package direct

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// Exposure is the ServiceSpec.exposure value this package serves.
const Exposure = "direct"

// ForwardOwner names the router port mappings (80 and 443) that every
// "direct" Service on a Node shares (decision 115).
const ForwardOwner = "https"

// Services that report the caller's public address, tried in order. Plain
// text bodies, HTTPS, no account.
var ipServices = []string{"https://api.ipify.org", "https://checkip.amazonaws.com", "https://icanhazip.com"}

var hostRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// ValidHost reports whether h is a usable public host name: lower-case
// DNS labels with at least one dot and no trailing dot.
func ValidHost(h string) bool {
	for _, reserved := range []string{".local", ".localhost", ".home.arpa", ".internal", ".lan"} {
		if strings.HasSuffix(h, reserved) {
			return false
		}
	}
	return len(h) <= 253 && hostRE.MatchString(h)
}

type watched struct {
	host  string
	state string
	url   string
}

// Manager tracks the public address and every direct host.
type Manager struct {
	log    *slog.Logger
	client *http.Client
	// TLSAddr is where Caddy's HTTPS listener is probed: 127.0.0.1:443, or
	// the web entry Node's (decision 218), set with SetTLSAddr.
	TLSAddr string

	mu       sync.Mutex
	publicIP string
	hosts    map[string]*watched // by Service name
	wake     chan struct{}
}

// New builds a Manager.
func New(log *slog.Logger) *Manager {
	return &Manager{log: log, client: &http.Client{Timeout: 10 * time.Second}, TLSAddr: "127.0.0.1:443", hosts: map[string]*watched{}, wake: make(chan struct{}, 1)}
}

// SetTLSAddr probes certificates at addr from now on: the Node whose Caddy
// answers the user's domain (decision 218).
func (m *Manager) SetTLSAddr(addr string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TLSAddr = addr
}

// PublicIP is the last address learned, "" when unknown.
func (m *Manager) PublicIP() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.publicIP
}

// Reconcile replaces the set of direct Services and re-checks them soon.
func (m *Manager) Reconcile(specs []*maitrev1.ServiceSpec) {
	m.mu.Lock()
	next := map[string]*watched{}
	for _, sp := range specs {
		if sp.GetExposure() != Exposure || sp.GetPublicHost() == "" || sp.GetWebHostPort() == 0 {
			continue
		}
		if w, ok := m.hosts[sp.GetName()]; ok && w.host == sp.GetPublicHost() {
			next[sp.GetName()] = w
			continue
		}
		next[sp.GetName()] = &watched{host: sp.GetPublicHost(), state: "checking"}
	}
	m.hosts = next
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Status reports a direct Service's URL and state; "" when it is not one.
func (m *Manager) Status(name string) (url, state string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.hosts[name]
	if !ok {
		return "", ""
	}
	return w.url, w.state
}

// Run learns the public address hourly and checks every host every
// half-minute until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	ipTick := time.NewTicker(time.Hour)
	defer ipTick.Stop()
	hostTick := time.NewTicker(30 * time.Second)
	defer hostTick.Stop()
	m.learnIP(ctx)
	for {
		m.checkAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ipTick.C:
			m.learnIP(ctx)
		case <-hostTick.C:
		case <-m.wake:
			// A new host: give Caddy a moment to pick up the config.
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func (m *Manager) learnIP(ctx context.Context) {
	m.mu.Lock()
	need := len(m.hosts) > 0 || m.publicIP == ""
	m.mu.Unlock()
	if !need {
		return
	}
	for _, svc := range ipServices {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, svc, nil)
		if err != nil {
			continue
		}
		res, err := m.client.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64))
		res.Body.Close()
		ip := net.ParseIP(strings.TrimSpace(string(body)))
		if res.StatusCode != 200 || ip == nil {
			continue
		}
		m.mu.Lock()
		changed := m.publicIP != ip.String()
		m.publicIP = ip.String()
		m.mu.Unlock()
		if changed {
			m.log.Info("public address", "ip", ip.String(), "via", svc)
		}
		return
	}
}

func (m *Manager) checkAll(ctx context.Context) {
	m.mu.Lock()
	ip := m.publicIP
	todo := map[string]string{}
	for name, w := range m.hosts {
		todo[name] = w.host
	}
	m.mu.Unlock()
	for name, host := range todo {
		state, url := m.check(ctx, host, ip)
		m.mu.Lock()
		if w, ok := m.hosts[name]; ok && w.host == host {
			if w.state != state {
				m.log.Info("direct host", "service", name, "host", host, "state", state)
			}
			w.state, w.url = state, url
		}
		m.mu.Unlock()
	}
}

// check answers, in the order a user fixes things: does the name resolve,
// does it point here, has Caddy got its certificate.
func (m *Manager) check(ctx context.Context, host, publicIP string) (state, url string) {
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	addrs, err := net.DefaultResolver.LookupHost(rctx, host)
	cancel()
	if err != nil || len(addrs) == 0 {
		if publicIP == "" {
			return "waiting for DNS: no record for " + host + " yet", ""
		}
		return "waiting for DNS: point " + host + " at " + publicIP, ""
	}
	points := publicIP == ""
	for _, a := range addrs {
		if a == publicIP {
			points = true
		}
	}
	if !points {
		return "DNS points at " + addrs[0] + ", not at " + publicIP, ""
	}
	// A certificate Caddy obtained proves the internet reached ports 80 or
	// 443 here; that is the whole chain working.
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	m.mu.Lock()
	addr := m.TLSAddr
	m.mu.Unlock()
	conn, err := d.DialContext(cctx, "tcp", addr)
	cancel()
	if err != nil {
		return "waiting for a certificate; ports 80 and 443 must reach this Node", ""
	}
	conn.Close()
	return "up", "https://" + host
}
