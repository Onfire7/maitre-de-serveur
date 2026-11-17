//go:build unix

package terminal

import "syscall"

// newSession starts a process in a session of its own, so it outlives the
// dev Node's terminal and process group.
func newSession() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
