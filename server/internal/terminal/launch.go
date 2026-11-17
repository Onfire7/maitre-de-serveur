package terminal

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// A Launcher starts a session (Hold) for argv apart from the agent and
// returns the unix socket it serves.
type Launcher interface {
	Launch(ctx context.Context, id string, argv []string, size *maitrev1.TerminalSize) (socket string, err error)
}

// HoldArgs are the arguments of `maitre terminal-hold` for a session.
func HoldArgs(socket string, argv []string, size *maitrev1.TerminalSize) []string {
	w := winsize(size)
	return append([]string{"terminal-hold", "--socket", socket, "--cols", strconv.Itoa(int(w.Cols)), "--rows", strconv.Itoa(int(w.Rows)), "--"}, argv...)
}

// Systemd starts each session in a transient systemd unit of its own,
// maitre-terminal-<id>.service, running `<Self> terminal-hold`, so the PTY
// is held by the unit and the agent can restart without ending it (ADR 0011).
type Systemd struct {
	// Self is the maitre binary.
	Self string
	// Dir holds the sessions' sockets (/run/maitre/terminals).
	Dir string
}

// Launch implements Launcher.
func (s Systemd) Launch(ctx context.Context, id string, argv []string, size *maitrev1.TerminalSize) (string, error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", err
	}
	sock := filepath.Join(s.Dir, id+".sock")
	args := append([]string{"--unit=maitre-terminal-" + id, "--collect", "--quiet", "--service-type=exec",
		"--description=Maître Terminal " + id, "--property=KillMode=mixed", "--"}, append([]string{s.Self}, HoldArgs(sock, argv, size)...)...)
	if out, err := exec.CommandContext(ctx, "systemd-run", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("systemd-run: %w: %s", err, out)
	}
	return sock, nil
}

// Process starts each session as a process of its own in a new session
// group, running `<Self> terminal-hold`: for a dev Node, which has no
// systemd to ask.
type Process struct {
	Self string
	Dir  string
}

// Launch implements Launcher.
func (p Process) Launch(ctx context.Context, id string, argv []string, size *maitrev1.TerminalSize) (string, error) {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return "", err
	}
	sock := filepath.Join(p.Dir, id+".sock")
	cmd := exec.Command(p.Self, HoldArgs(sock, argv, size)...)
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = newSession()
	if err := cmd.Start(); err != nil {
		return "", err
	}
	go cmd.Wait()
	return sock, nil
}

// InProcess runs each session in a goroutine of this process, its socket in
// dir: for tests.
func InProcess(dir string) Launcher { return inProcess(dir) }

type inProcess string

func (d inProcess) Launch(ctx context.Context, id string, argv []string, size *maitrev1.TerminalSize) (string, error) {
	sock := filepath.Join(string(d), id+".sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return "", err
	}
	go Hold(ln, argv, size)
	return sock, nil
}

// Serve is `maitre terminal-hold`: listen on socket and Hold argv there.
func Serve(socket string, argv []string, size *maitrev1.TerminalSize) error {
	os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer os.Remove(socket)
	return Hold(ln, argv, size)
}
