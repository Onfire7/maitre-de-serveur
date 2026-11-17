package terminal

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// A Launcher starts a holder (Hold) for a Terminal apart from the agent,
// serving it on socket.
type Launcher interface {
	Launch(ctx context.Context, socket string, spec HoldSpec) error
}

// HoldArgs are the arguments of `maitre terminal-hold` for a Terminal.
func HoldArgs(socket string, spec HoldSpec) []string {
	w := winsize(spec.Size)
	info, _ := proto.Marshal(spec.Info)
	return append([]string{"terminal-hold", "--socket", socket, "--cols", strconv.Itoa(int(w.Cols)), "--rows", strconv.Itoa(int(w.Rows)),
		"--info", base64.RawURLEncoding.EncodeToString(info), "--"}, spec.Argv...)
}

// HoldMain is `maitre terminal-hold` (args after the subcommand): it holds
// the PTY of `-- argv...` and serves the Terminal on --socket.
func HoldMain(args []string) error {
	fs := flag.NewFlagSet("terminal-hold", flag.ExitOnError)
	socket := fs.String("socket", "", "unix socket the agent connects to")
	cols := fs.Uint("cols", 80, "columns")
	rows := fs.Uint("rows", 24, "rows")
	info := fs.String("info", "", "the Terminal's TerminalInfo, protobuf in unpadded base64url")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *socket == "" || fs.NArg() == 0 {
		return fmt.Errorf("usage: maitre terminal-hold --socket path [--cols n --rows n --info b64] -- argv...")
	}
	spec := HoldSpec{Argv: fs.Args(), Size: &maitrev1.TerminalSize{Cols: uint32(*cols), Rows: uint32(*rows)}, Info: &maitrev1.TerminalInfo{}}
	if b, err := base64.RawURLEncoding.DecodeString(*info); err != nil || proto.Unmarshal(b, spec.Info) != nil {
		return fmt.Errorf("terminal-hold: --info is not a TerminalInfo")
	}
	return Serve(*socket, spec)
}

// Systemd starts each holder in a transient systemd unit of its own,
// maitre-terminal-<id>.service, running `<Self> terminal-hold`, so the PTY
// is held by the unit and the agent can restart without ending it (ADR 0011).
type Systemd struct {
	// Self is the maitre binary.
	Self string
}

// Launch implements Launcher.
func (s Systemd) Launch(ctx context.Context, socket string, spec HoldSpec) error {
	id := spec.Info.GetId()
	args := append([]string{"--unit=maitre-terminal-" + id, "--collect", "--quiet", "--service-type=exec",
		"--description=Maître Terminal " + id, "--property=KillMode=mixed", "--"}, append([]string{s.Self}, HoldArgs(socket, spec)...)...)
	if out, err := exec.CommandContext(ctx, "systemd-run", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("systemd-run: %w: %s", err, out)
	}
	return nil
}

// Process starts each holder as a process of its own in a new session,
// running `<Self> terminal-hold`: for a dev Node, which has no systemd to
// ask, and for tests of an agent restarting.
type Process struct {
	Self string
}

// Launch implements Launcher.
func (p Process) Launch(ctx context.Context, socket string, spec HoldSpec) error {
	cmd := exec.Command(p.Self, HoldArgs(socket, spec)...)
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = newSession()
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// InProcess runs each holder in a goroutine of this process: for tests.
func InProcess() Launcher { return inProcess{} }

type inProcess struct{}

func (inProcess) Launch(ctx context.Context, socket string, spec HoldSpec) error {
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	go func() {
		Hold(ln, spec)
		os.Remove(socket)
	}()
	return nil
}

// Serve is `maitre terminal-hold`: listen on socket and Hold there.
func Serve(socket string, spec HoldSpec) error {
	os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer os.Remove(socket)
	return Hold(ln, spec)
}
