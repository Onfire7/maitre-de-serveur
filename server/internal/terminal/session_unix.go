//go:build unix

package terminal

import (
	"os"
	"syscall"
)

// newSession starts a process in a session of its own, so it outlives the
// dev Node's terminal and process group.
func newSession() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// signalGroup signals the process's group (a PTY's session leader leads
// one), else the process.
func signalGroup(p *os.Process, sig syscall.Signal) {
	if syscall.Kill(-p.Pid, sig) != nil {
		p.Signal(sig)
	}
}
