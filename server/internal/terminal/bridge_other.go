//go:build !unix

package terminal

import (
	"context"
	"os"
	"time"

	"golang.org/x/term"
)

// hangup: Windows has no SIGHUP; closing the console ends the process.
var hangup os.Signal = os.Interrupt

// resized polls the console's size: Windows sends no signal when it changes.
func resized(ctx context.Context) <-chan struct{} {
	out := make(chan struct{}, 1)
	go func() {
		w, h, _ := term.GetSize(int(os.Stdout.Fd()))
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if nw, nh, err := term.GetSize(int(os.Stdout.Fd())); err == nil && (nw != w || nh != h) {
					w, h = nw, nh
					select {
					case out <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	return out
}
