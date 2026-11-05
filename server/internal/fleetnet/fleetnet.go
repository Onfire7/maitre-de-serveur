// Package fleetnet makes every Service of the Fleet reachable by every
// container of the Fleet at <name>.local (ADR 0010). A Service has one
// address on the shared maitre network, the same subnet on every Node. On
// the Node that runs it, its container holds the address. On every other
// Node the agent holds the address itself, on a dummy interface, listens
// on the Service's declared ports there, and carries each connection to
// the running Node's agent over the Nodes' own mutual-TLS link, which
// connects to the container. Nothing is opened on the LAN.
//
// The agent also answers DNS for its containers at FleetDNS: <name>.local
// and <name>-<page>.local are the Service's address; everything else is
// forwarded to the Node's own resolvers.
package fleetnet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
)

// RelayPath is where a Node's agent accepts a connection for one of its
// Services from a peer: POST, with service and port in the query, the
// request body carrying one direction and the response the other.
const RelayPath = "/maitre/relay"

// Iface is the dummy interface the agent holds other Nodes' Service
// addresses on.
const Iface = "maitre0"

// Config wires the manager to the Node.
type Config struct {
	Log *slog.Logger
	// Run runs ip; swapped for a fake in tests.
	Run services.Runner
	// Self is this Node's id.
	Self string
	// Services lists every live Service of the Fleet.
	Services func(ctx context.Context) []*maitrev1.ServiceSpec
	// PeerAddresses are a peer's host:port addresses, best first.
	PeerAddresses func(nodeID string) []string
	// HTTP reaches peers with this Node's identity.
	HTTP *http.Client
	// DNSAddr is where the DNS answers; empty leaves DNS off (tests).
	DNSAddr string
	// Upstreams are the resolvers other names go to, host:port; empty
	// reads the Node's resolv.conf.
	Upstreams []string
	// Bind makes the address of each listener; nil binds the real address
	// on the dummy interface, tests swap in loopback.
	Bind func(addr string, port int) string
}

// Manager holds the addresses and listeners this Node keeps for the Fleet.
type Manager struct {
	cfg Config

	mu        sync.Mutex
	claimed   map[string]bool
	listeners map[string]*listener // "addr:port"
	dns       *dnsServer
}

type listener struct {
	ln     net.Listener
	cancel context.CancelFunc
	target target
}

// target is where a Service's port answers: a local address, or a peer's
// relay.
type target struct {
	local   string // host:port, "" for a peer
	node    string
	service string
	port    int
}

func (t target) String() string {
	if t.local != "" {
		return t.local
	}
	return t.service + "@" + t.node
}

