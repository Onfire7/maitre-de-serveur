package owner

import (
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// As runs fn with the file system identity of a Space's owner (decision
// 189): its uid and gid, and no supplementary groups, for every file
// operation fn makes on its own goroutine. The agent runs as root; the
// Files jobs it does for a person act only with the rights the Space's
// owner has, so a symbolic link a container planted in a shared folder
// cannot lead them outside it, and a peer's NFS server sees the owner and
// not root. id 0, or an agent that is not root (tests, dev), runs fn as
// it is.
//
// The credentials are per thread: fn runs on a goroutine locked to a
// thread of its own, which exits with it instead of going back to the
// pool. Child processes fn starts inherit them; set SysProcAttr.Credential
// on those anyway (Credential) to be explicit.
func As(id uint32, fn func() error) error {
	if id == 0 || os.Geteuid() != 0 {
		return fn()
	}
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		// No UnlockOSThread: the thread is discarded when this returns.
		if err := become(id); err != nil {
			done <- err
			return
		}
		done <- fn()
	}()
	return <-done
}

func become(id uint32) error {
	// Raw syscalls: they change this thread only, where the libc and Go
	// wrappers change every thread of the process.
	if _, _, e := unix.RawSyscall(unix.SYS_SETGROUPS, 0, 0, 0); e != 0 {
		return fmt.Errorf("setgroups: %w", e)
	}
	unix.RawSyscall(unix.SYS_SETFSGID, uintptr(id), 0, 0)
	unix.RawSyscall(unix.SYS_SETFSUID, uintptr(id), 0, 0)
	// setfsuid and setfsgid report the previous value, never an error;
	// asking with an invalid id returns the current one without a change.
	cur, _, _ := unix.RawSyscall(unix.SYS_SETFSUID, ^uintptr(0), 0, 0)
	curg, _, _ := unix.RawSyscall(unix.SYS_SETFSGID, ^uintptr(0), 0, 0)
	if uint32(cur) != id || uint32(curg) != id {
		return fmt.Errorf("could not act as owner %d", id)
	}
	return nil
}
