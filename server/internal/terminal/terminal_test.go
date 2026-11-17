package terminal_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/svcproxy"
	"github.com/Onfire7/maitre-de-serveur/server/internal/terminal"
)

// fakePodman (scripts/fake-podman) stands in for podman where there is
// none: a container is a directory of the programs it has, which run on
// this machine, so the shell and its PTY are real.
const fakePodman = "../../../scripts/fake-podman"

// roles is a trust list: who is a Node and who holds only the Recovery Phrase.
type roles struct{ nodes, limited map[string]bool }

func (r roles) Node(fp string) bool    { return r.nodes[fp] }
func (r roles) Limited(fp string) bool { return r.limited[fp] }

type env struct {
	url  string
	root string
	h    *terminal.Handler
	dir  string
	st   *store.Store
	srv  *httptest.Server

	podman   string
	launcher terminal.Launcher
}

// container makes a pretend container for Service name with the given
// shells, each the real program of this machine.
func (e env) container(t *testing.T, name, user string, shells ...string) {
	t.Helper()
	bin := filepath.Join(e.root, name, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, s := range shells {
		real := "/bin/" + s
		if _, err := os.Stat(real); err != nil {
			real = "/usr/bin/" + s
		}
		if err := os.Symlink(real, filepath.Join(bin, s)); err != nil {
			t.Fatal(err)
		}
	}
	if user != "" {
		os.WriteFile(filepath.Join(e.root, name, "user"), []byte(user+"\n"), 0o644)
	}
}

// start serves the Terminal endpoint the way the agent does: on the
// Service tunnel, ahead of the Service's own pages. The caller is whoever
// X-Test-Peer names.
func start(t *testing.T, svcs ...string) env {
	t.Helper()
	return startWith(t, terminal.InProcess(), "", svcs...)
}

// startWith is start with a given launcher and sessions directory ("" is a
// new one).
func startWith(t *testing.T, launcher terminal.Launcher, sock string, svcs ...string) env {
	t.Helper()
	dir := t.TempDir()
	podman, err := filepath.Abs(fakePodman)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "containers")
	os.MkdirAll(root, 0o755)
	t.Setenv("MAITRE_FAKE_PODMAN_ROOT", root)
	ctx := context.Background()
	var st *store.Store
	st, err = store.Open(filepath.Join(dir, "f.db"), "n")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, s := range svcs {
		st.Put(ctx, services.KindService, s, &maitrev1.ServiceSpec{Name: s}, false)
	}
	if sock == "" {
		sock = sockets(t)
	}
	return serveAgent(t, env{root: root, dir: sock, st: st, podman: podman, launcher: launcher})
}

// serveAgent serves the endpoint for e's store, containers and sockets, as
// a (new) agent would.
func serveAgent(t *testing.T, e env) env {
	t.Helper()
	h := terminal.New(terminal.Config{
		Store:    e.st,
		Roles:    roles{nodes: map[string]bool{"node": true}, limited: map[string]bool{"phrase": true}},
		Podman:   e.podman,
		Launcher: e.launcher,
		Dir:      e.dir,
	})
	inner := h.Wrap(svcproxy.Header, svcproxy.Handler(e.st, http.NotFoundHandler()))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fp := r.Header.Get("X-Test-Peer"); fp != "" {
			r = r.WithContext(peer.WithInfo(r.Context(), peer.Info{Fingerprint: fp}))
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	e.url, e.h, e.srv = srv.URL, h, srv
	return e
}

// sockets is a directory for the holders' sockets, short enough for a
// unix socket's path.
func sockets(t *testing.T) string {
	t.Helper()
	sock, err := os.MkdirTemp("", "mt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sock) })
	return sock
}

type session struct {
	t   *testing.T
	c   *websocket.Conn
	out bytes.Buffer
}

func open(t *testing.T, e env, service, who string) (*session, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h := http.Header{}
	h.Set(svcproxy.Header, service)
	if who != "" {
		h.Set("X-Test-Peer", who)
	}
	c, res, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.url, "http")+terminal.Path, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		return nil, res, err
	}
	c.SetReadLimit(1 << 20)
	t.Cleanup(func() { c.CloseNow() })
	return &session{t: t, c: c}, res, nil
}

func (s *session) send(f *maitrev1.TerminalFrame) {
	s.t.Helper()
	b, _ := proto.Marshal(f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.c.Write(ctx, websocket.MessageBinary, b); err != nil {
		s.t.Fatal(err)
	}
}

func (s *session) start(service string, cols, rows uint32) {
	s.send(&maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Start{Start: &maitrev1.TerminalStart{Service: service, Size: &maitrev1.TerminalSize{Cols: cols, Rows: rows}}}})
}

func (s *session) typ(text string) {
	s.send(&maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Input{Input: &maitrev1.TerminalInput{Data: []byte(text)}}})
}

// next reads one frame, keeping what output it carries.
func (s *session) next() *maitrev1.TerminalFrame {
	s.t.Helper()
	{
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		typ, b, err := s.c.Read(ctx)
		cancel()
		if err != nil {
			s.t.Fatalf("read: %v (output so far %q)", err, s.out.String())
		}
		if typ != websocket.MessageBinary {
			s.t.Fatalf("a %v message", typ)
		}
		var f maitrev1.TerminalFrame
		if err := proto.Unmarshal(b, &f); err != nil {
			s.t.Fatal(err)
		}
		if o := f.GetOutput(); o != nil {
			s.out.Write(o.GetData())
		}
		return &f
	}
}

// until reads output until it contains want; anything else fails.
func (s *session) until(want string) {
	s.t.Helper()
	for !strings.Contains(s.out.String(), want) {
		if f := s.next(); f.GetOutput() == nil {
			s.t.Fatalf("waiting for %q, got %v (output %q)", want, f, s.out.String())
		}
	}
}

