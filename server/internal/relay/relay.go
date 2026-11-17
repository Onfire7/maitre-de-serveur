// Package relay is the Relay (ADR 0012): the anchor carries a Trusted
// Device's connection to another Node of the Fleet, unread. The Device
// asks the anchor, on its mutual-TLS listener, with an HTTP/1.1 Upgrade
// naming the target by node id (Path). The anchor dials only the Fleet's
// own Nodes at the addresses their records give, and carries the bytes to
// the target as a full-duplex HTTP/2 stream over the Nodes' own link
// (InPath, the carry code fleetnet uses). The target hands the stream to
// its own mutual-TLS listener in-process (Listener), so inside it the
// Device makes a second handshake with the target and is authorized as on
// any connection; the anchor sees only ciphertext.
package relay

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Onfire7/maitre-de-serveur/server/internal/carry"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
)

// Path is where a Device asks the anchor for a Relay: GET with
// node=<node id>, Connection: Upgrade and Upgrade: Token.
const Path = "/maitre/relay-node"

// InPath is where a Node accepts a connection relayed to it by another
// Node: POST, the request body carrying the Device's bytes and the
// response the target's.
const InPath = "/maitre/relayed"

// Token is the Upgrade protocol the Device asks for.
const Token = "maitre-relay"

// PerDevice is how many relayed connections one Device may hold at once.
const PerDevice = 256

// Roles is what the Relay needs from the trust list.
type Roles interface {
	Node(fingerprint string) bool
}

// Config wires the Relay to the Node.
type Config struct {
	Log *slog.Logger
	// Self is this Node's id.
	Self string
	// Roles tells Nodes from Devices.
	Roles Roles
	// Addresses are the host:port addresses the Fleet's records give a
	// Node, best first; none for a key that is not a Node of the Fleet.
	Addresses func(nodeID string) []string
	// HTTP reaches other Nodes with this Node's identity, over HTTP/2.
	HTTP *http.Client
}

// Relay serves both ends: the anchor's and the target's.
type Relay struct {
	cfg Config
	ln  *Listener

	mu     sync.Mutex
	counts map[string]int
}

// New builds a Relay.
func New(cfg Config) *Relay {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Relay{cfg: cfg, ln: NewListener(), counts: map[string]int{}}
}

// Listener is where connections relayed to this Node arrive; the Node
// serves it as it serves its mutual-TLS listener.
func (r *Relay) Listener() net.Listener { return r.ln }

// Close stops the Listener.
func (r *Relay) Close() { _ = r.ln.Close() }

func refuse(w http.ResponseWriter, code int, msg string) {
	http.Error(w, msg, code)
}

// isNodeID reports whether s has the shape of a node id: the hex SHA-256
// of a key. An address never does.
func isNodeID(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32
}

func (r *Relay) take(device string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counts[device] >= PerDevice {
		return false
	}
	r.counts[device]++
	return true
}

func (r *Relay) give(device string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counts[device]--; r.counts[device] <= 0 {
		delete(r.counts, device)
	}
}

