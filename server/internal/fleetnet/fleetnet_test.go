package fleetnet_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/fleetnet"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
)

// echo answers each connection with what it was sent, prefixed.
func echo(t *testing.T, prefix string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				b := make([]byte, 64)
				n, _ := c.Read(b)
				_, _ = c.Write([]byte(prefix + string(b[:n])))
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln
}

func port(addr string) int {
	_, p, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(p)
	return n
}

// Bound ports: tests bind loopback ports in place of the Fleet's
// addresses, one per (address, port) pair.
type binder struct{ ports map[string]string }

func (b *binder) bind(addr string, p int) string {
	key := addr + ":" + strconv.Itoa(p)
	if v, ok := b.ports[key]; ok {
		return v
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	v := ln.Addr().String()
	ln.Close()
	b.ports[key] = v
	return v
}

// Two Nodes: a Service of B reachable from the Fleet. Node A holds its
// address, listens on its port and carries the connection to B's agent,
// which connects to the container. A Service of A on the Node's own
// network holds its own Fleet address and is not listened for here. DNS
// gives each name its address either way.
func TestFleetReach(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := echo(t, "db:")
	ha := echo(t, "ha:")
	specs := []*maitrev1.ServiceSpec{
		{Name: "postgres", NodeId: "B", Address: "10.90.0.10", FleetReach: true, Ports: []string{strconv.Itoa(port(db.Addr().String())) + "/tcp"}},
		{Name: "ha", NodeId: "A", Address: "10.90.0.11", NetworkKind: services.NetworkHost, FleetReach: true, Pages: []*maitrev1.ServicePage{{Name: "web", Port: uint32(port(ha.Addr().String()))}}},
		{Name: "private", NodeId: "B", Address: "10.90.0.12", Pages: []*maitrev1.ServicePage{{Name: "web", Port: 80}}},
	}
	list := func(context.Context) []*maitrev1.ServiceSpec { return specs }
	// Node B: its container at the echo server stands in for postgres.
	bind := &binder{ports: map[string]string{}}
	specsB := []*maitrev1.ServiceSpec{{Name: "postgres", NodeId: "B", Address: "127.0.0.1", FleetReach: true, Ports: specs[0].Ports}}
	nodeB := fleetnet.New(fleetnet.Config{Self: "B", Services: func(context.Context) []*maitrev1.ServiceSpec { return specsB }, Bind: bind.bind})
	srv := httptest.NewUnstartedServer(nodeB.Handler())
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	// Node A.
	nodeA := fleetnet.New(fleetnet.Config{Self: "A", Services: list, Bind: bind.bind, DNSAddr: "10.90.0.2", HTTP: srv.Client(),
		PeerAddresses: func(id string) []string { return []string{strings.TrimPrefix(srv.URL, "https://")} }})
	defer nodeA.Close()
	nodeA.Reconcile(ctx)
	listening := nodeA.Listening()
	if len(listening) != 1 || listening[0] != "10.90.0.10:"+strconv.Itoa(port(db.Addr().String())) {
		t.Fatalf("listening %v", listening)
	}
	ask := func(addr, msg string) string {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = c.Write([]byte(msg))
		b, _ := io.ReadAll(c)
		return string(b)
	}
	if got := ask(bind.bind("10.90.0.10", port(db.Addr().String())), "hello"); got != "db:hello" {
		t.Errorf("through the peer: %q", got)
	}
	// ha is on this Node's own network, so its container shares the
	// namespace maitre0 lives in and answers on the Fleet address itself.
	// A listener here would take the port from it rather than reach it.
	if _, err := net.DialTimeout("tcp", bind.bind("10.90.0.11", port(ha.Addr().String())), 200*time.Millisecond); err == nil {
		t.Error("fleetnet listened on a host-networked Service's own port")
	}
	// A peer still reaches it, which is what the listener was for: B asks
	// A's agent, and A connects to the container at the Node itself.
	srvA := httptest.NewUnstartedServer(nodeA.Handler())
	srvA.EnableHTTP2 = true
	srvA.StartTLS()
	defer srvA.Close()
	// The status is the assertion: A accepted the relay and connected to
	// the container at 127.0.0.1. Exchanging bytes over it needs the
	// streaming body a real peer sends, which is dialPeer's job, not this
	// test's -- a plain POST would be refused 403 if the path were gone.
	if res, err := srvA.Client().Post(srvA.URL+fleetnet.RelayPath+"?service=ha&port="+strconv.Itoa(port(ha.Addr().String())), "", nil); err != nil {
		t.Fatal(err)
	} else {
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("through A's agent: %d", res.StatusCode)
		}
	}
	// A Service not reachable from the Fleet: its address is held, its
	// port refused.
	if _, err := net.DialTimeout("tcp", bind.bind("10.90.0.12", 80), 200*time.Millisecond); err == nil {
		t.Error("a private Service answered")
	}
	// B refuses a port the Service did not declare.
	res, err := srv.Client().Post(srv.URL+fleetnet.RelayPath+"?service=postgres&port=22", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("undeclared port: %d", res.StatusCode)
	}
	// DNS: names of the Fleet, and nothing else here.
	dnsAddr := bind.bind("10.90.0.2", 53)
	lookup := func(name string, typ dnsmessage.Type) (string, dnsmessage.RCode) {
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 7, RecursionDesired: true})
		_ = b.StartQuestions()
		_ = b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: typ, Class: dnsmessage.ClassINET})
		q, _ := b.Finish()
		c, err := net.Dial("udp", dnsAddr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = c.Write(q)
		buf := make([]byte, 512)
		n, err := c.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		var p dnsmessage.Parser
		h, _ := p.Start(buf[:n])
		_ = p.SkipAllQuestions()
		as, _ := p.AllAnswers()
		for _, a := range as {
			if r, ok := a.Body.(*dnsmessage.AResource); ok {
				return net.IP(r.A[:]).String(), h.RCode
			}
		}
		return "", h.RCode
	}
	if ip, _ := lookup("postgres.local.", dnsmessage.TypeA); ip != "10.90.0.10" {
		t.Errorf("postgres.local = %q", ip)
	}
	if ip, rc := lookup("HA.local.", dnsmessage.TypeA); ip != "10.90.0.11" || rc != dnsmessage.RCodeSuccess {
		t.Errorf("ha.local = %q %v", ip, rc)
	}
	if ip, rc := lookup("nothing.local.", dnsmessage.TypeA); ip != "" || rc != dnsmessage.RCodeNameError {
		t.Errorf("nothing.local = %q %v", ip, rc)
	}
	if ip, rc := lookup("postgres.local.", dnsmessage.TypeAAAA); ip != "" || rc != dnsmessage.RCodeSuccess {
		t.Errorf("AAAA = %q %v", ip, rc)
	}
	// The Service moves to A: its address is given up here (a container
	// will hold it) and the listener goes, leaving none.
	specs[0].NodeId = "A"
	nodeA.Reconcile(ctx)
	if l := nodeA.Listening(); len(l) != 0 {
		t.Errorf("after the move: %v", l)
	}
}

