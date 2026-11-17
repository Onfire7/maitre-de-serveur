package relay_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/relay"
)

// journal is a slog.Handler that keeps every record, as the Node's
// journal would.
type journal struct {
	mu      sync.Mutex
	records []map[string]any
}

func (j *journal) Enabled(context.Context, slog.Level) bool { return true }
func (j *journal) WithAttrs([]slog.Attr) slog.Handler       { return j }
func (j *journal) WithGroup(string) slog.Handler            { return j }
func (j *journal) Handle(_ context.Context, r slog.Record) error {
	m := map[string]any{"msg": r.Message}
	r.Attrs(func(a slog.Attr) bool { m[a.Key] = a.Value.Any(); return true })
	j.mu.Lock()
	j.records = append(j.records, m)
	j.mu.Unlock()
	return nil
}

func (j *journal) lines(msg string) []map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []map[string]any
	for _, r := range j.records {
		if r["msg"] == msg {
			out = append(out, r)
		}
	}
	return out
}

func TestTheAnchorRefusesADevicePastItsRelayedConnectionsAndNotAnother(t *testing.T) {
	f := newFleet(t)
	target := f.target.id.Fingerprint()
	held := make([]net.Conn, 0, relay.PerDevice)
	t.Cleanup(func() {
		for _, c := range held {
			c.Close()
		}
	})
	for i := range relay.PerDevice {
		c, err := relayed(t, f.device, f.anchor, target)
		if err != nil {
			t.Fatalf("relayed connection %d: %v", i+1, err)
		}
		held = append(held, c)
	}
	_, err := relayed(t, f.device, f.anchor, target)
	refused(t, err, fmt.Sprintf("429 Too Many Requests: this Device already holds %d relayed connections", relay.PerDevice))

	// Another Device is unaffected, and is carried to the target.
	other, err := relayed(t, f.outsider, f.anchor, target)
	if err != nil {
		t.Fatalf("another Device: %v", err)
	}
	inner := tls.Client(other, pinned(f.outsider, target, "http/1.1"))
	if got, want := get(t, inner, "/whoami"), fmt.Sprintf("200 %s %s HTTP/1.1", target, f.outsider.Fingerprint()); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	other.Close()

	// Once the Device lets one go, it may open another.
	held[0].Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := relayed(t, f.device, f.anchor, target)
		if err == nil {
			held[0] = c
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after letting one go: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// counted counts the bytes through a connection.
type counted struct {
	net.Conn
	read, written atomic.Int64
}

func (c *counted) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.read.Add(int64(n))
	return n, err
}

func (c *counted) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.written.Add(int64(n))
	return n, err
}

func TestEachRelayedConnectionIsOneJournalLine(t *testing.T) {
	f := newFleet(t)
	target := f.target.id.Fingerprint()
	before := time.Now().Truncate(time.Second)
	outer, err := relayed(t, f.device, f.anchor, target)
	if err != nil {
		t.Fatal(err)
	}
	c := &counted{Conn: outer}
	inner := tls.Client(c, pinned(f.device, target, "http/1.1"))
	get(t, inner, "/whoami")
	if n := len(f.anchor.log.lines("relayed connection")); n != 0 {
		t.Fatalf("%d journal lines while the connection is open", n)
	}
	// The Device ends its side, then reads what the anchor still sends.
	if err := outer.(*tls.Conn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, c)
	outer.Close()

	var lines []map[string]any
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if lines = f.anchor.log.lines("relayed connection"); len(lines) > 0 {
			break
		}
	}
	time.Sleep(50 * time.Millisecond)
	lines = f.anchor.log.lines("relayed connection")
	if len(lines) != 1 {
		t.Fatalf("%d journal lines, want 1", len(lines))
	}
	l := lines[0]
	if l["device"] != f.device.Fingerprint() || l["target"] != target {
		t.Fatalf("device %v target %v", l["device"], l["target"])
	}
	start, err1 := time.Parse(time.RFC3339, fmt.Sprint(l["start"]))
	end, err2 := time.Parse(time.RFC3339, fmt.Sprint(l["end"]))
	if err1 != nil || err2 != nil || start.Before(before) || end.Before(start) || end.After(time.Now()) {
		t.Fatalf("start %v end %v", l["start"], l["end"])
	}
	if got, want := l["bytes_to_target"], c.written.Load(); got != want {
		t.Fatalf("bytes_to_target %v, the Device sent %d", got, want)
	}
	if got, want := l["bytes_from_target"], c.read.Load(); got != want {
		t.Fatalf("bytes_from_target %v, the Device received %d", got, want)
	}
	if strings.Contains(fmt.Sprint(f.target.log.lines("relayed connection")), target) {
		t.Fatal("the target journals the relayed connection too")
	}
}

func TestASilentRelayedConnectionStaysOpen(t *testing.T) {
	// The Nodes' servers have one timeout, for a request's headers. Made
	// short, a connection silent for several times as long stays open:
	// nothing on the path, Device to anchor, anchor to target, or the
	// target's server, closes a connection for saying nothing.
	was := mtls.HeaderTimeout
	mtls.HeaderTimeout = 200 * time.Millisecond
	t.Cleanup(func() { mtls.HeaderTimeout = was })
	f := newFleet(t)
	target := f.target.id.Fingerprint()
	outer, err := relayed(t, f.device, f.anchor, target)
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	inner := tls.Client(outer, pinned(f.device, target, "http/1.1"))
	want := fmt.Sprintf("200 %s %s HTTP/1.1", target, f.device.Fingerprint())
	if got := get(t, inner, "/whoami"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	time.Sleep(10 * mtls.HeaderTimeout)
	if got := get(t, inner, "/whoami"); got != want {
		t.Fatalf("after the silence: got %q, want %q", got, want)
	}
}
