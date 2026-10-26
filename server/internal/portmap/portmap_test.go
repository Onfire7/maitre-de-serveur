package portmap

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRouter struct {
	mu      sync.Mutex
	mapped  map[string]string // "port/proto" -> internal ip
	refuse  map[string]error
	adds    int
	deletes int
}

func (f *fakeRouter) Name() string { return "FakeBox 3000" }
func (f *fakeRouter) Add(_ context.Context, m Mapping, ip string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adds++
	if err := f.refuse[m.String()]; err != nil {
		return err
	}
	if f.mapped == nil {
		f.mapped = map[string]string{}
	}
	f.mapped[m.String()] = ip
	return nil
}
func (f *fakeRouter) Delete(_ context.Context, m Mapping) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	delete(f.mapped, m.String())
	return nil
}

func newTest(r Router, derr error) (*Manager, *fakeRouter) {
	f, _ := r.(*fakeRouter)
	m := New(slog.Default(), func() string { return "192.168.1.20" })
	m.Discover = func(context.Context) (Router, error) { return r, derr }
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
	if got := f.mapped["25565/tcp"]; got != "192.168.1.20" {
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
	if _, ok := f.mapped["25565/tcp"]; ok || f.deletes != 2 {
		t.Fatalf("game ports not removed: %v (%d deletes)", f.mapped, f.deletes)
	}
	if m.State("minecraft") != "" {
		t.Fatalf("state for a dropped owner: %q", m.State("minecraft"))
	}
	// Shutdown releases the rest.
	m.release()
	if len(f.mapped) != 0 {
		t.Fatalf("mappings left after release: %v", f.mapped)
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
	m.Discover = func(context.Context) (Router, error) { return f, nil }
	m.tick(context.Background())
	if f.mapped["25565/tcp"] == "" {
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
