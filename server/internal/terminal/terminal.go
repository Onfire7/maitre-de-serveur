// Package terminal serves Terminals (ADR 0011): a Trusted Device's shell
// inside one of a Service's containers, held by the Service's Node.
//
// The Console opens a WebSocket on the Service tunnel (decision 71): a
// request naming the Service in X-Maitre-Service, to Path. Every binary
// message is one maitre.v1.TerminalFrame. The Node finds the running
// container the start names, its shell (the Catalog's `shell:`, the one set
// in Service Configuration, /bin/bash, /bin/sh) and its image's user or
// root, and starts `podman exec -it` through a Launcher, which in the agent
// puts it in a transient systemd unit of its own (Hold, `maitre
// terminal-hold`) that holds the PTY and serves the session on a unix
// socket. The agent connects to that socket and passes frames both ways, so
// a later agent can reconnect to a session it did not start.
//
// A Terminal belongs to the Node, not to a connection (#5): closing the
// view, losing the connection or the agent restarting leaves it running in
// its holder, which keeps about 1 MiB of its recent output, in memory only,
// to replay to the next view. The agent keeps no state of its own: it finds
// the Terminals by their sockets in Config.Dir and asks each. TerminalService
// (ListTerminals, EndTerminal) lists and Ends them.
package terminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Path is where a Terminal's WebSocket is opened on the Service tunnel. The
// Desktop's page proxies never relay /__maitre/ paths, so a Service's own
// page cannot reach it.
const Path = "/__maitre/terminal"

// Feature is what a Node lists in NodeInfo.features when it serves Terminals.
const Feature = "terminal"

// NoShell is what the Console shows for a container without a shell.
const NoShell = "This container has no shell"

// MaxPerContainer is how many running Terminals one container may hold
// (ADR 0011).
const MaxPerContainer = 10

// Shells are tried in order, after the container's declared shells.
var Shells = []string{"/bin/bash", "/bin/sh"}

// Roles is what the endpoint needs from the trust list.
type Roles interface {
	Node(fingerprint string) bool
	Limited(fingerprint string) bool
}

// Config is what a Handler needs.
type Config struct {
	Store    *store.Store
	Roles    Roles
	Launcher Launcher
	// Podman is the podman binary; "" is "podman".
	Podman string
	// Dir holds the holders' sockets, <id>.sock (/run/maitre/terminals):
	// where an agent finds the Terminals, including after it restarts.
	Dir string
	// Run runs a command for its output; nil runs it for real.
	Run func(ctx context.Context, name string, args ...string) (string, error)
	Log *slog.Logger
}

// Handler is the Terminal endpoint.
type Handler struct {
	cfg Config
	// starting is held while a Terminal is counted and launched, so two
	// opened at once cannot both be a container's 11th.
	starting sync.Mutex
}

