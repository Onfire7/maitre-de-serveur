package mtls_test

import (
	"context"
	"net"
	"net/http"
	"syscall"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
)

func keepAliveOn(t *testing.T, c net.Conn) bool {
	t.Helper()
	raw, err := c.(*net.TCPConn).SyscallConn()
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

func TestConnectionsANodeAcceptsHaveTCPKeepalives(t *testing.T) {
	ln, err := mtls.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := net.Dial("tcp", ln.Addr().String()); err == nil {
			defer c.Close()
			var b [1]byte
			_, _ = c.Read(b[:])
		}
	}()
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !keepAliveOn(t, c) {
		t.Fatal("an accepted connection has no TCP keepalive")
	}
}

func TestConnectionsANodeDialsHaveTCPKeepalives(t *testing.T) {
	ln, err := (&net.ListenConfig{KeepAlive: -1}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			defer c.Close()
			var b [1]byte
			_, _ = c.Read(b[:])
		}
	}()
	c, err := mtls.DialTCP(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !keepAliveOn(t, c) {
		t.Fatal("a dialed connection has no TCP keepalive")
	}
}

func TestANodesServerHasNoIdleTimeout(t *testing.T) {
	// A Terminal or a relayed connection may say nothing for hours.
	srv := mtls.Server(http.NotFoundHandler(), nil)
	if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 || srv.IdleTimeout != 0 {
		t.Fatalf("read %v write %v idle %v", srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
	if srv.ReadHeaderTimeout == 0 {
		t.Fatal("no header timeout: a client that never sends its request holds the connection")
	}
}
