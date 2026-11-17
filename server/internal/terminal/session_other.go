//go:build !unix

package terminal

import "syscall"

func newSession() *syscall.SysProcAttr { return nil }
