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
// For now (the tracer bullet, #4) a Terminal lives as long as its one
// connection: closing the view hangs the shell up.
package terminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

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
	// Run runs a command for its output; nil runs it for real.
	Run func(ctx context.Context, name string, args ...string) (string, error)
	Log *slog.Logger
}

// Handler is the Terminal endpoint.
type Handler struct{ cfg Config }

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
	st := f.GetStart()
	if st == nil {
		fail(maitrev1.TerminalExit_REASON_FAILED, "a Terminal begins with its start")
		return
	}
	if st.GetService() != spec.GetName() {
		fail(maitrev1.TerminalExit_REASON_FAILED, fmt.Sprintf("this connection is for %s, not %s", spec.GetName(), st.GetService()))
		return
	}
	ctr, comp, label, err := h.container(ctx, spec, st.GetComponent())
	if err != nil {
		fail(maitrev1.TerminalExit_REASON_FAILED, err.Error())
		return
	}
	user := "root"
	if !st.GetRoot() {
		user = h.user(ctx, ctr)
	}
	shell := h.shell(ctx, ctr, shellsFor(spec, comp))
	if shell == nil {
		fail(maitrev1.TerminalExit_REASON_NO_SHELL, NoShell)
		return
	}
	id := newID()
	argv := []string{h.cfg.Podman, "exec", "-it", "-e", "TERM=xterm-256color"}
	if st.GetRoot() {
		argv = append(argv, "--user", "0")
	}
	argv = append(append(argv, ctr), shell...)
	sock, err := h.cfg.Launcher.Launch(ctx, id, argv, st.GetSize())
	if err != nil {
		fail(maitrev1.TerminalExit_REASON_FAILED, "the Terminal could not start: "+err.Error())
		return
	}
	held, err := dialHolder(ctx, sock)
	if err != nil {
		fail(maitrev1.TerminalExit_REASON_FAILED, "the Terminal could not start: "+err.Error())
		return
	}
	defer held.Close()
	h.cfg.Log.Info("terminal opened", "id", id, "device", device, "service", spec.GetName(), "container", ctr, "user", user, "argv", strings.Join(shell, " "))
	if err := send(&maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Started{Started: &maitrev1.TerminalStarted{Id: id, Container: label, User: user, Argv: shell}}}); err != nil {
		return
	}
	// The Console's frames to the session.
	go func() {
		defer held.Close()
		for {
			f, err := readWS(ctx, c)
			if err != nil {
				return
			}
			if f.GetInput() == nil && f.GetResize() == nil {
				continue
			}
			if err := WriteFrame(held, f); err != nil {
				return
			}
		}
	}()
	// The session's frames to the Console, the exit last.
	for {
		f, err := ReadFrame(held)
		if err != nil {
			c.Close(websocket.StatusGoingAway, "the Terminal's session ended")
			return
		}
		if err := send(f); err != nil {
			return
		}
		if x := f.GetExit(); x != nil {
			h.cfg.Log.Info("terminal ended", "id", id, "code", x.GetCode())
			c.Close(websocket.StatusNormalClosure, "")
			return
		}
	}
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

// dialHolder connects to the socket a launched session serves, waiting a
// few seconds for it to appear.
func dialHolder(ctx context.Context, sock string) (net.Conn, error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := (&net.Dialer{}).DialContext(ctx, "unix", sock)
		if err == nil {
			return c, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}