// New makes the endpoint.
func New(cfg Config) *Handler {
	if cfg.Podman == "" {
		cfg.Podman = "podman"
	}
	if cfg.Run == nil {
		cfg.Run = services.ExecRunner{}.Run
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Handler{cfg: cfg}
}

// Wrap sends Terminal requests on the Service tunnel (svcHeader set, Path)
// to the endpoint and everything else to next.
func (h *Handler) Wrap(svcHeader string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == Path && r.Header.Get(svcHeader) != "" {
			h.serve(w, r, r.Header.Get(svcHeader))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request, service string) {
	// ADR 0011: a Trusted Device's power. The gate already refuses a Node
	// key and the Recovery Phrase anything on the Service tunnel; the
	// endpoint does not rely on it.
	p, ok := peer.From(r.Context())
	switch {
	case !ok:
		http.Error(w, "a Terminal needs a Trusted Device's key", http.StatusUnauthorized)
		return
	case h.cfg.Roles.Node(p.Fingerprint):
		http.Error(w, "a Node may not open a Terminal (ADR 0011)", http.StatusForbidden)
		return
	case h.cfg.Roles.Limited(p.Fingerprint):
		http.Error(w, "the Recovery Phrase alone may not open a Terminal (ADR 0011)", http.StatusForbidden)
		return
	}
	var spec maitrev1.ServiceSpec
	if _, err := h.cfg.Store.Get(r.Context(), services.KindService, service, &spec); err != nil {
		http.Error(w, "no Service named "+service, http.StatusNotFound)
		return
	}
	// The Console's origin is its own (tauri:// or a loopback port) and the
	// request comes through its proxy; who may open one is settled above
	// by the mutual-TLS key, and on the Desktop by the one-time token.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	// The WebSocket outlives the request's context (hijacked).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.session(ctx, c, &spec, p.Fingerprint)
}

func (h *Handler) session(ctx context.Context, c *websocket.Conn, spec *maitrev1.ServiceSpec, device string) {
	send := func(f *maitrev1.TerminalFrame) error {
		b, _ := proto.Marshal(f)
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return c.Write(wctx, websocket.MessageBinary, b)
	}
	fail := func(reason maitrev1.TerminalExit_Reason, msg string) {
		send(&maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Exit{Exit: &maitrev1.TerminalExit{Reason: reason, Message: msg}}})
		c.Close(websocket.StatusNormalClosure, "")
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	f, err := readWS(rctx, c)
	cancel()
	if err != nil {
		return
	}
	var (
		held net.Conn
		info *maitrev1.TerminalInfo
	)
	switch {
	case f.GetStart() != nil:
		held, info, err = h.start(ctx, spec, f.GetStart(), device)
	case f.GetAttach() != nil:
		held, info, err = h.attach(ctx, spec.GetName(), f.GetAttach())
	default:
		err = failure{maitrev1.TerminalExit_REASON_FAILED, "a Terminal begins with its start"}
	}
	if err != nil {
		var fl failure
		if !errors.As(err, &fl) {
			fl = failure{maitrev1.TerminalExit_REASON_FAILED, err.Error()}
		}
		fail(fl.reason, fl.msg)
		return
	}
	defer held.Close()
	if err := send(&maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Started{Started: &maitrev1.TerminalStarted{
		Id: info.GetId(), Container: info.GetContainer(), User: info.GetUser(), Argv: info.GetArgv(), StartedUnixMs: info.GetStartedUnixMs()}}}); err != nil {
		return
	}
	// The Console's frames to the Terminal. The Console leaving only
	// detaches: the Terminal runs on in its holder (ADR 0011).
	go func() {
		defer held.Close()
		for {
			f, err := readWS(ctx, c)
			if err != nil {
				return
			}
			if f.GetInput() == nil && f.GetResize() == nil && f.GetEnd() == nil {
				continue
			}
			if err := WriteFrame(held, f); err != nil {
				return
			}
		}
	}()
	// The Terminal's frames to the Console, the exit last.
	for {
		f, err := ReadFrame(held)
		if err != nil {
			c.Close(websocket.StatusGoingAway, "the Terminal's session ended")
			return
		}
		if f.GetOutput() == nil && f.GetResize() == nil && f.GetExit() == nil {
			continue
		}
		if err := send(f); err != nil {
			return
		}
		if x := f.GetExit(); x != nil {
			// The Device has seen how it ended: the holder may let it go.
			WriteFrame(held, &maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_End{End: &maitrev1.TerminalEnd{}}})
			h.cfg.Log.Info("terminal ended", "id", info.GetId(), "code", x.GetCode())
			c.Close(websocket.StatusNormalClosure, "")
			return
		}
	}
}

// failure is a Terminal that could not be opened, as its exit frame says.
type failure struct {
	reason maitrev1.TerminalExit_Reason
	msg    string
}

func (f failure) Error() string { return f.msg }

// start launches a new Terminal: the Service's shell in its container, in a
// holder of its own. It returns the view's connection to it.
func (h *Handler) start(ctx context.Context, spec *maitrev1.ServiceSpec, st *maitrev1.TerminalStart, device string) (net.Conn, *maitrev1.TerminalInfo, error) {
	if st.GetService() != spec.GetName() {
		return nil, nil, fmt.Errorf("this connection is for %s, not %s", spec.GetName(), st.GetService())
	}
	ctr, comp, label, err := h.container(ctx, spec, st.GetComponent())
	if err != nil {
		return nil, nil, err
	}
	h.starting.Lock()
	defer h.starting.Unlock()
	if n := h.running(ctx, spec.GetName(), label); n >= MaxPerContainer {
		return nil, nil, fmt.Errorf("%s already has %d Terminals running, the most a container may hold. End one to open another.", label, n)
	}
	user := "root"
	if !st.GetRoot() {
		user = h.user(ctx, ctr)
	}
	shell := h.shell(ctx, ctr, shellsFor(spec, comp))
	if shell == nil {
		return nil, nil, failure{maitrev1.TerminalExit_REASON_NO_SHELL, NoShell}
	}
	id := newID()
	argv := []string{h.cfg.Podman, "exec", "-it", "-e", "TERM=xterm-256color"}
	if st.GetRoot() {
		argv = append(argv, "--user", "0")
	}
	argv = append(append(argv, ctr), shell...)
	if err := os.MkdirAll(h.cfg.Dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("the Terminal could not start: %w", err)
	}
	hold := HoldSpec{Argv: argv, Size: st.GetSize(), Info: &maitrev1.TerminalInfo{Id: id, Service: spec.GetName(), Container: label, User: user, Argv: shell}}
	if err := h.cfg.Launcher.Launch(ctx, h.socket(id), hold); err != nil {
		return nil, nil, fmt.Errorf("the Terminal could not start: %w", err)
	}
	held, info, err := h.dial(ctx, id, &maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Attach{Attach: &maitrev1.TerminalAttach{Id: id}}}, 10*time.Second)
	if err != nil {
		return nil, nil, fmt.Errorf("the Terminal could not start: %w", err)
	}
	h.cfg.Log.Info("terminal opened", "id", id, "device", device, "service", spec.GetName(), "container", ctr, "user", user, "argv", strings.Join(shell, " "))
	return held, info, nil
}