// ipRunner records the ip commands fleetnet runs.
type ipRunner struct{ cmds []string }

func (r *ipRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	r.cmds = append(r.cmds, name+" "+strings.Join(args, " "))
	return "", nil
}

// The addresses are re-claimed on every Reconcile, not only the first.
// services.ensureNetwork deletes the dummy interface to free FleetSubnet
// before Podman will create the shared network over the same range, and a
// Manager that trusted its own cache would never put them back — the
// Fleet would stay unreachable until the agent restarted.
func TestReconcileReclaimsAfterTheInterfaceGoes(t *testing.T) {
	ctx := context.Background()
	spec := &maitrev1.ServiceSpec{Name: "memos", NodeId: "B", Address: "10.90.0.14"}
	r := &ipRunner{}
	m := fleetnet.New(fleetnet.Config{
		Self:     "A",
		Run:      r,
		Services: func(context.Context) []*maitrev1.ServiceSpec { return []*maitrev1.ServiceSpec{spec} },
	})
	defer m.Close()

	want := "ip addr replace 10.90.0.14/32 dev " + fleetnet.Iface
	m.Reconcile(ctx)
	m.Reconcile(ctx)

	n := 0
	for _, c := range r.cmds {
		if c == want {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%q ran %d times, want 2; ran %v", want, n, r.cmds)
	}
}
