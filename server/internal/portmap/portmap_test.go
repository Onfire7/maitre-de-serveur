package portmap

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huin/goupnp/soap"
)

// fakeRouter behaves like the eero: an entry per machine for the same port,
// the oldest one forwarding; an Add from a machine already holding an entry
// only renews it; a delete removes the oldest whoever asks. The real
// upnpRouter runs on top of it.
type fakeRouter struct {
	mu      sync.Mutex
	entries map[string][]fakeEntry // "port/proto" -> oldest first
	refuse  map[string]error
	// ignoreDeletes answers that many deletes OK without deleting.
	ignoreDeletes int
	refuseDel     error
	adds          int
	deletes       int
}

type fakeEntry struct{ client, desc string }

func portKey(port uint16, proto string) string {
	return strconv.Itoa(int(port)) + "/" + strings.ToLower(proto)
}

func (f *fakeRouter) router() Router {
	return &upnpRouter{name: "FakeBox 3000", add: f.add, del: f.del, get: f.get}
}

func (f *fakeRouter) add(_ context.Context, _ string, ext uint16, proto string, _ uint16, client string, _ bool, desc string, _ uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := portKey(ext, proto)
	f.adds++
	if err := f.refuse[k]; err != nil {
		return err
	}
	if f.entries == nil {
		f.entries = map[string][]fakeEntry{}
	}
	for _, e := range f.entries[k] {
		if e.client == client {
			return nil
		}
	}
	f.entries[k] = append(f.entries[k], fakeEntry{client, desc})
	return nil
}

func (f *fakeRouter) del(_ context.Context, _ string, ext uint16, proto string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := portKey(ext, proto)
	if f.refuseDel != nil {
		return f.refuseDel
	}
	if len(f.entries[k]) == 0 {
		return noEntry()
	}
	f.deletes++
	if f.ignoreDeletes > 0 {
		f.ignoreDeletes--
		return nil
	}
	f.entries[k] = f.entries[k][1:]
	if len(f.entries[k]) == 0 {
		delete(f.entries, k)
	}
	return nil
}

func noEntry() error {
	e := &soap.SOAPFaultError{}
	e.Detail.UPnPError.Errorcode = 714 // NoSuchEntryInArray
	return e
}

func (f *fakeRouter) get(_ context.Context, _ string, ext uint16, proto string) (uint16, string, bool, string, uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	es := f.entries[portKey(ext, proto)]
	if len(es) == 0 {
		return 0, "", false, "", 0, noEntry()
	}
	return ext, es[0].client, true, es[0].desc, 3600, nil
}

// top is where the router forwards a port, "" for nowhere.
func (f *fakeRouter) top(port string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if es := f.entries[port]; len(es) > 0 {
		return es[0].client
	}
	return ""
}

func (f *fakeRouter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, es := range f.entries {
		n += len(es)
	}
	return n
}

func newTest(f *fakeRouter, derr error) (*Manager, *fakeRouter) {
	m := New(slog.Default(), func() string { return "192.168.1.20" })
	m.Discover = func(context.Context) (Router, error) {
		if f == nil {
			return nil, derr
		}
		return f.router(), derr
	}
	return m, f
}

func TestParse(t *testing.T) {
	for in, want := range map[string]bool{"25565/tcp": true, "53/UDP": true, "0/tcp": false, "80": false, "80/sctp": false, "70000/tcp": false} {
		if _, ok := Parse(in); ok != want {
			t.Errorf("Parse(%q) ok=%v want %v", in, ok, want)
		}
	}
}

func TestNothingHappensWithoutWants(t *testing.T) {
	m, _ := newTest(nil, errors.New("should not be called"))
	m.tick(context.Background())
	if m.Router() != "" || m.State("x") != "" {
		t.Fatalf("idle manager reported %q / %q", m.Router(), m.State("x"))
	}
}