// attach connects a view to a Terminal the Node holds, the Service's own.
func (h *Handler) attach(ctx context.Context, service string, a *maitrev1.TerminalAttach) (net.Conn, *maitrev1.TerminalInfo, error) {
	gone := errors.New("That Terminal is no longer on the Node.")
	if !validID(a.GetId()) {
		return nil, nil, gone
	}
	held, info, err := h.dial(ctx, a.GetId(), &maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Attach{Attach: a}}, 0)
	if err != nil {
		return nil, nil, gone
	}
	if info.GetService() != service {
		held.Close()
		return nil, nil, gone
	}
	return held, info, nil
}

// dial connects to Terminal id's holder, says what it wants (first), and
// reads its info. wait is how long to wait for a holder just launched.
func (h *Handler) dial(ctx context.Context, id string, first *maitrev1.TerminalFrame, wait time.Duration) (net.Conn, *maitrev1.TerminalInfo, error) {
	deadline := time.Now().Add(wait)
	var c net.Conn
	for {
		var err error
		c, err = (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", h.socket(id))
		if err == nil {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if err := WriteFrame(c, first); err != nil {
		c.Close()
		return nil, nil, err
	}
	f, err := ReadFrame(c)
	if err != nil || f.GetInfo() == nil {
		c.Close()
		return nil, nil, fmt.Errorf("Terminal %s did not say what it is: %v", id, err)
	}
	c.SetDeadline(time.Time{})
	return c, f.GetInfo(), nil
}

func (h *Handler) socket(id string) string { return filepath.Join(h.cfg.Dir, id+".sock") }

var idRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

func validID(id string) bool { return idRe.MatchString(id) }

// ListTerminals implements TerminalService: every holder's info, asked of
// it, so an agent that restarted finds the Terminals still there. Without a
// Service, every Terminal on the Node (#9).
func (h *Handler) ListTerminals(ctx context.Context, req *maitrev1.ListTerminalsRequest) (*maitrev1.ListTerminalsResponse, error) {
	return &maitrev1.ListTerminalsResponse{Terminals: h.held(ctx, req.GetService())}, nil
}

// running is how many of the Service's Terminals run in its container
// named label (as TerminalInfo.container says it).
func (h *Handler) running(ctx context.Context, service, label string) int {
	n := 0
	for _, t := range h.held(ctx, service) {
		if t.GetRunning() && t.GetContainer() == label {
			n++
		}
	}
	return n
}

// held is the Service's Terminals, or with service "" the Node's, oldest
// first.
func (h *Handler) held(ctx context.Context, service string) []*maitrev1.TerminalInfo {
	socks, _ := filepath.Glob(filepath.Join(h.cfg.Dir, "*.sock"))
	res := &maitrev1.ListTerminalsResponse{}
	for _, s := range socks {
		id := strings.TrimSuffix(filepath.Base(s), ".sock")
		if !validID(id) {
			continue
		}
		c, info, err := h.dial(ctx, id, &maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Info{Info: &maitrev1.TerminalInfo{}}}, 0)
		if err != nil {
			// A holder that is gone without cleaning up after itself.
			if errors.Is(err, syscall.ECONNREFUSED) {
				os.Remove(s)
			}
			continue
		}
		c.Close()
		if service == "" || info.GetService() == service {
			res.Terminals = append(res.Terminals, info)
		}
	}
	sort.Slice(res.Terminals, func(i, j int) bool {
		return res.Terminals[i].GetStartedUnixMs() < res.Terminals[j].GetStartedUnixMs()
	})
	return res.Terminals
}

// EndTerminal implements TerminalService: End a running Terminal, or let a
// finished one go.
func (h *Handler) EndTerminal(ctx context.Context, req *maitrev1.EndTerminalRequest) (*maitrev1.EndTerminalResponse, error) {
	notFound := connect.NewError(connect.CodeNotFound, "that Terminal is no longer on the Node")
	if !validID(req.GetId()) {
		return nil, notFound
	}
	c, info, err := h.dial(ctx, req.GetId(), &maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Info{Info: &maitrev1.TerminalInfo{}}}, 0)
	if err != nil {
		return nil, notFound
	}
	c.Close()
	if info.GetService() != req.GetService() {
		return nil, notFound
	}
	c, err = (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", h.socket(req.GetId()))
	if err != nil {
		return nil, notFound
	}
	defer c.Close()
	if err := WriteFrame(c, &maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_End{End: &maitrev1.TerminalEnd{}}}); err != nil {
		return nil, notFound
	}
	h.cfg.Log.Info("terminal end", "id", req.GetId(), "service", req.GetService())
	return &maitrev1.EndTerminalResponse{}, nil
}

func readWS(ctx context.Context, c *websocket.Conn) (*maitrev1.TerminalFrame, error) {
	for {
		typ, b, err := c.Read(ctx)
		if err != nil {
			return nil, err
		}
		if typ != websocket.MessageBinary {
			continue
		}
		var f maitrev1.TerminalFrame
		if err := proto.Unmarshal(b, &f); err != nil {
			return nil, err
		}
		return &f, nil
	}
}

// container is the running container of the Service's component, found by
// the unit Quadlet labels it with, the component ("" in a Service of one
// container) and the name the Console shows for it.
func (h *Handler) container(ctx context.Context, spec *maitrev1.ServiceSpec, component string) (name, comp, label string, err error) {
	unit, label := spec.GetName()+".service", spec.GetName()
	if comps := spec.GetComponents(); len(comps) > 0 {
		if component == "" {
			component = comps[0].GetName()
		}
		found := false
		for _, c := range comps {
			found = found || c.GetName() == component
		}
		if !found {
			return "", "", "", fmt.Errorf("%s has no container named %s", spec.GetName(), component)
		}
		comp = component
		unit, label = spec.GetName()+"-"+component+".service", component
	}
	out, err := h.cfg.Run(ctx, h.cfg.Podman, "ps", "--filter", "label=PODMAN_SYSTEMD_UNIT="+unit, "--format", "{{.Names}}")
	if err != nil {
		return "", "", "", fmt.Errorf("could not ask podman for %s's container: %w", spec.GetName(), err)
	}
	name = strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
	if name == "" {
		return "", "", "", fmt.Errorf("%s is not running", spec.GetName())
	}
	return name, comp, label, nil
}

// user is the image's default user, as the image names it; "root" when it
// names none.
func (h *Handler) user(ctx context.Context, ctr string) string {
	out, err := h.cfg.Run(ctx, h.cfg.Podman, "inspect", "--format", "{{.Config.User}}", ctr)
	if u := strings.TrimSpace(out); err == nil && u != "" {
		return u
	}
	return "root"
}

// shellsFor are the shells to try in a component's container ("" in a
// Service of one container), in order (ADR 0011): the Catalog entry's
// `shell:`, the one set in Service Configuration, then Shells.
func shellsFor(spec *maitrev1.ServiceSpec, comp string) [][]string {
	var out [][]string
	declared := spec.GetShell()
	for _, c := range spec.GetComponents() {
		if comp != "" && c.GetName() == comp {
			declared = c.GetShell()
		}
	}
	if len(declared) > 0 {
		out = append(out, declared)
	}
	for _, s := range spec.GetShells() {
		if s.GetComponent() == comp && len(s.GetArgv()) > 0 {
			out = append(out, s.GetArgv())
		}
	}
	for _, s := range Shells {
		out = append(out, []string{s})
	}
	return out
}

// shell is the first of shells whose program the container can run, or
// nil.
func (h *Handler) shell(ctx context.Context, ctr string, shells [][]string) []string {
	for _, s := range shells {
		if _, err := h.cfg.Run(ctx, h.cfg.Podman, "exec", ctr, s[0], "-c", "exit 0"); err == nil {
			return s
		}
	}
	return nil
}

func newID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
