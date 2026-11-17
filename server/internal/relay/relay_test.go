package relay_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Onfire7/maitre-de-serveur/server/internal/gate"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/relay"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
	"golang.org/x/net/http2"
)

// node is one Node of a test Fleet, served on loopback the way the agent
// serves its mutual-TLS listener: peer identity, the gate, and a mux with
// the Relay's two endpoints and a page that says who is calling.
type node struct {
	id    *identity.Identity
	list  *trust.List
	addr  string
	addrs map[string][]string // the Fleet's records: Node id -> addresses
	log   *journal
}

func newIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func startNode(t *testing.T, id *identity.Identity, list *trust.List, addrs map[string][]string, listen func() (net.Listener, error)) *node {
	t.Helper()
	n := &node{id: id, list: list, addrs: addrs, log: &journal{}}
	tlsCfg := mtls.ServerConfig(id, list)
	r := relay.New(relay.Config{
		Log:       slog.New(n.log),
		Self:      id.Fingerprint(),
		Roles:     list,
		Addresses: func(nodeID string) []string { return n.addrs[nodeID] },
		HTTP:      nodeClient(id, list),
	})
	mux := http.NewServeMux()
	mux.Handle(relay.Path, r.Anchor())
	mux.Handle(relay.InPath, r.Target())
	mux.HandleFunc("/whoami", func(w http.ResponseWriter, req *http.Request) {
		p, _ := peer.From(req.Context())
		fmt.Fprintf(w, "%s %s %s", id.Fingerprint(), p.Fingerprint, req.Proto)
	})
	// A Node's own server, with the timeouts it has.
	srv := mtls.Server(peer.Middleware(gate.Handler(list, "X-Maitre-Service", mux)), tlsCfg)
	ln, err := listen()
	if err != nil {
		t.Fatal(err)
	}
	n.addr = ln.Addr().String()
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	go func() { _ = srv.ServeTLS(r.Listener(), "", "") }()
	t.Cleanup(func() { _ = srv.Close(); r.Close() })
	return n
}

// nodeClient is how one Node reaches another: HTTP/2 over mutual TLS.
func nodeClient(id *identity.Identity, list *trust.List) *http.Client {
	return mtls.NodeClient(mtls.ClientConfig(id, list))
}

// fleet is two Nodes, an anchor and a target, and a Trusted Device.
type fleet struct {
	anchor, target *node
	device         *identity.Identity
	recovery       *identity.Identity
	outsider       *identity.Identity // trusted, but not a Node of the Fleet
}