// Anchor answers Path: it relays a Trusted Device's connection to the
// Node it names, one hop, to the addresses the Fleet's records give.
// A request that itself came through the Relay is refused (ADR 0012: the
// Relay is one hop).
func (r *Relay) Anchor() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p, ok := peer.From(req.Context())
		switch {
		case !ok:
			refuse(w, http.StatusUnauthorized, "the Relay needs a Trusted Device")
			return
		case r.cfg.Roles.Node(p.Fingerprint):
			refuse(w, http.StatusForbidden, "a Node may not use the Relay; it reaches other Nodes itself")
			return
		case throughRelay(req):
			refuse(w, http.StatusForbidden, "the Relay is one hop: a relayed connection cannot ask for another")
			return
		case req.ProtoMajor != 1 || !strings.EqualFold(req.Header.Get("Upgrade"), Token):
			w.Header().Set("Upgrade", Token)
			refuse(w, http.StatusUpgradeRequired, "the Relay is an HTTP/1.1 Upgrade to "+Token)
			return
		}
		target := strings.ToLower(strings.TrimSpace(req.URL.Query().Get("node")))
		switch {
		case !isNodeID(target):
			refuse(w, http.StatusBadRequest, fmt.Sprintf("the Relay takes a Node's id, not %q", target))
			return
		case target == r.cfg.Self:
			refuse(w, http.StatusBadRequest, "this is the anchor itself; connect to it directly")
			return
		}
		var addrs []string
		if r.cfg.Addresses != nil {
			addrs = r.cfg.Addresses(target)
		}
		if !r.cfg.Roles.Node(target) || len(addrs) == 0 {
			refuse(w, http.StatusNotFound, "no Node "+short(target)+" in this Fleet")
			return
		}
		if !r.take(p.Fingerprint) {
			refuse(w, http.StatusTooManyRequests, fmt.Sprintf("this Device already holds %d relayed connections", PerDevice))
			return
		}
		defer r.give(p.Fingerprint)

		// The relayed connection outlives the request that asked for it.
		ctx, cancel := context.WithCancel(context.WithoutCancel(req.Context()))
		defer cancel()
		up, err := carry.Dial(ctx, r.cfg.HTTP, addrs, InPath, nil)
		if err != nil {
			refuse(w, http.StatusBadGateway, "Node "+short(target)+" could not be reached from the anchor: "+err.Error())
			return
		}
		defer up.Close()
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			refuse(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer conn.Close()
		// No deadline on the Device's leg, however long it is silent; TCP
		// keepalives find a Device that is gone, whatever listener it
		// came in on. (The anchor's leg to the target is the Nodes' own
		// link, which mtls dials and accepts with keepalives.)
		_ = conn.SetDeadline(time.Time{})
		keepAlive(conn)
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + Token + "\r\n\r\n")
		if err := brw.Flush(); err != nil {
			return
		}
		start := time.Now()
		toTarget, fromTarget := carry.Join(ctx, &hijacked{Conn: conn, r: brw.Reader}, up)
		r.cfg.Log.Info("relayed connection", "device", p.Fingerprint, "target", target,
			"start", start.Format(time.RFC3339), "end", time.Now().Format(time.RFC3339),
			"bytes_to_target", toTarget, "bytes_from_target", fromTarget)
	})
}

// keepAlive turns TCP keepalives on under c, a TLS connection or not.
func keepAlive(c net.Conn) {
	if t, ok := c.(*tls.Conn); ok {
		c = t.NetConn()
	}
	if k, ok := c.(interface {
		SetKeepAliveConfig(net.KeepAliveConfig) error
	}); ok {
		_ = k.SetKeepAliveConfig(mtls.KeepAlive)
	}
}

// hijacked reads what the server had buffered before the connection.
type hijacked struct {
	net.Conn
	r *bufio.Reader
}

func (h *hijacked) Read(b []byte) (int, error) { return h.r.Read(b) }

// Target answers InPath: a connection relayed to this Node by another
// Node, handed to this Node's own listener, where the Device inside it is
// authorized as on any connection.
func (r *Relay) Target() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p, ok := peer.From(req.Context())
		if !ok || !r.cfg.Roles.Node(p.Fingerprint) {
			refuse(w, http.StatusForbidden, "only a Node of the Fleet may relay a connection to this Node")
			return
		}
		if req.Method != http.MethodPost {
			refuse(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		mine, theirs := net.Pipe()
		defer mine.Close()
		if err := r.ln.hand(relayedConn{theirs}); err != nil {
			refuse(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		carry.Join(req.Context(), carry.Answer(w, req), mine)
	})
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// Listener is a net.Listener whose connections are handed to it in-process.
type Listener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

// NewListener builds an open Listener.
func NewListener() *Listener {
	return &Listener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

var errClosed = errors.New("relay: listener closed")

func (l *Listener) hand(c net.Conn) error {
	select {
	case l.conns <- c:
		return nil
	case <-l.closed:
		return errClosed
	}
}

func (l *Listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, errClosed
	}
}

func (l *Listener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *Listener) Addr() net.Addr { return relayAddr{} }

// relayedConn is a connection relayed to this Node: its local address is
// the Listener's, which is how throughRelay knows a request came through it.
type relayedConn struct{ net.Conn }

func (relayedConn) LocalAddr() net.Addr { return relayAddr{} }

// throughRelay reports whether req arrived on a connection relayed to this Node
// (the http.Server puts the connection's local address in its context).
func throughRelay(req *http.Request) bool {
	a, ok := req.Context().Value(http.LocalAddrContextKey).(net.Addr)
	return ok && a.Network() == (relayAddr{}).Network()
}

type relayAddr struct{}

func (relayAddr) Network() string { return "relay" }
func (relayAddr) String() string  { return "relay" }

var _ io.ReadWriter = (*hijacked)(nil)
