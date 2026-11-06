// Package portmap asks the home router to forward ports from the internet
// to this Node over UPnP (decision 115). Nothing is forwarded unless a
// Service explicitly asked for it, or a Service is published at the user's
// own domain (then 80 and 443 for Caddy). Mappings are leased for an hour
// and renewed; anything no longer wanted is deleted.
package portmap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway2"
	"github.com/huin/goupnp/soap"
)

// Mapping is one port the router should forward to this Node. Owner is the
// Service that wants it, or "https" for Caddy.
type Mapping struct {
	Port  uint16
	Proto string // "tcp" | "udp"
	Owner string
}

// String is "25565/tcp".
func (m Mapping) String() string { return strconv.Itoa(int(m.Port)) + "/" + m.Proto }

// Parse reads "25565/tcp" into a Mapping without an owner.
func Parse(s string) (Mapping, bool) {
	p, proto, ok := strings.Cut(strings.ToLower(strings.TrimSpace(s)), "/")
	if !ok || (proto != "tcp" && proto != "udp") {
		return Mapping{}, false
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return Mapping{}, false
	}
	return Mapping{Port: uint16(n), Proto: proto}, true
}

// Router is what a port-mapping gateway must do; the UPnP implementation
// below is the real one, tests use a fake.
type Router interface {
	Name() string
	Add(ctx context.Context, m Mapping, internalIP string, lease time.Duration) error
	// Delete removes the mapping only while it still forwards to
	// internalIP, and answers ErrNotOurs when it forwards elsewhere. A
	// router keys mappings by external port alone, so a Node letting go of
	// 443 just after another Node took it over (the web entry moving,
	// decision 218) would otherwise delete the new holder's forward.
	Delete(ctx context.Context, m Mapping, internalIP string) error
}

// ErrNotOurs is Delete's answer for a mapping another machine now holds;
// it was left in place.
var ErrNotOurs = errors.New("now forwarded to another machine; left in place")

// Lease is how long each mapping is asked for; renewals come at half that.
const Lease = time.Hour

type key struct {
	port  uint16
	proto string
}

type live struct {
	m       Mapping
	ip      string // the address it was forwarded to
	mapped  bool
	err     string
	renewed time.Time
}

// Manager keeps the wanted mappings in step with the router.
type Manager struct {
	log      *slog.Logger
	Discover func(ctx context.Context) (Router, error)
	LocalIP  func() string

	mu        sync.Mutex
	wants     map[key]Mapping
	state     map[key]*live
	router    Router
	routerErr string
	looked    bool
	firstLook time.Time
	lastLook  time.Time
	now       func() time.Time
	wake      chan struct{}
}

// Discovery cadence while no router answers (decision 118): every pass for
// the first hour, then every five minutes.
const (
	eagerFor   = time.Hour
	backoffGap = 5 * time.Minute
)

// New builds a Manager that discovers a UPnP router. localIP gives this
// Node's LAN address, the one the router must forward to.
func New(log *slog.Logger, localIP func() string) *Manager {
	return &Manager{log: log, Discover: discoverUPnP, LocalIP: localIP, wants: map[key]Mapping{}, state: map[key]*live{}, now: time.Now, wake: make(chan struct{}, 1)}
}

// Reconcile sets the complete list of wanted mappings. Changes are applied
// shortly after by Run.
func (m *Manager) Reconcile(wants []Mapping) {
	m.mu.Lock()
	m.wants = map[key]Mapping{}
	for _, w := range wants {
		if w.Port == 0 || (w.Proto != "tcp" && w.Proto != "udp") {
			continue
		}
		m.wants[key{w.Port, w.Proto}] = w
	}
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Router describes the gateway for NodeInfo: "router: <name>", "no UPnP
// router found", or "" before the first look.
func (m *Manager) Router() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case m.router != nil:
		return "router: " + m.router.Name()
	case m.looked:
		return "no UPnP router found"
	}
	return ""
}

