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
	st   *store.Store
}

// put records a Service as the Node knows it.
func (e env) put(t *testing.T, spec *maitrev1.ServiceSpec) {
	t.Helper()
	if _, err := e.st.Put(context.Background(), services.KindService, spec.GetName(), spec, false); err != nil {
		t.Fatal(err)
	}
}

// program puts a program at path in a pretend container: this machine's
// real one.
func (e env) program(t *testing.T, ctr, path, real string) {
	t.Helper()
	at := filepath.Join(e.root, ctr, path)
	os.MkdirAll(filepath.Dir(at), 0o755)
	if err := os.Symlink(real, at); err != nil {
		t.Fatal(err)
	}
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
	sock, err := os.MkdirTemp("", "mt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sock) })
	h := terminal.New(terminal.Config{
		Store:    st,
		Roles:    roles{nodes: map[string]bool{"node": true}, limited: map[string]bool{"phrase": true}},
		Podman:   podman,
		Launcher: terminal.InProcess(sock),
	})
	inner := h.Wrap(svcproxy.Header, svcproxy.Handler(st, http.NotFoundHandler()))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fp := r.Header.Get("X-Test-Peer"); fp != "" {
			r = r.WithContext(peer.WithInfo(r.Context(), peer.Info{Fingerprint: fp}))
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return env{url: srv.URL, root: root, st: st}
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
	s.startWith(&maitrev1.TerminalStart{Service: service, Size: &maitrev1.TerminalSize{Cols: cols, Rows: rows}})
}

func (s *session) startWith(st *maitrev1.TerminalStart) {
	s.send(&maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Start{Start: st}})
}

// started reads the started frame, failing on anything else.
func (s *session) started() *maitrev1.TerminalStarted {
	s.t.Helper()
	f := s.next()
	if f.GetStarted() == nil {
		s.t.Fatalf("expected started, got %v", f)
	}
	return f.GetStarted()
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

// In a Service of several containers, the Terminal opens in the one the
// start names, the first when it names none, and says which.
func TestATerminalOpensInTheChosenContainer(t *testing.T) {
	e := start(t)
	e.put(t, &maitrev1.ServiceSpec{Name: "photos", Components: []*maitrev1.ServiceComponent{{Name: "server"}, {Name: "database"}}})
	e.container(t, "photos-server", "", "sh")
	e.container(t, "photos-database", "999", "sh")
	os.WriteFile(filepath.Join(e.root, "photos-database", "marker"), nil, 0o644)

	s, _, err := open(t, e, "photos", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	s.startWith(&maitrev1.TerminalStart{Service: "photos", Component: "database", Size: &maitrev1.TerminalSize{Cols: 80, Rows: 24}})
	if st := s.started(); st.GetContainer() != "database" || st.GetUser() != "999" {
		t.Fatalf("started: %v", st)
	}
	// It runs in that container: the pretend container's directory.
	s.typ("echo in=$(basename $MAITRE_FAKE_PODMAN_CONTAINER)\n")
	s.until("in=photos-database")

	first, _, err := open(t, e, "photos", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	first.start("photos", 80, 24)
	if st := first.started(); st.GetContainer() != "server" {
		t.Fatalf("started: %v", st)
	}

	other, _, err := open(t, e, "photos", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	other.startWith(&maitrev1.TerminalStart{Service: "photos", Component: "cache"})
	if x := other.next().GetExit(); x.GetReason() != maitrev1.TerminalExit_REASON_FAILED || !strings.Contains(x.GetMessage(), "cache") {
		t.Fatalf("exit %v", x)
	}
}

// Run as root runs the shell as uid 0, whatever user the image names;
// without it the image's user is used.
func TestRunAsRootOverridesTheImagesUser(t *testing.T) {
	e := start(t, "memos")
	e.container(t, "memos", "1000", "sh")
	s, _, err := open(t, e, "memos", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	s.startWith(&maitrev1.TerminalStart{Service: "memos", Root: true, Size: &maitrev1.TerminalSize{Cols: 80, Rows: 24}})
	if st := s.started(); st.GetUser() != "root" {
		t.Fatalf("started: %v", st)
	}
	// The fake podman says what --user it was given.
	s.typ("echo as=[$MAITRE_FAKE_PODMAN_USER]\n")
	s.until("as=[0]")

	plain, _, err := open(t, e, "memos", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	plain.start("memos", 80, 24)
	if st := plain.started(); st.GetUser() != "1000" {
		t.Fatalf("started: %v", st)
	}
	plain.typ("echo as=[$MAITRE_FAKE_PODMAN_USER]\n")
	plain.until("as=[]")
}

// The shell is the Catalog's `shell:` when the container has it, else the
// one set in Service Configuration, else /bin/bash, else /bin/sh, else
// none; the started frame names the whole argv.
func TestTheShellsAreTriedInOrder(t *testing.T) {
	sh := "/bin/sh"
	if _, err := os.Stat(sh); err != nil {
		sh = "/usr/bin/sh"
	}
	for _, c := range []struct {
		name     string
		catalog  []string
		set      []string
		programs []string
		want     []string
	}{
		{"the Catalog's first", []string{"/opt/a/sh", "-i"}, []string{"/opt/b/sh"}, []string{"/opt/a/sh", "/opt/b/sh", "/bin/bash", "/bin/sh"}, []string{"/opt/a/sh", "-i"}},
		{"then the one set", []string{"/opt/a/sh", "-i"}, []string{"/opt/b/sh", "-i"}, []string{"/opt/b/sh", "/bin/bash", "/bin/sh"}, []string{"/opt/b/sh", "-i"}},
		{"then bash", []string{"/opt/a/sh"}, []string{"/opt/b/sh"}, []string{"/bin/bash", "/bin/sh"}, []string{"/bin/bash"}},
		{"then sh", nil, []string{"/opt/b/sh"}, []string{"/bin/sh"}, []string{"/bin/sh"}},
		{"else none", []string{"/opt/a/sh"}, []string{"/opt/b/sh"}, nil, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := start(t)
			spec := &maitrev1.ServiceSpec{Name: "app", Shell: c.catalog}
			if c.set != nil {
				spec.Shells = []*maitrev1.ContainerShell{{Argv: c.set}}
			}
			e.put(t, spec)
			os.MkdirAll(filepath.Join(e.root, "app"), 0o755)
			for _, p := range c.programs {
				e.program(t, "app", p, sh)
			}
			s, _, err := open(t, e, "app", "laptop")
			if err != nil {
				t.Fatal(err)
			}
			s.start("app", 80, 24)
			f := s.next()
			if c.want == nil {
				if x := f.GetExit(); x.GetReason() != maitrev1.TerminalExit_REASON_NO_SHELL {
					t.Fatalf("got %v", f)
				}
				return
			}
			if got := f.GetStarted().GetArgv(); strings.Join(got, " ") != strings.Join(c.want, " ") {
				t.Fatalf("argv %v, want %v (%v)", got, c.want, f)
			}
			s.typ("echo ran-$((1+1))\n")
			s.until("ran-2")
		})
	}
}

// A component's shells are its own: the Catalog's on the component, and
// the one set for that component.
func TestAComponentsShellIsItsOwn(t *testing.T) {
	sh := "/bin/sh"
	if _, err := os.Stat(sh); err != nil {
		sh = "/usr/bin/sh"
	}
	e := start(t)
	e.put(t, &maitrev1.ServiceSpec{Name: "photos",
		Components: []*maitrev1.ServiceComponent{{Name: "server", Shell: []string{"/opt/server/sh"}}, {Name: "database"}},
		Shells:     []*maitrev1.ContainerShell{{Component: "database", Argv: []string{"/opt/db/sh"}}, {Component: "server", Argv: []string{"/opt/wrong/sh"}}},
	})
	e.program(t, "photos-server", "/opt/server/sh", sh)
	e.program(t, "photos-server", "/opt/db/sh", sh)
	e.program(t, "photos-database", "/opt/server/sh", sh)
	e.program(t, "photos-database", "/opt/db/sh", sh)
	for comp, want := range map[string]string{"server": "/opt/server/sh", "database": "/opt/db/sh"} {
		s, _, err := open(t, e, "photos", "laptop")
		if err != nil {
			t.Fatal(err)
		}
		s.startWith(&maitrev1.TerminalStart{Service: "photos", Component: comp})
		if got := s.started().GetArgv(); len(got) != 1 || got[0] != want {
			t.Fatalf("%s: argv %v", comp, got)
		}
	}
}
