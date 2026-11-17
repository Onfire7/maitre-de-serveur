package terminal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"
	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// maxFrame bounds one frame between the agent and a session.
const maxFrame = 1 << 20

// WriteFrame writes one frame to a session's socket: its length, four bytes
// big-endian, then the TerminalFrame.
func WriteFrame(w io.Writer, f *maitrev1.TerminalFrame) error {
	b, err := proto.Marshal(f)
	if err != nil {
		return err
	}
	buf := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(buf, uint32(len(b)))
	copy(buf[4:], b)
	_, err = w.Write(buf)
	return err
}

// ReadFrame reads one frame WriteFrame wrote.
func ReadFrame(r io.Reader) (*maitrev1.TerminalFrame, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(n[:])
	if size > maxFrame {
		return nil, fmt.Errorf("a frame of %d bytes", size)
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	var f maitrev1.TerminalFrame
	return &f, proto.Unmarshal(b, &f)
}

// hangupGrace is how long a hung-up process has before it is killed.
const hangupGrace = 5 * time.Second

// acceptWait is how long a session waits for the agent to connect.
const acceptWait = 30 * time.Second

// Hold runs argv on a new PTY of the given size and serves the session on
// ln: the agent's input and resize frames go to the PTY, its output comes
// back as output frames, and the exit frame is last. It is what the
// transient unit runs (`maitre terminal-hold`), so the PTY is held outside
// the agent.
//
// For now the session has one connection: when it closes before the
// process ends, the process is hung up (SIGHUP, then SIGKILL after 5 s).
func Hold(ln net.Listener, argv []string, size *maitrev1.TerminalSize) error {
	defer ln.Close()
	if len(argv) == 0 {
		return errors.New("no command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, winsize(size))
	if err != nil {
		return err
	}
	defer ptmx.Close()
	exited := make(chan struct{})
	hangup := func() {
		cmd.Process.Signal(syscall.SIGHUP)
		select {
		case <-exited:
		case <-time.After(hangupGrace):
			cmd.Process.Kill()
		}
	}
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetDeadline(time.Now().Add(acceptWait))
	}
	conn, err := ln.Accept()
	if err != nil {
		go hangup()
		cmd.Wait()
		close(exited)
		return fmt.Errorf("no one connected: %w", err)
	}
	defer conn.Close()
	// The agent's frames to the PTY; its leaving hangs the process up.
	go func() {
		for {
			f, err := ReadFrame(conn)
			if err != nil {
				hangup()
				return
			}
			switch {
			case f.GetInput() != nil:
				ptmx.Write(f.GetInput().GetData())
			case f.GetResize() != nil:
				pty.Setsize(ptmx, winsize(f.GetResize().GetSize()))
			}
		}
	}()
	// The PTY's output to the agent, until the terminal closes.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		buf := make([]byte, 32<<10)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if WriteFrame(conn, &maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Output{Output: &maitrev1.TerminalOutput{Data: append([]byte(nil), buf[:n]...)}}}) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	werr := cmd.Wait()
	close(exited)
	// What the process wrote last is still on its way; a process it left
	// behind holding the terminal does not keep the session open.
	select {
	case <-drained:
	case <-time.After(time.Second):
		ptmx.Close()
		<-drained
	}
	return WriteFrame(conn, &maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Exit{Exit: &maitrev1.TerminalExit{Reason: maitrev1.TerminalExit_REASON_EXITED, Code: exitCode(cmd, werr)}}})
}

// exitCode is the process's exit code, 128+n when signal n ended it.
func exitCode(cmd *exec.Cmd, err error) int32 {
	if cmd.ProcessState == nil {
		return -1
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int32(ws.Signal())
	}
	return int32(cmd.ProcessState.ExitCode())
}

func winsize(s *maitrev1.TerminalSize) *pty.Winsize {
	cols, rows := s.GetCols(), s.GetRows()
	if cols == 0 || cols > 1000 {
		cols = 80
	}
	if rows == 0 || rows > 1000 {
		rows = 24
	}
	return &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}
}
