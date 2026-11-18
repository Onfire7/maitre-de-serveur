package terminal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
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

// hangupGrace is how long a process that was hung up has before it is
// killed.
var hangupGrace = 5 * time.Second

// keepFinished is how long a finished Terminal is kept, with its exit code
// and output, for a Device that has not seen it yet (ADR 0011).
var keepFinished atomic.Int64

func init() { keepFinished.Store(int64(24 * time.Hour)) }

// replayBytes is how much recent output a Terminal keeps to replay.
const replayBytes = 1 << 20

// chunk bounds the output in one frame.
const chunk = 32 << 10

// firstFrameWait is how long a connection has to say what it wants.
const firstFrameWait = 30 * time.Second

// viewerQueue is how many frames a slow view may fall behind before it is
// dropped (it can attach again, and gets the replay).
const viewerQueue = 512

// HoldSpec is what a holder runs and what it says about it.
type HoldSpec struct {
	Argv []string
	Size *maitrev1.TerminalSize
	// Info is the Terminal's id, Service, container, user and argv as the
	// Console shows them; the holder fills in its times and state.
	Info *maitrev1.Terminal
}

// Hold runs argv on a new PTY and serves the Terminal on ln to any number
// of connections, each starting with what it wants (see TerminalFrame): a
// view gets the info, the session's size, about 1 MiB of recent output
// replayed, then output and new sizes as they come and the exit last; its
// input goes to the PTY. Views share the session: the one that last typed
// or resized sets its size, and every view is told it. It is
// what the transient unit runs (`maitre terminal-hold`), so the PTY and the
// output, kept in memory only, outlive the agent and every connection.
//
// The process ends when it exits, or on End (SIGHUP, then SIGKILL after 5
// seconds). Hold then keeps the exit code and the output until a view has
// seen the exit, End is sent again, or 24 hours pass, and returns.
func Hold(ln net.Listener, spec HoldSpec) error {
	defer ln.Close()
	if len(spec.Argv) == 0 {
		return errors.New("no command")
	}
	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, winsize(spec.Size))
	if err != nil {
		return err
	}
	defer ptmx.Close()
	info := proto.Clone(spec.Info).(*maitrev1.Terminal)
	if info == nil {
		info = &maitrev1.Terminal{}
	}
	info.Running = true
	info.StartedUnixMs = time.Now().UnixMilli()
	ws := winsize(spec.Size)
	h := &holder{cmd: cmd, ptmx: ptmx, info: info, size: &maitrev1.TerminalSize{Cols: uint32(ws.Cols), Rows: uint32(ws.Rows)},
		viewers: map[*viewer]bool{}, exited: make(chan struct{}), gone: make(chan struct{})}
	go h.accept(ln)

	// The PTY's output to the replay and every view, until it closes.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		buf := make([]byte, chunk)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				h.output(append([]byte(nil), buf[:n]...))
			}
			if err != nil {
				return
			}
		}
	}()
	werr := cmd.Wait()
	close(h.exited)
	// What the process wrote last is still on its way; a process it left
	// behind holding the terminal does not keep the Terminal running.
	select {
	case <-drained:
	case <-time.After(time.Second):
		ptmx.Close()
		<-drained
	}
	h.finish(exitCode(cmd, werr))
	select {
	case <-h.gone:
	case <-time.After(time.Duration(keepFinished.Load())):
	}
	return nil
}

type holder struct {
	cmd    *exec.Cmd
	ptmx   *os.File
	exited chan struct{}

	mu     sync.Mutex
	info   *maitrev1.Terminal
	replay []byte
	// size is the session's: the size of the view that last typed or
	// resized (ADR 0011), which every view is told.
	size     *maitrev1.TerminalSize
	viewers  map[*viewer]bool
	hanging  bool
	gone     chan struct{}
	goneOnce sync.Once
}

type viewer struct {
	conn net.Conn
	out  chan *maitrev1.TerminalFrame
	// size is the view's own size, as it last said; h.mu guards it.
	size *maitrev1.TerminalSize
}

func outputFrame(b []byte) *maitrev1.TerminalFrame {
	return &maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Output{Output: &maitrev1.TerminalOutput{Data: b}}}
}

func (h *holder) output(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.replay = append(h.replay, b...)
	if over := len(h.replay) - replayBytes; over > 0 {
		h.replay = append(h.replay[:0:0], h.replay[over:]...)
	}
	h.broadcast(outputFrame(b))
}

// broadcast queues f for every view, dropping one that has fallen too far
// behind. h.mu is held.
func (h *holder) broadcast(f *maitrev1.TerminalFrame) {
	for v := range h.viewers {
		select {
		case v.out <- f:
		default:
			delete(h.viewers, v)
			close(v.out)
		}
	}
}

func sizeFrame(s *maitrev1.TerminalSize) *maitrev1.TerminalFrame {
	return &maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Resize{Resize: &maitrev1.TerminalResize{Size: proto.Clone(s).(*maitrev1.TerminalSize)}}}
}

