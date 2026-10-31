package copies

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Watcher follows Space folders with inotify, every folder under them,
// and reports files written or moved in (decision 260). Folders made
// later are followed as they appear. When the kernel's watch limit is
// reached the rest is left to the hourly pass.
type Watcher struct {
	m    *Manager
	fd   int
	mu   sync.Mutex
	dirs map[int]string
	wds  map[string]int
	full bool
}

const watchMask = unix.IN_CLOSE_WRITE | unix.IN_MOVED_TO | unix.IN_CREATE | unix.IN_DELETE_SELF | unix.IN_MOVE_SELF

// Watch follows the Manager's roots until ctx ends, re-reading them every
// ten minutes as Spaces and Drives change.
func (m *Manager) Watch(ctx context.Context) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		m.log.Warn("inotify unavailable; copies follow the hourly pass only", "err", err)
		return
	}
	w := &Watcher{m: m, fd: fd, dirs: map[int]string{}, wds: map[string]int{}}
	defer unix.Close(fd)
	w.follow(ctx)
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				w.follow(ctx)
			}
		}
	}()
	buf := make([]byte, 64<<10)
	for ctx.Err() == nil {
		n, err := unix.Read(fd, buf)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EINTR {
				select {
				case <-ctx.Done():
					return
				case <-time.After(200 * time.Millisecond):
				}
				continue
			}
			m.log.Warn("inotify read", "err", err)
			return
		}
		for off := 0; off+unix.SizeofInotifyEvent <= n; {
			ev := (*unix.InotifyEvent)(unsafe.Pointer(&buf[off]))
			name := ""
			if ev.Len > 0 {
				name = strings.TrimRight(string(buf[off+unix.SizeofInotifyEvent:off+unix.SizeofInotifyEvent+int(ev.Len)]), "\x00")
			}
			off += unix.SizeofInotifyEvent + int(ev.Len)
			w.mu.Lock()
			dir := w.dirs[int(ev.Wd)]
			w.mu.Unlock()
			if dir == "" {
				continue
			}
			switch {
			case ev.Mask&(unix.IN_DELETE_SELF|unix.IN_MOVE_SELF) != 0:
				w.forget(int(ev.Wd))
			case ev.Mask&unix.IN_ISDIR != 0 && ev.Mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0:
				w.add(filepath.Join(dir, name), true)
			case ev.Mask&(unix.IN_CLOSE_WRITE|unix.IN_MOVED_TO) != 0 && name != "" && !strings.HasPrefix(name, ".maitre"):
				m.Changed(filepath.Join(dir, name))
			}
		}
	}
}

// follow adds watches for every root and the folders under it.
func (w *Watcher) follow(ctx context.Context) {
	for _, root := range w.m.Roots(ctx) {
		w.add(root, true)
	}
}

// add watches dir and, when deep, every folder under it.
func (w *Watcher) add(dir string, deep bool) {
	w.mu.Lock()
	_, have := w.wds[dir]
	full := w.full
	w.mu.Unlock()
	if !have && !full {
		wd, err := unix.InotifyAddWatch(w.fd, dir, watchMask)
		if err != nil {
			if err == unix.ENOSPC {
				w.mu.Lock()
				w.full = true
				w.mu.Unlock()
				w.m.log.Warn("inotify watch limit reached; some folders follow the hourly pass only (raise fs.inotify.max_user_watches)")
			}
			return
		}
		w.mu.Lock()
		w.dirs[wd], w.wds[dir] = dir, wd
		w.mu.Unlock()
	}
	if !deep {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".maitre") {
			w.add(filepath.Join(dir, e.Name()), true)
		}
	}
}

func (w *Watcher) forget(wd int) {
	w.mu.Lock()
	if dir, ok := w.dirs[wd]; ok {
		delete(w.dirs, wd)
		delete(w.wds, dir)
	}
	w.mu.Unlock()
}
