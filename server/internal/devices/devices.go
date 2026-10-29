// Package devices lists a Node's devices that a Catalog entry's device
// setting can name (docs/popularity-and-grants.md, G5).
package devices

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Device is one device: the path a unit names, its kind, and a name for
// a person.
type Device struct {
	Path, Kind, Name string
}

// List reads root (/dev on a Node) for devices of kind: serial, video, or
// "" for both. Serial devices are named by /dev/serial/by-id where udev
// made a link, since ttyUSB0 can become ttyUSB1 after a reboot; those
// without one are listed by their own name.
func List(root, kind string) []Device {
	var out []Device
	if kind == "" || kind == "serial" {
		linked := map[string]bool{}
		byID := filepath.Join(root, "serial", "by-id")
		links, _ := os.ReadDir(byID)
		for _, l := range links {
			p := filepath.Join(byID, l.Name())
			if target, err := filepath.EvalSymlinks(p); err == nil {
				linked[filepath.Base(target)] = true
			}
			out = append(out, Device{Path: "/dev/serial/by-id/" + l.Name(), Kind: "serial", Name: l.Name()})
		}
		for _, pattern := range []string{"ttyUSB*", "ttyACM*"} {
			matches, _ := filepath.Glob(filepath.Join(root, pattern))
			for _, m := range matches {
				if !linked[filepath.Base(m)] {
					out = append(out, Device{Path: "/dev/" + filepath.Base(m), Kind: "serial", Name: filepath.Base(m)})
				}
			}
		}
	}
	if kind == "" || kind == "video" {
		matches, _ := filepath.Glob(filepath.Join(root, "dri", "renderD*"))
		for _, m := range matches {
			out = append(out, Device{Path: "/dev/dri/" + filepath.Base(m), Kind: "video", Name: "GPU " + strings.TrimPrefix(filepath.Base(m), "renderD")})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Kind < out[j].Kind || out[i].Kind == out[j].Kind && out[i].Path < out[j].Path
	})
	return out
}
