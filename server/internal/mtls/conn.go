package mtls

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/http2"
)

// KeepAlive is the TCP keepalive on every connection a Node accepts or
// dials. A Terminal or a relayed connection (ADR 0012) may carry nothing
// for hours: the probes keep NATs and firewalls from forgetting it and
// find a peer that is gone, so nothing needs an idle timeout.
var KeepAlive = net.KeepAliveConfig{Enable: true, Idle: 15 * time.Second, Interval: 15 * time.Second, Count: 4}

// HeaderTimeout is how long a Node's server waits for a request's
// headers, the TLS handshake included. It is the server's only timeout.
var HeaderTimeout = 10 * time.Second

// Listen listens on addr with KeepAlive on every connection it accepts.
func Listen(addr string) (net.Listener, error) {
	return (&net.ListenConfig{KeepAliveConfig: KeepAlive}).Listen(context.Background(), "tcp", addr)
}

// dialer dials with KeepAlive, giving up after 5 seconds.
func dialer() *net.Dialer {
	return &net.Dialer{Timeout: 5 * time.Second, KeepAliveConfig: KeepAlive}
}

// DialTCP dials addr with KeepAlive, giving up after 5 seconds.
func DialTCP(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialer().DialContext(ctx, network, addr)
}

// Server is a Node's HTTPS server for h. It has no read, write or idle
// timeout, only HeaderTimeout: a connection that is open is kept open,
// however long it says nothing (KeepAlive watches it instead).
func Server(h http.Handler, cfg *tls.Config) *http.Server {
	return &http.Server{Handler: h, TLSConfig: cfg, ReadHeaderTimeout: HeaderTimeout}
}

// NodeClient is how a Node calls another Node: HTTP/2 over cfg (a
// ClientConfig), on connections dialed as DialTCP dials (the handshake within its 5
// seconds), with no timeout of its
// own and no idle connection timeout.
func NodeClient(cfg *tls.Config) *http.Client {
	return &http.Client{Transport: &http2.Transport{
		TLSClientConfig: cfg,
		DialTLSContext: func(ctx context.Context, network, addr string, c *tls.Config) (net.Conn, error) {
			return (&tls.Dialer{NetDialer: dialer(), Config: c}).DialContext(ctx, network, addr)
		},
	}}
}
