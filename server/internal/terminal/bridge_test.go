package terminal_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Onfire7/maitre-de-serveur/server/internal/svcproxy"
	"github.com/Onfire7/maitre-de-serveur/server/internal/terminal"
)

// loopback stands in for the Desktop's loopback proxy: it relays a
// connection to the Terminal endpoint only for a token it minted, once.
type loopback struct {
	mu     sync.Mutex
	tokens map[string]string // token -> Service
	url    string
}

func newLoopback(t *testing.T, e env) *loopback {
	t.Helper()
	l := &loopback{tokens: map[string]string{}}
	node, _ := url.Parse(e.url)
	rp := httputil.NewSingleHostReverseProxy(node)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		service, ok := l.tokens[strings.TrimPrefix(r.URL.Path, "/")]
		delete(l.tokens, strings.TrimPrefix(r.URL.Path, "/"))
		l.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		r.URL.Path = terminal.Path
		r.Header.Set(svcproxy.Header, service)
		r.Header.Set("X-Test-Peer", "laptop")
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	l.url = "ws" + strings.TrimPrefix(srv.URL, "http")
	return l
}

func (l *loopback) mint(service string) string {
	b := make([]byte, 16)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	l.mu.Lock()
	l.tokens[tok] = service
	l.mu.Unlock()
	return l.url + "/" + tok
}

// ticket writes what the Desktop hands the bridge: where to connect, with
// a one-time token, and which Terminal to attach to.
func ticket(t *testing.T, wsURL, id string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ticket")
	b, _ := json.Marshal(terminal.Ticket{URL: wsURL, Terminal: id})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// screen is the person's own terminal app: what the bridge writes to it,
// and a pipe for what they type.
type screen struct {
	mu  sync.Mutex
	out bytes.Buffer
	in  *io.PipeWriter
}

func (s *screen) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.Write(b)
}

func (s *screen) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.String()
}

type bridged struct {
	*screen
	cancel context.CancelFunc
	done   chan struct{}
	code   int
	err    error
}

func bridge(t *testing.T, ticketPath string) *bridged {
	t.Helper()
	r, w := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	b := &bridged{screen: &screen{in: w}, cancel: cancel, done: make(chan struct{})}
	t.Cleanup(func() { cancel(); w.Close(); <-b.done })
	go func() {
		defer close(b.done)
		b.code, b.err = terminal.Bridge(ctx, ticketPath, terminal.BridgeIO{In: r, Out: b.screen, Size: func() (uint32, uint32) { return 100, 30 }})
	}()
	return b
}

func (b *bridged) typ(text string) { b.in.Write([]byte(text)) }

func (b *bridged) until(t *testing.T, want string) {
	t.Helper()
	eventually(t, "the bridge shows "+want+" (has "+b.String()+")", func() bool { return strings.Contains(b.String(), want) })
}

func (b *bridged) finished(t *testing.T) {
	t.Helper()
	select {
	case <-b.done:
	case <-time.After(15 * time.Second):
		t.Fatalf("the bridge never finished (screen %q)", b.String())
	}
}

// Open in your terminal: the bridge attaches to the very Terminal the
// embedded view shows. Each sees what the other typed, and closing the
// bridge leaves the Terminal running.
func TestTheBridgeAttachesToTheSameTerminalAsTheView(t *testing.T) {
	e := start(t, "kuma")
	e.container(t, "kuma", "", "bash")
	l := newLoopback(t, e)
	view, id := opened(t, e, "kuma")
	view.typ("echo pid-$$\n")
	pid := view.pid()

	b := bridge(t, ticket(t, l.mint("kuma"), id))
	b.until(t, "pid-"+pid) // the replay
	b.typ("echo from-the-br''idge-$$\n")
	b.until(t, "from-the-bridge-"+pid)
	view.until("from-the-bridge-" + pid)
	view.typ("echo from-the-v''iew\n")
	b.until(t, "from-the-view")

	b.cancel()
	b.finished(t)
	if ts := list(t, e, "kuma"); len(ts) != 1 || !ts[0].GetRunning() {
		t.Fatalf("after the bridge closed: %v", ts)
	}
	view.typ("echo still-h''ere\n")
	view.until("still-here")
}

// The bridge runs only from a ticket: none, a missing file, or one already
// used (the bridge removes it as it reads it) are refused before it
// connects anywhere; and the token in it works once.
func TestTheBridgeRefusesToRunWithoutAOneTimeTicket(t *testing.T) {
	e := start(t, "kuma")
	e.container(t, "kuma", "", "bash")
	l := newLoopback(t, e)
	_, id := opened(t, e, "kuma")

	for _, p := range []string{"", filepath.Join(t.TempDir(), "none")} {
		b := bridge(t, p)
		b.finished(t)
		if b.err != terminal.ErrNoTicket {
			t.Fatalf("ticket %q: %v", p, b.err)
		}
	}

	url := l.mint("kuma")
	p := ticket(t, url, id)
	first := bridge(t, p)
	first.typ("echo fi''rst\n")
	first.until(t, "first")
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("the ticket is still there: %v", err)
	}
	again := bridge(t, p)
	again.finished(t)
	if again.err != terminal.ErrNoTicket {
		t.Fatalf("a used ticket: %v", again.err)
	}
	// The token itself, copied into a new ticket, is spent too.
	spent := bridge(t, ticket(t, url, id))
	spent.finished(t)
	if spent.err == nil {
		t.Fatal("a spent token connected")
	}
}

// When the Terminal's process exits, the bridge ends with its code.
func TestTheBridgeEndsWithTheTerminalsExitCode(t *testing.T) {
	e := start(t, "kuma")
	e.container(t, "kuma", "", "bash")
	l := newLoopback(t, e)
	_, id := opened(t, e, "kuma")
	b := bridge(t, ticket(t, l.mint("kuma"), id))
	b.typ("echo re''ady\n")
	b.until(t, "ready")
	b.typ("exit 3\n")
	b.finished(t)
	if b.err != nil || b.code != 3 {
		t.Fatalf("code %d, err %v", b.code, b.err)
	}
}
