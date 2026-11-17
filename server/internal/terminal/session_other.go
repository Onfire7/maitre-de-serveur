//go:build !unix

package terminal

import (
	"os"
	"syscall"
)

func newSession() *syscall.SysProcAttr { return nil }

func signalGroup(p *os.Process, sig syscall.Signal) { p.Signal(sig) }