func newFleet(t *testing.T) *fleet {
	t.Helper()
	return newFleetOn(t, func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") })
}

// newFleetOn serves the Fleet's Nodes on the listeners listen gives.
func newFleetOn(t *testing.T, listen func() (net.Listener, error)) *fleet {
	t.Helper()
	a, b := newIdentity(t), newIdentity(t)
	dev, rec, out := newIdentity(t), newIdentity(t), newIdentity(t)
	roles := func() *trust.List {
		l := trust.New()
		l.ReplaceRoles([]string{dev.Fingerprint(), out.Fingerprint()}, []string{rec.Fingerprint()}, []string{a.Fingerprint(), b.Fingerprint()})
		return l
	}
	f := &fleet{device: dev, recovery: rec, outsider: out}
	f.target = startNode(t, b, roles(), map[string][]string{}, listen)
	f.anchor = startNode(t, a, roles(), map[string][]string{b.Fingerprint(): {f.target.addr}}, listen)
	f.target.addrs[a.Fingerprint()] = []string{f.anchor.addr}
	return f
}

// pinned is a client config that accepts only the Node fingerprint.
func pinned(id *identity.Identity, fingerprint string, proto string) *tls.Config {
	l := trust.New()
	l.Add(fingerprint)
	c := mtls.ClientConfig(id, l)
	c.NextProtos = []string{proto}
	return c
}

// relayed asks the anchor for a Relay to target and returns the upgraded
// stream, or the anchor's refusal.
func relayed(t *testing.T, as *identity.Identity, anchor *node, target string) (net.Conn, error) {
	t.Helper()
	c, err := tls.Dial("tcp", anchor.addr, pinned(as, anchor.id.Fingerprint(), "http/1.1"))
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(c, "GET %s?node=%s HTTP/1.1\r\nHost: maitre\r\nConnection: Upgrade\r\nUpgrade: %s\r\n\r\n", relay.Path, url.QueryEscape(target), relay.Token)
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		c.Close()
		return nil, err
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		c.Close()
		return nil, fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	if br.Buffered() > 0 {
		t.Fatalf("anchor sent %d bytes after the upgrade", br.Buffered())
	}
	return c, nil
}

// get makes one HTTP/1.1 request over an established inner session.
func get(t *testing.T, c net.Conn, path string) string {
	t.Helper()
	fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: maitre\r\n\r\n", path)
	res, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	return fmt.Sprintf("%d %s", res.StatusCode, b)
}

func TestADeviceReachesATargetThroughTheAnchor(t *testing.T) {
	f := newFleet(t)
	outer, err := relayed(t, f.device, f.anchor, f.target.id.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	inner := tls.Client(outer, pinned(f.device, f.target.id.Fingerprint(), "http/1.1"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := inner.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("200 %s %s HTTP/1.1", f.target.id.Fingerprint(), f.device.Fingerprint())
	if got := get(t, inner, "/whoami"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// A second request on the same session.
	if got := get(t, inner, "/whoami"); got != want {
		t.Fatalf("again: got %q", got)
	}
}

func refused(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("relayed; want refused with %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestTheAnchorRefusesANodeKey(t *testing.T) {
	f := newFleet(t)
	other := newIdentity(t)
	f.anchor.addrs[other.Fingerprint()] = []string{f.target.addr}
	// The target, a Node of the Fleet, asking the anchor to relay it.
	_, err := relayed(t, f.target.id, f.anchor, f.target.id.Fingerprint())
	refused(t, err, "403 Forbidden")
}

func TestTheAnchorRefusesATargetThatIsNotANodeOfTheFleet(t *testing.T) {
	f := newFleet(t)
	// A trusted key that is a Device, with an address the anchor knows.
	f.anchor.addrs[f.outsider.Fingerprint()] = []string{f.target.addr}
	_, err := relayed(t, f.device, f.anchor, f.outsider.Fingerprint())
	refused(t, err, "404 Not Found")
	// A key nobody trusts.
	_, err = relayed(t, f.device, f.anchor, newIdentity(t).Fingerprint())
	refused(t, err, "404 Not Found")
}

func TestTheAnchorRefusesATargetGivenAsAnAddress(t *testing.T) {
	f := newFleet(t)
	for _, target := range []string{f.target.addr, "127.0.0.1", "[::1]:7443", "localhost:7443", "https://" + f.target.addr, ""} {
		_, err := relayed(t, f.device, f.anchor, target)
		refused(t, err, "400 Bad Request")
	}
	// Nor the anchor itself, which the Device reaches already.
	_, err := relayed(t, f.device, f.anchor, f.anchor.id.Fingerprint())
	refused(t, err, "400 Bad Request")
}

func TestTheTargetRefusesARelayFromAKeyThatIsNotANode(t *testing.T) {
	f := newFleet(t)
	// A Trusted Device calling the target's relayed endpoint itself.
	pr, pw := io.Pipe()
	defer pw.Close()
	req, _ := http.NewRequest(http.MethodPost, "https://"+f.target.addr+relay.InPath, pr)
	l := trust.New()
	l.Add(f.target.id.Fingerprint())
	res, err := nodeClient(f.device, l).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("got %s, want 403", res.Status)
	}
}

func TestTheRecoveryPhraseIsRelayedAndLimitedAtTheTarget(t *testing.T) {
	f := newFleet(t)
	outer, err := relayed(t, f.recovery, f.anchor, f.target.id.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	inner := tls.Client(outer, pinned(f.recovery, f.target.id.Fingerprint(), "http/1.1"))
	if got := get(t, inner, "/whoami"); !strings.HasPrefix(got, "403 the Recovery Phrase may only add a Trusted Device") {
		t.Fatalf("got %q", got)
	}
}

func TestHTTP2RunsInsideTheRelay(t *testing.T) {
	f := newFleet(t)
	outer, err := relayed(t, f.device, f.anchor, f.target.id.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	inner := tls.Client(outer, pinned(f.device, f.target.id.Fingerprint(), "h2"))
	cc, err := (&http2.Transport{}).NewClientConn(inner)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		req, _ := http.NewRequest(http.MethodGet, "https://maitre/whoami", nil)
		res, err := cc.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if want := fmt.Sprintf("%s %s HTTP/2.0", f.target.id.Fingerprint(), f.device.Fingerprint()); string(b) != want {
			t.Fatalf("got %q, want %q", b, want)
		}
	}
}

func TestAnInnerSessionPinnedToAnotherNodeFails(t *testing.T) {
	f := newFleet(t)
	outer, err := relayed(t, f.device, f.anchor, f.target.id.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	// The Device meant the anchor, but the stream reaches the target.
	inner := tls.Client(outer, pinned(f.device, f.anchor.id.Fingerprint(), "h2"))
	if err := inner.Handshake(); err == nil {
		t.Fatal("handshake with the wrong Node succeeded")
	}
}

func TestAnUnreachableTargetIsTheAnchorsAnswer(t *testing.T) {
	f := newFleet(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	f.anchor.addrs[f.target.id.Fingerprint()] = []string{dead}
	_, err := relayed(t, f.device, f.anchor, f.target.id.Fingerprint())
	refused(t, err, "502 Bad Gateway: Node "+f.target.id.Fingerprint()[:8]+" could not be reached from the anchor")
}

// The Relay is one hop (ADR 0012): a connection that came through the
// Relay cannot ask its Node for another, so a Device cannot chain Nodes.
func TestARelayedConnectionCannotAskForAnotherRelay(t *testing.T) {
	f := newFleet(t)
	outer, err := relayed(t, f.device, f.anchor, f.target.id.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	inner := tls.Client(outer, pinned(f.device, f.target.id.Fingerprint(), "http/1.1"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := inner.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	// The target knows the anchor's address, so only the one-hop rule
	// stands between this request and a second Relay.
	fmt.Fprintf(inner, "GET %s?node=%s HTTP/1.1\r\nHost: maitre\r\nConnection: Upgrade\r\nUpgrade: %s\r\n\r\n", relay.Path, f.anchor.id.Fingerprint(), relay.Token)
	res, err := http.ReadResponse(bufio.NewReader(inner), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusForbidden || !strings.Contains(string(b), "one hop") {
		t.Fatalf("got %s: %s", res.Status, b)
	}
}