// New builds a Manager.
func New(cfg Config) *Manager {
	if cfg.Run == nil {
		cfg.Run = services.ExecRunner{}
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Manager{cfg: cfg, claimed: map[string]bool{}, listeners: map[string]*listener{}}
}

// Reconcile brings the addresses, listeners and DNS in line with the
// Fleet's Services. Called at start and whenever records change.
func (m *Manager) Reconcile(ctx context.Context) {
	specs := m.cfg.Services(ctx)
	byName := map[string]*maitrev1.ServiceSpec{}
	for _, s := range specs {
		byName[s.GetName()] = s
	}
	wantAddr := map[string]bool{}
	wantListen := map[string]target{}
	for _, s := range specs {
		addr := s.GetAddress()
		if addr == "" {
			continue
		}
		local := s.GetNodeId() == m.cfg.Self
		if local && containerHolds(s) {
			continue
		}
		if !local && !s.GetFleetReach() {
			// Not reachable from other Nodes, but the name still means
			// something: the address is held so a connection is refused
			// at once instead of wandering off.
			wantAddr[addr] = true
			continue
		}
		wantAddr[addr] = true
		for _, port := range declaredPorts(s) {
			key := net.JoinHostPort(addr, strconv.Itoa(port))
			if local {
				wantListen[key] = target{local: localTarget(s, byName, port), service: s.GetName(), port: port}
			} else {
				wantListen[key] = target{node: s.GetNodeId(), service: s.GetName(), port: port}
			}
		}
	}
	if m.cfg.DNSAddr != "" {
		wantAddr[m.cfg.DNSAddr] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Listeners first, so a container's address is free before the
	// container starts here.
	for key, l := range m.listeners {
		if t, ok := wantListen[key]; !ok || t != l.target {
			l.cancel()
			_ = l.ln.Close()
			delete(m.listeners, key)
		}
	}
	for addr := range m.claimed {
		if !wantAddr[addr] {
			m.release(ctx, addr)
		}
	}
	for addr := range wantAddr {
		if !m.claimed[addr] {
			m.claim(ctx, addr)
		}
	}
	for key, t := range wantListen {
		if _, ok := m.listeners[key]; ok {
			continue
		}
		host, portStr, _ := net.SplitHostPort(key)
		port, _ := strconv.Atoi(portStr)
		bind := key
		if m.cfg.Bind != nil {
			bind = m.cfg.Bind(host, port)
		}
		ln, err := net.Listen("tcp", bind)
		if err != nil {
			m.cfg.Log.Warn("fleet listener", "service", t.service, "port", port, "err", err)
			continue
		}
		lctx, cancel := context.WithCancel(ctx)
		l := &listener{ln: ln, cancel: cancel, target: t}
		m.listeners[key] = l
		go m.accept(lctx, l)
	}
	if m.cfg.DNSAddr != "" && m.dns == nil {
		d, err := serveDNS(ctx, m.cfg, m.resolve)
		if err != nil {
			m.cfg.Log.Warn("fleet dns", "err", err)
		} else {
			m.dns = d
		}
	}
}

// Listening reports the addresses the Node listens on for the Fleet, for
// tests and the status page.
func (m *Manager) Listening() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for key := range m.listeners {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// Close stops everything and gives the addresses back.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, l := range m.listeners {
		l.cancel()
		_ = l.ln.Close()
		delete(m.listeners, key)
	}
	if m.dns != nil {
		m.dns.close()
		m.dns = nil
	}
	for addr := range m.claimed {
		m.release(context.Background(), addr)
	}
}

func (m *Manager) claim(ctx context.Context, addr string) {
	if m.cfg.Bind != nil {
		m.claimed[addr] = true
		return
	}
	if len(m.claimed) == 0 {
		if _, err := m.cfg.Run.Run(ctx, "ip", "link", "add", Iface, "type", "dummy"); err != nil && !strings.Contains(err.Error(), "exists") {
			m.cfg.Log.Warn("fleet interface", "err", err)
		}
		_, _ = m.cfg.Run.Run(ctx, "ip", "link", "set", Iface, "up")
	}
	if _, err := m.cfg.Run.Run(ctx, "ip", "addr", "replace", addr+"/32", "dev", Iface); err != nil {
		m.cfg.Log.Warn("fleet address", "addr", addr, "err", err)
		return
	}
	m.claimed[addr] = true
}

func (m *Manager) release(ctx context.Context, addr string) {
	if m.cfg.Bind == nil {
		_, _ = m.cfg.Run.Run(ctx, "ip", "addr", "del", addr+"/32", "dev", Iface)
	}
	delete(m.claimed, addr)
}

// containerHolds reports whether a Service's own container holds its
// address on this Node: it is on the shared network, not the Node's or a
// Tunnel's.
func containerHolds(s *maitrev1.ServiceSpec) bool {
	switch s.GetNetworkKind() {
	case services.NetworkTunnel, services.NetworkHost:
		return false
	}
	return !services.HostNetworked(s)
}

// declaredPorts are the ports other Services may use: every Page's and
// the declared ports (TCP only for now).
func declaredPorts(s *maitrev1.ServiceSpec) []int {
	seen := map[int]bool{}
	var out []int
	for _, p := range services.Pages(s) {
		if p.GetPort() != 0 && !seen[int(p.GetPort())] {
			seen[int(p.GetPort())] = true
			out = append(out, int(p.GetPort()))
		}
	}
	for _, spec := range s.GetPorts() {
		num, proto, _ := strings.Cut(spec, "/")
		n, err := strconv.Atoi(num)
		if err != nil || proto != "tcp" || n <= 0 || n > 65535 || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// localTarget is where a Service of this Node answers a port when its
// container does not hold the Service's address: on the Node's own
// network at the Node itself, in a Tunnel at the Tunnel's address.
func localTarget(s *maitrev1.ServiceSpec, byName map[string]*maitrev1.ServiceSpec, port int) string {
	switch s.GetNetworkKind() {
	case services.NetworkTunnel:
		if t := byName[s.GetNetworkTarget()]; t != nil && t.GetAddress() != "" {
			return net.JoinHostPort(t.GetAddress(), strconv.Itoa(port))
		}
	case services.NetworkHost:
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	}
	if services.HostNetworked(s) {
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	}
	return net.JoinHostPort(s.GetAddress(), strconv.Itoa(port))
}

func (m *Manager) accept(ctx context.Context, l *listener) {
	for {
		c, err := l.ln.Accept()
		if err != nil {
			return
		}
		go m.carry(ctx, c, l.target)
	}
}

// carry joins one accepted connection to its target.
func (m *Manager) carry(ctx context.Context, c net.Conn, t target) {
	defer c.Close()
	var up io.ReadWriteCloser
	var err error
	if t.local != "" {
		d := net.Dialer{Timeout: 10 * time.Second}
		up, err = d.DialContext(ctx, "tcp", t.local)
	} else {
		up, err = m.dialPeer(ctx, t)
	}
	if err != nil {
		m.cfg.Log.Debug("fleet relay", "to", t.String(), "err", err)
		return
	}
	defer up.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// peerConn is a relayed connection: writes go up the request body, reads
// come down the response body.
type peerConn struct {
	w   *io.PipeWriter
	res *http.Response
}

func (p *peerConn) Read(b []byte) (int, error)  { return p.res.Body.Read(b) }
func (p *peerConn) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *peerConn) Close() error {
	_ = p.w.Close()
	return p.res.Body.Close()
}

// dialPeer opens a relayed connection to a Service on another Node,
// trying the peer's addresses in turn.
func (m *Manager) dialPeer(ctx context.Context, t target) (io.ReadWriteCloser, error) {
	if m.cfg.HTTP == nil || m.cfg.PeerAddresses == nil {
		return nil, errors.New("no peer link")
	}
	addrs := m.cfg.PeerAddresses(t.node)
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no address for Node %s", t.node)
	}
	var last error
	for _, addr := range addrs {
		pr, pw := io.Pipe()
		u := url.URL{Scheme: "https", Host: addr, Path: RelayPath, RawQuery: url.Values{"service": {t.service}, "port": {strconv.Itoa(t.port)}}.Encode()}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), pr)
		if err != nil {
			return nil, err
		}
		res, err := m.cfg.HTTP.Do(req)
		if err != nil {
			_ = pw.Close()
			last = err
			continue
		}
		if res.StatusCode != http.StatusOK {
			msg, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
			_ = res.Body.Close()
			_ = pw.Close()
			return nil, fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(msg)))
		}
		return &peerConn{w: pw, res: res}, nil
	}
	return nil, last
}

