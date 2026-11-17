package terminal

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// BridgeMain is `maitre terminal-bridge --ticket <file>`, which Maître
// Desktop runs in the person's own terminal app (Open in your terminal).
// It returns the process's exit code.
func BridgeMain(args []string) int {
	fs := flag.NewFlagSet("terminal-bridge", flag.ContinueOnError)
	ticket := fs.String("ticket", "", "the ticket Maître Desktop wrote for this Terminal")
	if fs.Parse(args) != nil {
		return 2
	}
	in := int(os.Stdin.Fd())
	tty := term.IsTerminal(in)
	size := func() (uint32, uint32) {
		if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 && h > 0 {
			return uint32(w), uint32(h)
		}
		return 80, 24
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, hangup)
	defer stop()

	var restore func()
	if tty {
		if old, err := term.MakeRaw(in); err == nil {
			restore = func() { term.Restore(in, old) }
		}
	}
	code, err := Bridge(ctx, *ticket, BridgeIO{In: os.Stdin, Out: os.Stdout, Size: size, Resized: resized(ctx)})
	if restore != nil {
		restore()
	}
	switch {
	case errors.Is(err, ErrNoTicket):
		fmt.Fprintln(os.Stderr, err)
		return 2
	case ctx.Err() != nil:
		return 0
	case err != nil:
		fmt.Fprintf(os.Stderr, "\r\n%s\r\n", upper(err.Error()))
		code = 1
	default:
		fmt.Fprintf(os.Stderr, "\r\nThe Terminal exited with code %d.\r\n", code)
	}
	// Leave the window open long enough to read why it ended.
	if tty {
		fmt.Fprint(os.Stderr, "Press Enter to close this window.")
		os.Stdin.Read(make([]byte, 1))
	}
	return code
}

func upper(s string) string {
	if s != "" && s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-'a'+'A') + s[1:]
	}
	return s
}