func TestAddRenewDelete(t *testing.T) {
	m, f := newTest(&fakeRouter{}, nil)
	m.Reconcile([]Mapping{{25565, "tcp", "minecraft"}, {25565, "udp", "minecraft"}, {80, "tcp", "https"}, {443, "tcp", "https"}})
	m.tick(context.Background())
	if got := f.top("25565/tcp"); got != "192.168.1.20" {
		t.Fatalf("25565/tcp mapped to %q", got)
	}
	if s := m.State("minecraft"); s != "forwarded 25565/tcp, 25565/udp at FakeBox 3000" {
		t.Fatalf("state %q", s)
	}
	if s := m.State("https"); !strings.HasPrefix(s, "forwarded 80/tcp, 443/tcp") {
		t.Fatalf("https state %q", s)
	}
	if m.Router() != "router: FakeBox 3000" {
		t.Fatalf("router %q", m.Router())
	}
	// A second pass inside the lease does nothing.
	adds := f.adds
	m.tick(context.Background())
	if f.adds != adds {
		t.Fatalf("renewed too early: %d adds", f.adds-adds)
	}
	// Past half the lease it renews.
	m.mu.Lock()
	for _, l := range m.state {
		l.renewed = time.Now().Add(-Lease)
	}
	m.mu.Unlock()
	m.tick(context.Background())
	if f.adds != adds+4 {
		t.Fatalf("expected 4 renewals, got %d", f.adds-adds)
	}
	// Dropping the game server deletes only its ports.
	m.Reconcile([]Mapping{{80, "tcp", "https"}, {443, "tcp", "https"}})
	m.tick(context.Background())
	if f.top("25565/tcp") != "" || f.deletes != 2 {
		t.Fatalf("game ports not removed: %v (%d deletes)", f.entries, f.deletes)
	}
	if m.State("minecraft") != "" {
		t.Fatalf("state for a dropped owner: %q", m.State("minecraft"))
	}
	// Shutdown releases the rest.
	m.release()
	if f.count() != 0 {
		t.Fatalf("mappings left after release: %v", f.entries)
	}
}

func TestRouterRefusal(t *testing.T) {
	m, _ := newTest(&fakeRouter{refuse: map[string]error{"443/tcp": errors.New("ConflictInMappingEntry")}}, nil)
	m.Reconcile([]Mapping{{80, "tcp", "https"}, {443, "tcp", "https"}})
	m.tick(context.Background())
	if s := m.State("https"); s != "router did not forward 443/tcp: ConflictInMappingEntry" {
		t.Fatalf("state %q", s)
	}
}

func TestNoRouter(t *testing.T) {
	m, _ := newTest(nil, errors.New("no UPnP gateway answered"))
	m.Reconcile([]Mapping{{25565, "tcp", "minecraft"}})
	m.tick(context.Background())
	if m.Router() != "no UPnP router found" {
		t.Fatalf("router %q", m.Router())
	}
	if s := m.State("minecraft"); s != "no UPnP router found; forward 25565/tcp to this Node on your router yourself" {
		t.Fatalf("state %q", s)
	}
	// The router appears later: the next pass finds and uses it.
	f := &fakeRouter{}
	m.Discover = func(context.Context) (Router, error) { return f.router(), nil }
	m.tick(context.Background())
	if f.top("25565/tcp") == "" {
		t.Fatal("not mapped once the router appeared")
	}
}

func TestDiscoveryBacksOffAfterAnHour(t *testing.T) {
	calls := 0
	m, _ := newTest(nil, errors.New("no UPnP gateway answered"))
	m.Discover = func(context.Context) (Router, error) { calls++; return nil, errors.New("no") }
	clock := time.Now()
	m.now = func() time.Time { return clock }
	m.Reconcile([]Mapping{{25565, "tcp", "minecraft"}})
	for i := 0; i < 3; i++ {
		m.tick(context.Background())
		clock = clock.Add(time.Minute)
	}
	if calls != 3 {
		t.Fatalf("first hour: %d looks in 3 passes", calls)
	}
	clock = clock.Add(2 * time.Hour)
	for i := 0; i < 5; i++ {
		m.tick(context.Background())
		clock = clock.Add(time.Minute)
	}
	if calls != 4 {
		t.Fatalf("after an hour: %d looks in 5 minute-passes, want 1", calls-3)
	}
}

// Avalon took the web entry's 80 and 443 and Boulder, letting go of them a
// second later, deleted Avalon's new forwards: a router keys mappings by
// external port alone. The Node letting go must leave a forward to another
// machine in place.
func TestLettingGoLeavesAnotherNodesForward(t *testing.T) {
	f := &fakeRouter{}
	boulder := New(slog.Default(), func() string { return "192.168.4.74" })
	boulder.Discover = func(context.Context) (Router, error) { return f.router(), nil }
	avalon := New(slog.Default(), func() string { return "192.168.4.4" })
	avalon.Discover = func(context.Context) (Router, error) { return f.router(), nil }
	web := []Mapping{{80, "tcp", "https"}, {443, "tcp", "https"}}
	boulder.Reconcile(web)
	boulder.tick(context.Background())
	avalon.Reconcile(web)
	avalon.tick(context.Background())
	boulder.Reconcile(nil)
	boulder.tick(context.Background())
	for _, p := range []string{"80/tcp", "443/tcp"} {
		if got := f.top(p); got != "192.168.4.4" {
			t.Fatalf("%s forwarded to %q after Boulder let go, want Avalon", p, got)
		}
	}
	if len(boulder.state) != 0 {
		t.Fatalf("Boulder still tracks %v", boulder.state)
	}
	boulder.release()
	if f.count() != 2 {
		t.Fatalf("Boulder's shutdown removed Avalon's forwards: %v", f.entries)
	}
}

