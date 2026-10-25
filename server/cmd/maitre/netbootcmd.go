package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Onfire7/maitre-de-serveur/server/internal/depot"
	"github.com/Onfire7/maitre-de-serveur/server/internal/update"
)

// runNetboot is `maitre netboot`: the privileged helper Maître Desktop runs
// (elevated, decision 92) so a new machine can boot the Installer from the
// network with nothing but the Console. It answers PXE, serves the boot
// files from --dir over TFTP and HTTP, and stops on its own after --idle
// without a client (decision 94), when --stop-file appears, or on Ctrl-C.
//
// Events are JSON lines on stdout and, with --events-file, appended there
// too (elevation on macOS and Windows gives the Desktop no live stdout).
func runNetboot(args []string) error {
	fs := flag.NewFlagSet("netboot", flag.ContinueOnError)
	dir := fs.String("dir", "", "directory holding the netboot files (required)")
	image := fs.String("image", "", "image reference the Installer pulls (default: from netboot.json)")
	insecure := fs.String("insecure", "", "registry host:port the Installer may pull from over plain HTTP")
	iface := fs.String("interface", "", "interface to answer PXE on (default: the one with the LAN address)")
	httpPort := fs.Int("http-port", 0, "HTTP port for kernel, initramfs and root (0 = any free port)")
	idle := fs.Duration("idle", time.Hour, "stop after this long without a client; 0 = never")
	serial := fs.Bool("serial", false, "netbooted machines also get a serial console (QEMU)")
	eventsFile := fs.String("events-file", "", "append JSON events here as well as stdout")
	stopFile := fs.String("stop-file", "", "exit when this file appears")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return errors.New("--dir is required")
	}
	l := depot.Layout{Dir: *dir, BootDir: *dir}
	if !depot.HaveBootFiles(l) {
		return fmt.Errorf("no netboot files in %s", l.Boot())
	}
	if *image == "" {
		m, err := depot.ReadManifest(l)
		if err != nil || m.Image == "" {
			return errors.New("--image is required (netboot.json names none)")
		}
		*image = m.Image
	}
	if !update.ValidRef(*image) {
		return fmt.Errorf("%q is not an image reference", *image)
	}
	if *insecure != "" && !update.LANHost(*insecure) {
		return fmt.Errorf("--insecure %s: only IP addresses, localhost and .local names may skip TLS", *insecure)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ev := &eventLog{path: *eventsFile}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ip, err := depot.LANAddress()
	if err != nil {
		return fmt.Errorf("no LAN address: %w", err)
	}
	// Only the LAN address, and only the boot files: the directory also
	// holds the events and stop files, which are nobody else's business.
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", ip, *httpPort))
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ifname := *iface
	if ifname == "" {
		ifname, _ = depot.InterfaceFor(ip)
	}
	if err := depot.WriteGrubConfig(l, depot.BootParams{IP: ip, HTTPPort: port, Image: *image, Insecure: *insecure, Serial: *serial}); err != nil {
		return err
	}
	srv := &http.Server{Handler: depot.BootFileHandler(l), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()

	// Idle: any client interaction restarts the clock.
	var mu sync.Mutex
	last := time.Now()
	nb := &depot.Netboot{Layout: l, IP: net.ParseIP(ip).To4(), Interface: ifname, Log: log, BindIP: true, OnEvent: func(kind string, f map[string]any) {
		mu.Lock()
		last = time.Now()
		mu.Unlock()
		ev.emit(kind, f)
	}}
	if err := nb.Start(ctx); err != nil {
		ev.emit("error", map[string]any{"message": err.Error()})
		return err
	}
	ev.emit("ready", map[string]any{"ip": ip, "interface": ifname, "http_port": port, "image": *image, "idle_seconds": int(idle.Seconds())})

	// Whatever the idle setting, a helper that has run as root for a day is
	// one somebody forgot about (or one the LAN keeps poking to stay alive).
	const maxRun = 24 * time.Hour
	started := time.Now()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			ev.emit("stopped", map[string]any{"reason": "signal"})
			shutdown(srv, nb)
			return nil
		case <-t.C:
			if *stopFile != "" {
				if _, err := os.Stat(*stopFile); err == nil {
					_ = os.Remove(*stopFile)
					ev.emit("stopped", map[string]any{"reason": "requested"})
					shutdown(srv, nb)
					return nil
				}
			}
			mu.Lock()
			quiet := time.Since(last)
			mu.Unlock()
			if time.Since(started) > maxRun {
				ev.emit("stopped", map[string]any{"reason": "max runtime"})
				shutdown(srv, nb)
				return nil
			}
			if *idle > 0 && quiet > *idle {
				ev.emit("stopped", map[string]any{"reason": "idle", "after_seconds": int(idle.Seconds())})
				shutdown(srv, nb)
				return nil
			}
		}
	}
}

func shutdown(srv *http.Server, nb *depot.Netboot) {
	nb.Close()
	c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(c)
}

// eventLog writes JSON lines to stdout and optionally a file.
type eventLog struct {
	mu   sync.Mutex
	path string
}

func (e *eventLog) emit(kind string, fields map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rec := map[string]any{"event": kind, "time": time.Now().UTC().Format(time.RFC3339)}
	for k, v := range fields {
		rec[k] = v
	}
	b, _ := json.Marshal(rec)
	fmt.Println(string(b))
	if e.path != "" {
		if f, err := os.OpenFile(e.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			_, _ = f.Write(append(b, '\n'))
			f.Close()
		}
	}
}