// sizeTo makes v's size the session's, as when v types or resizes: the PTY
// takes it and every view is told. h.mu is held.
func (h *holder) sizeTo(v *viewer) {
	if v.size == nil || !h.info.GetRunning() {
		return
	}
	ws := winsize(v.size)
	if uint32(ws.Cols) == h.size.GetCols() && uint32(ws.Rows) == h.size.GetRows() {
		return
	}
	h.size = &maitrev1.TerminalSize{Cols: uint32(ws.Cols), Rows: uint32(ws.Rows)}
	pty.Setsize(h.ptmx, ws)
	h.broadcast(sizeFrame(h.size))
}

func (h *holder) finish(code int32) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.info.Running = false
	h.info.Code = code
	h.info.EndedUnixMs = time.Now().UnixMilli()
	h.broadcast(&maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Exit{Exit: &maitrev1.TerminalExit{Reason: maitrev1.TerminalExit_REASON_EXITED, Code: code}}})
	// The exit is every view's last frame.
	for v := range h.viewers {
		delete(h.viewers, v)
		close(v.out)
	}
}

// end is End: a running process is hung up, then killed after the grace;
// a finished Terminal is let go.
func (h *holder) end() {
	h.mu.Lock()
	running, hanging := h.info.GetRunning(), h.hanging
	h.hanging = true
	h.mu.Unlock()
	if !running {
		h.goneOnce.Do(func() { close(h.gone) })
		return
	}
	if hanging {
		return
	}
	go func() {
		signalGroup(h.cmd.Process, syscall.SIGHUP)
		select {
		case <-h.exited:
		case <-time.After(hangupGrace):
			signalGroup(h.cmd.Process, syscall.SIGKILL)
		}
	}()
}

func (h *holder) accept(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go h.serve(c)
	}
}

func (h *holder) serve(c net.Conn) {
	c.SetReadDeadline(time.Now().Add(firstFrameWait))
	f, err := ReadFrame(c)
	if err != nil {
		c.Close()
		return
	}
	c.SetReadDeadline(time.Time{})
	switch {
	case f.GetAttach() != nil:
		h.attach(c, f.GetAttach())
	case f.GetEnd() != nil:
		h.end()
		c.Close()
	default:
		WriteFrame(c, h.infoFrame())
		c.Close()
	}
}

func (h *holder) infoFrame() *maitrev1.TerminalFrame {
	h.mu.Lock()
	defer h.mu.Unlock()
	return &maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Info{Info: proto.Clone(h.info).(*maitrev1.Terminal)}}
}

// attach serves a view: the info, the replay, then what comes; its input
// and resizes go to the PTY, and an End after the exit means it was seen.
func (h *holder) attach(c net.Conn, a *maitrev1.TerminalAttach) {
	defer c.Close()
	v := &viewer{conn: c, out: make(chan *maitrev1.TerminalFrame, viewerQueue), size: a.GetSize()}
	// The info, the size and the replay are taken, and the view added, at
	// one moment, so no output is missed or sent twice. A view joining
	// others shows the session at their size; one alone gives it its own.
	h.mu.Lock()
	if len(h.viewers) == 0 {
		h.sizeTo(v)
	}
	first := []*maitrev1.TerminalFrame{{Frame: &maitrev1.TerminalFrame_Info{Info: proto.Clone(h.info).(*maitrev1.Terminal)}}, sizeFrame(h.size)}
	for b := h.replay; len(b) > 0; {
		n := min(len(b), chunk)
		first = append(first, outputFrame(append([]byte(nil), b[:n]...)))
		b = b[n:]
	}
	if h.info.GetRunning() {
		h.viewers[v] = true
	} else {
		first = append(first, &maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Exit{Exit: &maitrev1.TerminalExit{Reason: maitrev1.TerminalExit_REASON_EXITED, Code: h.info.GetCode()}}})
		close(v.out)
	}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.viewers[v] {
			delete(h.viewers, v)
			close(v.out)
		}
		h.mu.Unlock()
	}()
	// The view's frames to the PTY.
	read := make(chan struct{})
	go func() {
		defer close(read)
		for {
			f, err := ReadFrame(c)
			if err != nil {
				// The view has gone: it no longer shares the session.
				c.Close()
				h.mu.Lock()
				if h.viewers[v] {
					delete(h.viewers, v)
					close(v.out)
				}
				h.mu.Unlock()
				return
			}
			// The view that types or resizes sets the session's size.
			switch {
			case f.GetInput() != nil:
				h.mu.Lock()
				h.sizeTo(v)
				h.mu.Unlock()
				h.ptmx.Write(f.GetInput().GetData())
			case f.GetResize() != nil:
				h.mu.Lock()
				v.size = f.GetResize().GetSize()
				h.sizeTo(v)
				h.mu.Unlock()
			case f.GetEnd() != nil:
				h.end()
			}
		}
	}()
	exit := false
	for _, f := range first {
		if WriteFrame(c, f) != nil {
			return
		}
		exit = f.GetExit() != nil
	}
	for f := range v.out {
		if WriteFrame(c, f) != nil {
			return
		}
		exit = f.GetExit() != nil
	}
	if exit {
		// Wait for the view to say it has seen the exit, or to leave.
		<-read
	}
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