// State summarises the mappings owned by owner for the Console; empty when
// it wants none.
func (m *Manager) State(owner string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var want []Mapping
	for _, w := range m.wants {
		if w.Owner == owner {
			want = append(want, w)
		}
	}
	if len(want) == 0 {
		return ""
	}
	sort.Slice(want, func(i, j int) bool {
		if want[i].Port != want[j].Port {
			return want[i].Port < want[j].Port
		}
		return want[i].Proto < want[j].Proto
	})
	var names, failed []string
	for _, w := range want {
		names = append(names, w.String())
		if l := m.state[key{w.Port, w.Proto}]; l == nil || !l.mapped {
			if l != nil && l.err != "" {
				failed = append(failed, w.String()+": "+l.err)
			} else {
				failed = append(failed, w.String())
			}
		}
	}
	ports := strings.Join(names, ", ")
	switch {
	case m.router == nil && m.looked:
		return "no UPnP router found; forward " + ports + " to this Node on your router yourself"
	case m.router == nil:
		return "looking for a UPnP router to forward " + ports
	case len(failed) == 0:
		return "forwarded " + ports + " at " + m.router.Name()
	}
	return "router did not forward " + strings.Join(failed, "; ")
}

// Run applies changes, renews leases and looks for a router until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		m.tick(ctx)
		select {
		case <-ctx.Done():
			m.release()
			return
		case <-t.C:
		case <-m.wake:
			select {
			case <-ctx.Done():
				m.release()
				return
			case <-time.After(2 * time.Second):
			}
		}
	}
}

