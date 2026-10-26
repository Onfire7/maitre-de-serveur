package mdns

import (
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/holoplot/go-avahi"
)

// cpuTime is this process's CPU time so far.
func cpuTime() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func requireAvahi(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Skip("no Avahi on CI runners")
	}
	conn, server, err := connect()
	if err != nil {
		t.Skip("Avahi not reachable:", err)
	}
	closeAll(conn, server)
}

// Closing an advertisement must stop its Avahi client's goroutines and must
// not disturb other advertisements, which used to share one D-Bus
// connection: the first Close closed everyone's signal channel and go-avahi
// spun on it (one core per client).
func TestCloseIsQuietAndIsolated(t *testing.T) {
	requireAvahi(t)
	a, err := PublishHosts([]string{"maitre-test-a.local"}, []string{"192.0.2.10"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := PublishHosts([]string{"maitre-test-b.local"}, []string{"192.0.2.11"})
	if err != nil {
		t.Fatal(err)
	}
	before := runtime.NumGoroutine()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	cpu0 := cpuTime()
	time.Sleep(300 * time.Millisecond)
	if burned := cpuTime() - cpu0; burned > 100*time.Millisecond {
		t.Fatalf("process burned %v of CPU in 300ms after Close: a dispatch loop is spinning", burned)
	}
	if after := runtime.NumGoroutine(); after >= before {
		t.Fatalf("goroutines %d before Close, %d after: the client's loop did not exit", before, after)
	}
	// b still has a live connection: re-committing its group must work.
	if err := b.group.Reset(); err != nil {
		t.Fatalf("second advertisement broken by the first Close: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}

// The patched go-avahi copy: when the bus connection is closed underneath
// an Avahi client (an avahi-daemon restart does this), its dispatch loop
// must end instead of spinning.
func TestDispatchLoopEndsWhenBusCloses(t *testing.T) {
	requireAvahi(t)
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		t.Skip(err)
	}
	if _, err := avahi.ServerNew(conn); err != nil {
		t.Skip(err)
	}
	before := runtime.NumGoroutine()
	conn.Close()
	time.Sleep(200 * time.Millisecond)
	cpu0 := cpuTime()
	time.Sleep(300 * time.Millisecond)
	if burned := cpuTime() - cpu0; burned > 100*time.Millisecond {
		t.Fatalf("process burned %v of CPU in 300ms after the bus closed: dispatch loop spinning", burned)
	}
	if after := runtime.NumGoroutine(); after >= before {
		t.Fatalf("goroutines %d before, %d after: dispatch loop did not exit", before, after)
	}
}