// Handler answers RelayPath for peers: it connects to the named Service's
// port on this Node and joins the two directions. The gate lets only
// Nodes of the Fleet call it.
func (m *Manager) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST", http.StatusMethodNotAllowed)
			return
		}
		name := r.URL.Query().Get("service")
		port, _ := strconv.Atoi(r.URL.Query().Get("port"))
		specs := m.cfg.Services(r.Context())
		byName := map[string]*maitrev1.ServiceSpec{}
		for _, s := range specs {
			byName[s.GetName()] = s
		}
		s := byName[name]
		if s == nil || s.GetNodeId() != m.cfg.Self {
			http.Error(w, "no such Service on this Node", http.StatusNotFound)
			return
		}
		allowed := false
		for _, p := range declaredPorts(s) {
			allowed = allowed || p == port
		}
		if !allowed || !s.GetFleetReach() {
			http.Error(w, name+" is not reachable from other Nodes on port "+strconv.Itoa(port), http.StatusForbidden)
			return
		}
		d := net.Dialer{Timeout: 10 * time.Second}
		up, err := d.DialContext(r.Context(), "tcp", localTarget(s, byName, port))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer up.Close()
		rc := http.NewResponseController(w)
		_ = rc.EnableFullDuplex()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_ = rc.Flush()
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(up, r.Body); done <- struct{}{} }()
		go func() {
			buf := make([]byte, 32*1024)
			for {
				n, err := up.Read(buf)
				if n > 0 {
					if _, werr := w.Write(buf[:n]); werr != nil {
						break
					}
					_ = rc.Flush()
				}
				if err != nil {
					break
				}
			}
			done <- struct{}{}
		}()
		select {
		case <-done:
		case <-r.Context().Done():
		}
	})
}

// resolve answers a name of the Fleet: <service> or <service>-<page>,
// without the domain.
func (m *Manager) resolve(ctx context.Context, host string) (string, bool) {
	for _, s := range m.cfg.Services(ctx) {
		if s.GetAddress() == "" {
			continue
		}
		if s.GetName() == host {
			return s.GetAddress(), true
		}
		for _, p := range services.Pages(s) {
			if services.PageHost(s, p) == host {
				return s.GetAddress(), true
			}
		}
	}
	return "", false
}

// upstreams are the Node's own resolvers: configured, else the
// nameservers of resolv.conf that are not the Node itself.
func upstreams(cfg Config) []string {
	if len(cfg.Upstreams) > 0 {
		return cfg.Upstreams
	}
	var out []string
	for _, path := range []string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"} {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) >= 2 && f[0] == "nameserver" {
				ip := net.ParseIP(f[1])
				if ip == nil || ip.IsLoopback() || ip.Equal(net.ParseIP(cfg.DNSAddr)) {
					continue
				}
				out = append(out, net.JoinHostPort(f[1], "53"))
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return []string{"1.1.1.1:53", "9.9.9.9:53"}
}
