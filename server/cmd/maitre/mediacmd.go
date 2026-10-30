package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ulikunitz/xz"

	"github.com/Onfire7/maitre-de-serveur/server/internal/depot"
)

// runMedia is `maitre media <list|build|write>`: the Console's "Prepare an
// installer USB" (Milestone 7). list and build run unprivileged; write runs
// elevated like netboot and reports progress as JSON lines (and to
// --events-file).
func runMedia(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: maitre media <list|build|write> [flags]")
	}
	switch args[0] {
	case "list":
		return runMediaList()
	case "build":
		return runMediaBuild(args[1:])
	case "write":
		return runMediaWrite(args[1:])
	}
	return fmt.Errorf("maitre media: unknown command %q", args[0])
}

type mediaDisk struct {
	Device    string `json:"device"`
	Model     string `json:"model"`
	SizeBytes uint64 `json:"size_bytes"`
	Removable bool   `json:"removable"`
	Transport string `json:"transport"`
	// Holds a mounted filesystem (the OS, or something in use).
	InUse bool `json:"in_use"`
}

// runMediaList prints removable disks as JSON (whole disks only).
func runMediaList() error {
	out, err := exec.Command("lsblk", "-J", "-b", "-o", "NAME,PATH,MODEL,SIZE,RM,TYPE,TRAN,MOUNTPOINTS").Output()
	if err != nil {
		return fmt.Errorf("lsblk: %w", err)
	}
	var parsed struct {
		Blockdevices []struct {
			Name        string    `json:"name"`
			Path        string    `json:"path"`
			Model       *string   `json:"model"`
			Size        uint64    `json:"size"`
			RM          bool      `json:"rm"`
			Type        string    `json:"type"`
			Tran        *string   `json:"tran"`
			Mountpoints []*string `json:"mountpoints"`
			Children    []struct {
				Mountpoints []*string `json:"mountpoints"`
			} `json:"children"`
		} `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return err
	}
	var disks []mediaDisk
	for _, d := range parsed.Blockdevices {
		if d.Type != "disk" || strings.HasPrefix(d.Name, "zram") || strings.HasPrefix(d.Name, "loop") {
			continue
		}
		md := mediaDisk{Device: d.Path, SizeBytes: d.Size, Removable: d.RM}
		if d.Model != nil {
			md.Model = strings.TrimSpace(*d.Model)
		}
		if d.Tran != nil {
			md.Transport = *d.Tran
		}
		for _, m := range d.Mountpoints {
			if m != nil && *m != "" {
				md.InUse = true
			}
		}
		for _, c := range d.Children {
			for _, m := range c.Mountpoints {
				if m != nil && *m != "" {
					md.InUse = true
				}
			}
		}
		disks = append(disks, md)
	}
	if disks == nil {
		disks = []mediaDisk{}
	}
	return json.NewEncoder(os.Stdout).Encode(disks)
}

// runMediaBuild makes a raw installer image from netboot files (decision
// 221), so a Depot's build goes on a stick without a release.
func runMediaBuild(args []string) error {
	fs := flag.NewFlagSet("media build", flag.ContinueOnError)
	boot := fs.String("boot", "", "directory holding the netboot files")
	out := fs.String("out", "", "raw image to write")
	image := fs.String("image", "", "image the Installer installs, e.g. 192.168.1.5:8080/maitre-server:dev")
	insecure := fs.String("insecure-registry", "", "registry host:port to pull from without TLS (a Depot)")
	serial := fs.Bool("serial", false, "add a serial console (VMs)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *boot == "" || *out == "" || *image == "" {
		return errors.New("--boot, --out and --image are required")
	}
	ev := &eventLog{}
	last := time.Now()
	err := depot.BuildStick(depot.Layout{Dir: *boot, BootDir: *boot}, *out, depot.StickParams{Image: *image, Insecure: *insecure, Serial: *serial}, func(done, total int64) {
		if time.Since(last) > 500*time.Millisecond {
			ev.emit("progress", map[string]any{"done": done, "total": total})
			last = time.Now()
		}
	})
	if err != nil {
		ev.emit("error", map[string]any{"message": err.Error()})
		return err
	}
	ev.emit("done", map[string]any{"image": *image})
	return nil
}

// runMediaWrite copies an image (raw, or xz-compressed raw) onto a disk.
func runMediaWrite(args []string) error {
	fs := flag.NewFlagSet("media write", flag.ContinueOnError)
	image := fs.String("image", "", "installer image (.img or .img.xz)")
	device := fs.String("device", "", "disk to write, e.g. /dev/sdb (everything on it is destroyed)")
	eventsFile := fs.String("events-file", "", "append JSON progress events here as well as stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *image == "" || *device == "" {
		return errors.New("--image and --device are required")
	}
	ev := &eventLog{path: *eventsFile}
	in, err := os.Open(*image)
	if err != nil {
		return err
	}
	defer in.Close()
	var src io.Reader = in
	total := int64(0)
	if st, err := in.Stat(); err == nil {
		total = st.Size()
	}
	if strings.HasSuffix(*image, ".xz") {
		r, err := xz.NewReader(in)
		if err != nil {
			return fmt.Errorf("xz: %w", err)
		}
		src = r
		total = 0 // unknown after decompression
	}
	// Unmount anything on the target first; a mounted partition would
	// corrupt both the write and the filesystem.
	_ = exec.Command("sh", "-c", "for p in $(lsblk -nlo PATH "+*device+" | tail -n +2); do umount -l \"$p\" 2>/dev/null; done").Run()
	out, err := os.OpenFile(*device, os.O_WRONLY|os.O_SYNC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", *device, err)
	}
	defer out.Close()
	ev.emit("start", map[string]any{"device": *device, "image": *image, "total": total})
	buf := make([]byte, 4<<20)
	var done int64
	last := time.Now()
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				ev.emit("error", map[string]any{"message": werr.Error()})
				return werr
			}
			done += int64(n)
			if time.Since(last) > 500*time.Millisecond {
				ev.emit("progress", map[string]any{"done": done, "total": total})
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			ev.emit("error", map[string]any{"message": rerr.Error()})
			return rerr
		}
	}
	if err := out.Sync(); err != nil {
		return err
	}
	_ = exec.Command("udevadm", "settle").Run()
	_ = exec.Command("eject", *device).Run()
	ev.emit("done", map[string]any{"done": done})
	depot.OwnByInvoker(*eventsFile)
	return nil
}