// Tick is one pass: discover when needed, add, renew, delete.
func (m *Manager) tick(ctx context.Context) {
	m.mu.Lock()
	wants := make(map[key]Mapping, len(m.wants))
	for k, w := range m.wants {
		wants[k] = w
	}
	router := m.router
	stale := len(m.state) > 0
	m.mu.Unlock()
	if len(wants) == 0 && !stale {
		return
	}
	if router == nil {
		// A router is looked for only while something wants a mapping;
		// every pass (a minute) for the first hour, then every five minutes.
		m.mu.Lock()
		now := m.now()
		if m.firstLook.IsZero() {
			m.firstLook = now
		}
		if now.Sub(m.firstLook) > eagerFor && now.Sub(m.lastLook) < backoffGap {
			m.mu.Unlock()
			return
		}
		m.lastLook = now
		m.mu.Unlock()
		dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		r, err := m.Discover(dctx)
		cancel()
		m.mu.Lock()
		m.looked = true
		if err != nil {
			m.routerErr = err.Error()
			m.mu.Unlock()
			return
		}
		m.router, m.routerErr = r, ""
		router = r
		m.mu.Unlock()
		m.log.Info("port mapping: router found", "router", r.Name())
	}
	ip := m.LocalIP()
	now := time.Now()
	m.mu.Lock()
	var add []Mapping
	var del []live
	for k, w := range wants {
		l := m.state[k]
		if l == nil || !l.mapped || now.Sub(l.renewed) > Lease/2 {
			add = append(add, w)
		}
	}
	for k, l := range m.state {
		if _, ok := wants[k]; !ok {
			del = append(del, *l)
		}
	}
	m.mu.Unlock()
	for _, l := range del {
		w := l.m
		m.mu.Lock()
		delete(m.state, key{w.Port, w.Proto})
		m.mu.Unlock()
		if !l.mapped {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := router.Delete(cctx, w, l.ip)
		cancel()
		switch {
		case errors.Is(err, ErrNotOurs):
			m.log.Info("port mapping: let go", "port", w.String(), "owner", w.Owner, "note", err)
		case err != nil:
			m.log.Warn("port mapping: delete", "port", w.String(), "err", err)
		default:
			m.log.Info("port mapping: removed", "port", w.String(), "owner", w.Owner)
		}
	}
	for _, w := range add {
		if ip == "" {
			m.setErr(w, "this Node has no LAN address yet")
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := router.Add(cctx, w, ip, Lease)
		cancel()
		if err != nil {
			m.setErr(w, trim(err))
			m.log.Warn("port mapping: add", "port", w.String(), "err", err)
			continue
		}
		m.mu.Lock()
		l := m.state[key{w.Port, w.Proto}]
		fresh := l == nil || !l.mapped
		m.state[key{w.Port, w.Proto}] = &live{m: w, ip: ip, mapped: true, renewed: now}
		m.mu.Unlock()
		if fresh {
			m.log.Info("port mapping: forwarded", "port", w.String(), "owner", w.Owner, "to", ip)
		}
	}
}

func (m *Manager) setErr(w Mapping, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state[key{w.Port, w.Proto}] = &live{m: w, err: msg}
}

// release deletes every mapping at shutdown; a stopped Node should not
// stay reachable from the internet.
func (m *Manager) release() {
	m.mu.Lock()
	router := m.router
	var del []live
	for _, l := range m.state {
		if l.mapped {
			del = append(del, *l)
		}
	}
	m.state = map[key]*live{}
	m.mu.Unlock()
	if router == nil {
		return
	}
	for _, l := range del {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = router.Delete(ctx, l.m, l.ip)
		cancel()
	}
}

func trim(err error) string {
	s := strings.TrimSpace(err.Error())
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// upnpRouter wraps the first WANIPConnection (v2, then v1) or
// WANPPPConnection service found on the LAN.
type upnpRouter struct {
	name string
	add  func(ctx context.Context, remote string, ext uint16, proto string, in uint16, client string, enabled bool, desc string, lease uint32) error
	del  func(ctx context.Context, remote string, ext uint16, proto string) error
	get  func(ctx context.Context, remote string, ext uint16, proto string) (in uint16, client string, enabled bool, desc string, lease uint32, err error)
}

func (r *upnpRouter) Name() string { return r.name }

func (r *upnpRouter) Add(ctx context.Context, m Mapping, ip string, lease time.Duration) error {
	desc := "Maitre " + m.Owner
	err := r.add(ctx, "", m.Port, strings.ToUpper(m.Proto), m.Port, ip, true, desc, uint32(lease/time.Second))
	if err != nil {
		// Older gateways only take permanent leases (error 725
		// OnlyPermanentLeasesSupported); try without one.
		if err2 := r.add(ctx, "", m.Port, strings.ToUpper(m.Proto), m.Port, ip, true, desc, 0); err2 == nil {
			return nil
		}
	}
	return err
}

func (r *upnpRouter) Delete(ctx context.Context, m Mapping, ip string) error {
	proto := strings.ToUpper(m.Proto)
	_, client, _, _, _, err := r.get(ctx, "", m.Port, proto)
	switch {
	case upnpCode(err) == 714: // NoSuchEntryInArray: already gone
		return nil
	case err == nil && client != ip:
		return fmt.Errorf("%w (%s)", ErrNotOurs, client)
	}
	// A router that cannot say who holds the port is answered as before:
	// the mapping is deleted.
	return r.del(ctx, "", m.Port, proto)
}

// upnpCode is the UPnP error code in a SOAP fault, 0 for anything else.
func upnpCode(err error) int {
	var f *soap.SOAPFaultError
	if errors.As(err, &f) {
		return f.Detail.UPnPError.Errorcode
	}
	return 0
}

func discoverUPnP(ctx context.Context) (Router, error) {
	if cs, _, err := internetgateway2.NewWANIPConnection2ClientsCtx(ctx); err == nil && len(cs) > 0 {
		c := cs[0]
		return &upnpRouter{name: friendly(c.RootDevice.Device.FriendlyName), add: c.AddPortMappingCtx, del: c.DeletePortMappingCtx, get: c.GetSpecificPortMappingEntryCtx}, nil
	}
	if cs, _, err := internetgateway2.NewWANIPConnection1ClientsCtx(ctx); err == nil && len(cs) > 0 {
		c := cs[0]
		return &upnpRouter{name: friendly(c.RootDevice.Device.FriendlyName), add: c.AddPortMappingCtx, del: c.DeletePortMappingCtx, get: c.GetSpecificPortMappingEntryCtx}, nil
	}
	if cs, _, err := internetgateway2.NewWANPPPConnection1ClientsCtx(ctx); err == nil && len(cs) > 0 {
		c := cs[0]
		return &upnpRouter{name: friendly(c.RootDevice.Device.FriendlyName), add: c.AddPortMappingCtx, del: c.DeletePortMappingCtx, get: c.GetSpecificPortMappingEntryCtx}, nil
	}
	return nil, fmt.Errorf("no UPnP gateway answered")
}

func friendly(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "your router"
	}
	return s
}