// exit reads past output to the exit frame.
func (s *session) exit() *maitrev1.TerminalExit {
	s.t.Helper()
	for {
		f := s.next()
		if x := f.GetExit(); x != nil {
			return x
		}
		if f.GetOutput() == nil {
			s.t.Fatalf("expected the exit, got %v", f)
		}
	}
}

// A Trusted Device opens a Terminal into a running Service: it learns where
// the shell runs and as whom, what it types runs there, a resize reaches the
// shell, and when the shell exits the last frame carries its code.
func TestATrustedDeviceTypesIntoTheServicesShell(t *testing.T) {
	e := start(t, "kuma")
	e.container(t, "kuma", "", "bash", "sh")
	s, _, err := open(t, e, "kuma", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	s.start("kuma", 80, 24)
	st := s.next().GetStarted()
	if st == nil || st.GetContainer() != "kuma" || st.GetUser() != "root" || st.GetId() == "" {
		t.Fatalf("started: %v", st)
	}
	if argv := st.GetArgv(); len(argv) != 1 || argv[0] != "/bin/bash" {
		t.Fatalf("argv %v", argv)
	}
	s.typ("echo hello-$((40+2))\n")
	s.until("hello-42")
	s.typ("stty size\n")
	s.until("24 80")
	s.send(&maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Resize{Resize: &maitrev1.TerminalResize{Size: &maitrev1.TerminalSize{Cols: 132, Rows: 41}}}})
	s.typ("stty size\n")
	s.until("41 132")
	s.typ("exit 7\n")
	x := s.exit()
	if x.GetReason() != maitrev1.TerminalExit_REASON_EXITED || x.GetCode() != 7 {
		t.Fatalf("exit %v", x)
	}
	// Then the Node closes the WebSocket.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := s.c.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("after the exit: %v", err)
	}
}

// The shell is /bin/bash, else /bin/sh, and runs as the image's user.
func TestTheShellFallsBackToShAndRunsAsTheImagesUser(t *testing.T) {
	e := start(t, "memos")
	e.container(t, "memos", "1000", "sh")
	s, _, err := open(t, e, "memos", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	s.start("memos", 80, 24)
	st := s.next().GetStarted()
	if st == nil || st.GetUser() != "1000" || len(st.GetArgv()) != 1 || st.GetArgv()[0] != "/bin/sh" {
		t.Fatalf("started: %v", st)
	}
	s.typ("exit\n")
	if x := s.exit(); x.GetReason() != maitrev1.TerminalExit_REASON_EXITED || x.GetCode() != 0 {
		t.Fatalf("exit %v", x)
	}
}

// A container with neither /bin/bash nor /bin/sh says so, and nothing runs.
func TestAContainerWithoutAShellSaysSo(t *testing.T) {
	e := start(t, "distroless")
	e.container(t, "distroless", "")
	s, _, err := open(t, e, "distroless", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	s.start("distroless", 80, 24)
	x := s.next().GetExit()
	if x.GetReason() != maitrev1.TerminalExit_REASON_NO_SHELL || x.GetMessage() != "This container has no shell" {
		t.Fatalf("exit %v", x)
	}
}

// A Service whose container is not running has no Terminal to open.
func TestAServiceThatIsNotRunningHasNoTerminal(t *testing.T) {
	e := start(t, "kuma")
	s, _, err := open(t, e, "kuma", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	s.start("kuma", 80, 24)
	x := s.next().GetExit()
	if x.GetReason() != maitrev1.TerminalExit_REASON_FAILED || !strings.Contains(x.GetMessage(), "not running") {
		t.Fatalf("exit %v", x)
	}
}

// Only a full Trusted Device opens a Terminal (ADR 0011): a Node key, the
// Recovery Phrase alone, or no client key at all is refused before the
// WebSocket opens, as is a Service the Node does not have.
func TestOnlyAFullTrustedDeviceOpensATerminal(t *testing.T) {
	e := start(t, "kuma")
	e.container(t, "kuma", "", "sh")
	for who, want := range map[string]int{"node": http.StatusForbidden, "phrase": http.StatusForbidden, "": http.StatusUnauthorized} {
		_, res, err := open(t, e, "kuma", who)
		if err == nil || res == nil || res.StatusCode != want {
			t.Fatalf("%q: err %v, res %v, want %d", who, err, res, want)
		}
	}
	_, res, err := open(t, e, "ghost", "laptop")
	if err == nil || res == nil || res.StatusCode != http.StatusNotFound {
		t.Fatalf("no such Service: err %v res %v", err, res)
	}
}

// The start frame names the Service the tunnel names; another is refused.
func TestTheStartMustNameTheTunnelsService(t *testing.T) {
	e := start(t, "kuma", "vault")
	e.container(t, "kuma", "", "sh")
	e.container(t, "vault", "", "sh")
	s, _, err := open(t, e, "kuma", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	s.start("vault", 80, 24)
	if x := s.next().GetExit(); x.GetReason() != maitrev1.TerminalExit_REASON_FAILED {
		t.Fatalf("exit %v", x)
	}
}

// Requests on the tunnel that are not for the Terminal still reach the Service.
func TestOtherPathsStillReachTheService(t *testing.T) {
	e := start(t, "kuma")
	req, _ := http.NewRequest("GET", e.url+"/", nil)
	req.Header.Set(svcproxy.Header, "kuma")
	req.Header.Set("X-Test-Peer", "laptop")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	// kuma has no Page, so svcproxy answers it has none: the Terminal did not take it.
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", res.StatusCode)
	}
}
