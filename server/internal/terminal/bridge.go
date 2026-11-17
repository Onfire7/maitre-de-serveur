package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// Ticket is what the Desktop hands `maitre terminal-bridge` (Open in your
// terminal): the loopback proxy's Terminal URL with a one-time token, and
// the Node-held Terminal to attach to. It is written to a file only the
// person can read, which the bridge removes as it reads it, so neither the
// token nor the file outlives one use.
type Ticket struct {
	URL      string `json:"url"`
	Terminal string `json:"terminal"`
}

// BridgeIO is the person's own terminal app: what they type, where output
// goes, its size, and a signal each time that size changes (nil: never).
type BridgeIO struct {
	In      io.Reader
	Out     io.Writer
	Size    func() (cols, rows uint32)
	Resized <-chan struct{}
}

// ErrNoTicket: the bridge was started without a ticket, or with one
// already used.
var ErrNoTicket = errors.New("this opens a Terminal only from Maître Desktop's Open in your terminal; there is no ticket for it")

// Bridge attaches io to the Terminal the ticket names, through the
// Desktop's loopback proxy, until the Terminal exits (its code is
// returned), ctx ends (detached: the Terminal runs on), or the connection
// drops (an error).
func Bridge(ctx context.Context, ticketPath string, tio BridgeIO) (int, error) {
	tk, err := readTicket(ticketPath)
	if err != nil {
		return 0, err
	}
	c, _, err := websocket.Dial(ctx, tk.URL, nil)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil
		}
		return 0, fmt.Errorf("could not reach Maître Desktop: %w", err)
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)

	size := func() *maitrev1.TerminalSize {
		cols, rows := uint32(80), uint32(24)
		if tio.Size != nil {
			cols, rows = tio.Size()
		}
		return &maitrev1.TerminalSize{Cols: cols, Rows: rows}
	}
	send := func(f *maitrev1.TerminalFrame) error {
		b, err := proto.Marshal(f)
		if err != nil {
			return err
		}
		return c.Write(ctx, websocket.MessageBinary, b)
	}
	if err := send(&maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Attach{Attach: &maitrev1.TerminalAttach{Id: tk.Terminal, Size: size()}}}); err != nil {
		return 0, lost(ctx, err)
	}

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := tio.In.Read(buf)
			if n > 0 {
				data := append([]byte(nil), buf[:n]...)
				if send(&maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Input{Input: &maitrev1.TerminalInput{Data: data}}}) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	if tio.Resized != nil {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-tio.Resized:
					if send(&maitrev1.TerminalFrame{Frame: &maitrev1.TerminalFrame_Resize{Resize: &maitrev1.TerminalResize{Size: size()}}}) != nil {
						return
					}
				}
			}
		}()
	}

	for {
		typ, b, err := c.Read(ctx)
		if err != nil {
			return 0, lost(ctx, err)
		}
		if typ != websocket.MessageBinary {
			continue
		}
		var f maitrev1.TerminalFrame
		if proto.Unmarshal(b, &f) != nil {
			continue
		}
		switch {
		case f.GetOutput() != nil:
			tio.Out.Write(f.GetOutput().GetData())
		case f.GetExit() != nil:
			x := f.GetExit()
			c.Close(websocket.StatusNormalClosure, "")
			if x.GetReason() == maitrev1.TerminalExit_REASON_EXITED {
				return int(x.GetCode()), nil
			}
			msg := x.GetMessage()
			if msg == "" {
				msg = "The Terminal could not start."
			}
			return 0, errors.New(msg)
		}
	}
}

// lost is nil when ctx ended (the person closed their terminal: detach),
// else says the connection dropped.
func lost(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	return fmt.Errorf("the connection to the Node was lost (%v); the Terminal still runs there, open it again from Maître", err)
}

// readTicket reads the ticket and removes it, so it serves once.
func readTicket(path string) (Ticket, error) {
	var tk Ticket
	if path == "" {
		return tk, ErrNoTicket
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return tk, ErrNoTicket
	}
	os.Remove(path)
	if json.Unmarshal(b, &tk) != nil || tk.Terminal == "" || !(strings.HasPrefix(tk.URL, "ws://") || strings.HasPrefix(tk.URL, "wss://")) {
		return Ticket{}, ErrNoTicket
	}
	return tk, nil
}
