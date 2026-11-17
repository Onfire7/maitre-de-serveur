//go:build unix

package terminal

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

var hangup os.Signal = syscall.SIGHUP

// resized signals each SIGWINCH: the terminal app's window changed size.
func resized(ctx context.Context) <-chan struct{} {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	out := make(chan struct{}, 1)
	go func() {
		defer signal.Stop(sig)
		for {
			select {
			case <-ctx.Done():
				return
			case <-sig:
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
	return out
}
