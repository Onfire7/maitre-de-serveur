package tui

import (
	"os"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Console ioctls (linux/kd.h).
const (
	kdFontOp      = 0x4B72
	kdFontOpSet   = 0
	kdFontOpGet   = 1
	gioUnimap     = 0x4B66
	pioUnimap     = 0x4B67
	pioUnimapClr  = 0x4B68
	pioCmap       = 0x4B71
	kdGetMode     = 0x4B3B
	fontMaxWidth  = 32
	unimapMaxSize = 8192
)

type fontOp struct {
	op, flags, width, height, charcount uint32
	_                                   uint32
	data                                uintptr
}

type unimapDesc struct {
	count   uint16
	_       [6]byte
	entries uintptr
}

type unimapInit struct{ size, step, level uint16 }

func ioctl(f *os.File, req uintptr, arg unsafe.Pointer) error {
	_, _, e := unix.Syscall(unix.SYS_IOCTL, f.Fd(), req, uintptr(arg))
	if e != 0 {
		return e
	}
	return nil
}

// consoleSetup, on a Linux virtual console, adds braille to its font and
// sets the screen's palette (decision 212). On any other terminal it does
// nothing: that terminal has braille and its own colours.
func consoleSetup(tty *os.File) consoleMode {
	var mode uint32
	if ioctl(tty, kdGetMode, unsafe.Pointer(&mode)) != nil {
		return consoleTerminal
	}
	buf := make([]byte, 512*fontPitch*(fontMaxWidth/8))
	var op fontOp
	get := func() error {
		op = fontOp{op: kdFontOpGet, width: fontMaxWidth, height: fontPitch, charcount: 512, data: uintptr(unsafe.Pointer(&buf[0]))}
		err := ioctl(tty, kdFontOp, unsafe.Pointer(&op))
		runtime.KeepAlive(buf)
		return err
	}
	err := get()
	if err != nil {
		// With deferred takeover the framebuffer console replaces the
		// placeholder (which has no font) only once a character other than
		// a space is written, and then in the background: write a dot,
		// clear the screen and wait for it (about 10 ms).
		_, _ = tty.WriteString("\033[H.\033[H\033[2J")
		for range 20 {
			time.Sleep(50 * time.Millisecond)
			if err = get(); err == nil {
				break
			}
		}
	}
	if err != nil {
		return consoleBlocks
	}
	pairs := make([]unipair, unimapMaxSize)
	desc := unimapDesc{count: unimapMaxSize, entries: uintptr(unsafe.Pointer(&pairs[0]))}
	err = ioctl(tty, gioUnimap, unsafe.Pointer(&desc))
	runtime.KeepAlive(pairs)
	if err != nil {
		return consoleBlocks
	}
	desc0 := int(desc.count)
	pal := cmap()
	setPalette := func() {
		_ = ioctl(tty, pioCmap, unsafe.Pointer(&pal[0]))
		runtime.KeepAlive(pal)
	}
	// Set up already (the agent restarted): keep it.
	if hasBraille(int(op.charcount), pairs[:desc0]) {
		setPalette()
		return consoleBraille
	}
	font, uni, ok := withBraille(consoleFont{w: int(op.width), h: int(op.height), n: int(op.charcount), data: buf}, pairs[:desc0])
	if !ok {
		return consoleBlocks
	}
	set := fontOp{op: kdFontOpSet, width: uint32(font.w), height: uint32(font.h), charcount: uint32(font.n), data: uintptr(unsafe.Pointer(&font.data[0]))}
	err = ioctl(tty, kdFontOp, unsafe.Pointer(&set))
	runtime.KeepAlive(font.data)
	if err != nil {
		return consoleBlocks
	}
	// Then the map naming glyphs 256..511 (and keeping the rest). Without
	// it the 512-glyph font would cost the colours for nothing: put the
	// old font and map back.
	restore := func() consoleMode {
		op.op = kdFontOpSet
		_ = ioctl(tty, kdFontOp, unsafe.Pointer(&op))
		runtime.KeepAlive(buf)
		init := unimapInit{}
		_ = ioctl(tty, pioUnimapClr, unsafe.Pointer(&init))
		old := pairs[:desc0]
		if len(old) > 0 {
			d := unimapDesc{count: uint16(len(old)), entries: uintptr(unsafe.Pointer(&old[0]))}
			_ = ioctl(tty, pioUnimap, unsafe.Pointer(&d))
			runtime.KeepAlive(old)
		}
		return consoleBlocks
	}
	init := unimapInit{}
	if ioctl(tty, pioUnimapClr, unsafe.Pointer(&init)) != nil {
		return restore()
	}
	desc = unimapDesc{count: uint16(len(uni)), entries: uintptr(unsafe.Pointer(&uni[0]))}
	err = ioctl(tty, pioUnimap, unsafe.Pointer(&desc))
	runtime.KeepAlive(uni)
	if err != nil {
		return restore()
	}
	setPalette()
	return consoleBraille
}
