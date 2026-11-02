// Package tailnet puts a Node on the Fleet's Tailscale network (Milestone
// 5) with tsnet: no system tailscaled, the agent is the Tailscale client.
// One tsnet node carries the agent itself, so the Console reaches the Node
// over the tailnet when the LAN does not; Services get their own nodes.
package tailnet

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2/clientcredentials"
	"tailscale.com/client/tailscale"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// KindTailscale is the Fleet-wide Record holding the OAuth client.
const KindTailscale = "tailscale"

// Node is one tsnet node.
type Node struct {
	srv  *tsnet.Server
	log  *slog.Logger
	name string

	mu      sync.Mutex
	status  *ipnstate.Status
	authURL string
	err     error
}

// Start brings up a tsnet node named hostname with state under dir. authKey
// may be empty: then the node waits for the login URL to be visited, which
// Status reports. It returns at once; Up runs in the background.
func Start(ctx context.Context, log *slog.Logger, dir, hostname, authKey string) *Node {
	_ = os.MkdirAll(dir, 0o700)
	n := &Node{name: hostname, log: log}
	n.srv = &tsnet.Server{
		Dir:      dir,
		Hostname: hostname,
		AuthKey:  authKey,
		Logf:     func(string, ...any) {}, // tsnet is chatty; errors surface through Status
		UserLogf: func(format string, args ...any) { log.Info("tailscale: " + fmt.Sprintf(format, args...)) },
	}
	go func() {
		if err := n.srv.Start(); err != nil {
			n.set(nil, "", err)
			return
		}
		// Poll for the login URL or the Running state.
		for ctx.Err() == nil {
			lc, err := n.srv.LocalClient()
			if err != nil {
				n.set(nil, "", err)
				return
			}
			st, err := lc.Status(ctx)
			if err != nil {
				n.set(nil, "", err)
			} else {
				n.set(st, st.AuthURL, nil)
				if st.BackendState == "Running" {
					if _, err := n.srv.Up(ctx); err == nil {
						return
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
	return n
}

func (n *Node) set(st *ipnstate.Status, url string, err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if st != nil {
		n.status = st
	}
	if url != "" {
		n.authURL = url
	}
	n.err = err
}

// Status describes the node for the Console.
func (n *Node) Status() *maitrev1.TailscaleStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := &maitrev1.TailscaleStatus{Hostname: n.name}
	if n.err != nil {
		out.Error = n.err.Error()
	}
	if n.status != nil {
		out.State = n.status.BackendState
		out.LoginUrl = n.authURL
		if n.status.Self != nil {
			out.DnsName = n.status.Self.DNSName
			for _, ip := range n.status.Self.TailscaleIPs {
				out.Addresses = append(out.Addresses, ip.String())
			}
		}
		if n.status.CurrentTailnet != nil {
			out.Tailnet = n.status.CurrentTailnet.Name
		}
	}
	return out
}

// Listen opens a TCP listener on the tailnet.
func (n *Node) Listen(port int) (net.Listener, error) {
	return n.srv.Listen("tcp", fmt.Sprintf(":%d", port))
}

// ListenFunnel exposes a port to the public internet through Tailscale
// Funnel (TLS terminated by Tailscale).
func (n *Node) ListenFunnel(port int) (net.Listener, error) {
	return n.srv.ListenFunnel("tcp", fmt.Sprintf(":%d", port))
}

// ListenTLS serves HTTPS on the tailnet with a certificate Tailscale issues
// for the node's DNS name.
func (n *Node) ListenTLS(port int) (net.Listener, error) {
	return n.srv.ListenTLS("tcp", fmt.Sprintf(":%d", port))
}

// Close stops the node.
func (n *Node) Close() error { return n.srv.Close() }

// StateDir is where a node's tsnet state lives under the config partition.
func StateDir(stateDir, hostname string) string {
	return filepath.Join(stateDir, "tailscale", hostname)
}

// AuthKey creates a one-off, pre-authorised auth key for the tag through
// the Fleet's OAuth client (tailscale.com/cmd/get-authkey does the same).
func AuthKey(ctx context.Context, cfg *maitrev1.TailscaleConfig, ephemeral bool) (string, error) {
	if cfg.GetOauthClientId() == "" || cfg.GetOauthClientSecret() == "" {
		return "", fmt.Errorf("no Tailscale OAuth client in the Fleet")
	}
	tag := cfg.GetTag()
	if tag == "" {
		tag = "tag:maitre"
	}
	creds := clientcredentials.Config{
		ClientID:     cfg.GetOauthClientId(),
		ClientSecret: cfg.GetOauthClientSecret(),
		TokenURL:     "https://api.tailscale.com/api/v2/oauth/token",
	}
	c := tailscale.NewClient("-", nil)
	c.UserAgent = "maitre"
	c.HTTPClient = creds.Client(ctx)
	key, _, err := c.CreateKeyWithExpiry(ctx, tailscale.KeyCapabilities{
		Devices: tailscale.KeyDeviceCapabilities{Create: tailscale.KeyDeviceCreateCapabilities{Reusable: false, Ephemeral: ephemeral, Preauthorized: true, Tags: []string{tag}}},
	}, 24*time.Hour)
	if err != nil {
		return "", fmt.Errorf("tailscale: create auth key: %w", err)
	}
	return key, nil
}

// IsTailnetAddr reports whether a host:port (or bare host) is on Tailscale's
// own ranges, 100.64.0.0/10 or fd7a:115c:a1e0::/48, or a .ts.net name
// (decision 284): the addresses a Node keeps in its record across restarts.
func IsTailnetAddr(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if strings.HasSuffix(host, ".ts.net") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return cgnat.Contains(ip) || tsULA.Contains(ip)
}

var (
	cgnat = mustCIDR("100.64.0.0/10")
	tsULA = mustCIDR("fd7a:115c:a1e0::/48")
)

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// KeepTailnet returns lan followed by the tailnet addresses in prev, in
// their order, so a Node that starts with no tailnet yet still advertises
// the address it had: the Console away from home reaches it there while
// tsnet comes back (decision 284). A new tailnet address replaces them
// when tsnet reports one.
func KeepTailnet(lan, prev []string) []string {
	out := append([]string{}, lan...)
	for _, a := range prev {
		if IsTailnetAddr(a) && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}
