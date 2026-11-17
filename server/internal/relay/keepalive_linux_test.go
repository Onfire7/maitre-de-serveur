package relay_test

import (
	"context"
	"crypto/tls"
	"net"
	"sync"
	"syscall"
	"testing"
)

// accepting is a listener without TCP keepalives that keeps every
// connection it accepts, so a test can look at the server's own sockets.
type accepting struct {
	net.Listener
	mu    sync.Mutex
	conns []*net.TCPConn
}

func (a *accepting) Accept() (net.Conn, error) {
	c, err := a.Listener.Accept()
	if tc, ok := c.(*net.TCPConn); ok {
		a.mu.Lock()
		a.conns = append(a.conns, tc)
		a.mu.Unlock()
	}
	return c, err
}

func keepAliveOn(t *testing.T, c *net.TCPConn) bool {
	t.Helper()
	raw, err := c.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var on int
	var serr error
	if err := raw.Control(func(fd uintptr) {
		on, serr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_KEEPALIVE)
	}); err != nil {
		t.Fatal(err)
	}
	if serr != nil {
		t.Fatal(serr)
	}
	return on != 0
}

func TestTheDevicesLegOfARelayedConnectionHasTCPKeepalives(t *testing.T) {
	var anchorLn *accepting
	f := newFleetOn(t, func() (net.Listener, error) {
		ln, err := (&net.ListenConfig{KeepAlive: -1}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		anchorLn = &accepting{Listener: ln}
		return anchorLn, nil
	})
	// The anchor is started last, so anchorLn is its listener.
	outer, err := relayed(t, f.device, f.anchor, f.target.id.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	inner := tls.Client(outer, pinned(f.device, f.target.id.Fingerprint(), "http/1.1"))
	get(t, inner, "/whoami")
	anchorLn.mu.Lock()
	defer anchorLn.mu.Unlock()
	if len(anchorLn.conns) != 1 {
		t.Fatalf("the anchor accepted %d connections, want the Device's one", len(anchorLn.conns))
	}
	if !keepAliveOn(t, anchorLn.conns[0]) {
		t.Fatal("the anchor's socket to the Device has no TCP keepalive")
	}
}