// Tonight's outage: Boulder held the web entry's ports while Avalon
// rebooted, Avalon came back and its Add answered OK while the router went
// on forwarding to Boulder, and Boulder's delete on letting go answered OK
// and deleted nothing. Avalon must see it, clear Boulder's forward and end
// up holding the ports.
func TestTakingOverFromAnotherNodesLeftover(t *testing.T) {
	f := &fakeRouter{}
	boulder := New(slog.Default(), func() string { return "192.168.4.74" })
	boulder.Discover = func(context.Context) (Router, error) { return f.router(), nil }
	web := []Mapping{{80, "tcp", "https"}, {443, "tcp", "https"}}
	boulder.Reconcile(web)
	boulder.tick(context.Background())
	// Boulder lets go, but the router ignores its deletes.
	f.ignoreDeletes = 100
	boulder.Reconcile(nil)
	boulder.tick(context.Background())
	if f.top("443/tcp") != "192.168.4.74" {
		t.Fatalf("setup: 443 goes to %q", f.top("443/tcp"))
	}
	f.ignoreDeletes = 0

	avalon := New(slog.Default(), func() string { return "192.168.4.4" })
	avalon.Discover = func(context.Context) (Router, error) { return f.router(), nil }
	avalon.Reconcile(web)
	avalon.tick(context.Background())
	for _, p := range []string{"80/tcp", "443/tcp"} {
		if got := f.top(p); got != "192.168.4.4" {
			t.Fatalf("%s forwarded to %q, want Avalon", p, got)
		}
	}
	if s := avalon.State("https"); !strings.HasPrefix(s, "forwarded") {
		t.Fatalf("state %q", s)
	}
	// Boulder's retry finds Avalon's forward on top and leaves it.
	boulder.tick(context.Background())
	if f.top("443/tcp") != "192.168.4.4" || len(boulder.gone) != 0 {
		t.Fatalf("after Boulder's retry: 443 to %q, Boulder still letting go of %v", f.top("443/tcp"), boulder.gone)
	}
}

// A port held by something that is not a Node is left alone, reported, and
// tried again each pass until it is free.
func TestPortHeldBySomethingElse(t *testing.T) {
	f := &fakeRouter{entries: map[string][]fakeEntry{"443/tcp": {{"192.168.4.29", "Manual Port Forwarding"}}}}
	m, _ := newTest(f, nil)
	m.Reconcile([]Mapping{{443, "tcp", "https"}})
	m.tick(context.Background())
	if s := m.State("https"); s != "router did not forward 443/tcp: the router still forwards it to 192.168.4.29 (Manual Port Forwarding)" {
		t.Fatalf("state %q", s)
	}
	if f.top("443/tcp") != "192.168.4.29" {
		t.Fatal("cleared a forward that was not a Node's")
	}
	f.mu.Lock()
	f.entries["443/tcp"] = f.entries["443/tcp"][1:]
	f.mu.Unlock()
	m.tick(context.Background())
	if f.top("443/tcp") != "192.168.1.20" {
		t.Fatalf("not taken once free: %q", f.top("443/tcp"))
	}
}

// A forward that stops being this Node's between renewals is noticed on
// the next pass, not half an hour later.
func TestLostForwardIsTakenBack(t *testing.T) {
	f := &fakeRouter{}
	m, _ := newTest(f, nil)
	m.Reconcile([]Mapping{{443, "tcp", "https"}})
	m.tick(context.Background())
	f.mu.Lock()
	f.entries["443/tcp"] = []fakeEntry{{"192.168.4.74", "Maitre https"}, {"192.168.1.20", "Maitre https"}}
	f.mu.Unlock()
	m.tick(context.Background())
	if f.top("443/tcp") != "192.168.1.20" {
		t.Fatalf("443 still goes to %q", f.top("443/tcp"))
	}
}

// A delete the router does not answer is tried again on later passes.
func TestLettingGoRetriesDeletes(t *testing.T) {
	f := &fakeRouter{}
	m, _ := newTest(f, nil)
	m.Reconcile([]Mapping{{443, "tcp", "https"}})
	m.tick(context.Background())
	f.refuseDel = errors.New("context deadline exceeded")
	m.Reconcile(nil)
	m.tick(context.Background())
	if f.top("443/tcp") == "" || len(m.gone) != 1 {
		t.Fatalf("setup: 443 to %q, letting go of %v", f.top("443/tcp"), m.gone)
	}
	f.refuseDel = nil
	m.tick(context.Background())
	if f.top("443/tcp") != "" || len(m.gone) != 0 {
		t.Fatalf("443 still to %q after the retry, letting go of %v", f.top("443/tcp"), m.gone)
	}
}
