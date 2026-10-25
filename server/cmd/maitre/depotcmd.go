package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Onfire7/maitre-de-serveur/server/internal/ascii"
	"github.com/Onfire7/maitre-de-serveur/server/internal/depot"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mdns"
)

// runDepot is the developer-side image and netboot server (decisions 79-88):
//
//	maitre depot publish <local image> [--name maitre-server] [--tag dev]
//	maitre depot ls
//	maitre depot serve [--listen :8080]
func runDepot(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: maitre depot <publish|ls|serve> [flags]")
	}
	switch args[0] {
	case "publish":
		return runDepotPublish(args[1:])
	case "ls":
		return runDepotLs(args[1:])
	case "serve":
		return runDepotServe(args[1:])
	}
	return fmt.Errorf("maitre depot: unknown command %q", args[0])
}

func depotFlags(fs *flag.FlagSet) *string {
	return fs.String("dir", depot.DefaultDir(), "Depot directory")
}

func runDepotPublish(args []string) error {
	fs := flag.NewFlagSet("depot publish", flag.ContinueOnError)
	dir := depotFlags(fs)
	name := fs.String("name", "maitre-server", "image name in the Depot")
	tag := fs.String("tag", "dev", "tag in the Depot")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Allow flags after the image as well as before it.
	if fs.NArg() > 1 {
		rest := fs.Args()
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("usage: maitre depot publish <local image> [--name N] [--tag T]")
		}
		args = rest[:1]
	} else {
		args = fs.Args()
	}
	if len(args) != 1 {
		return errors.New("usage: maitre depot publish <local image> [--name N] [--tag T]")
	}
	image := args[0]
	l := depot.Layout{Dir: *dir}
	started := time.Now()
	if err := depot.Publish(context.Background(), l, image, *name, *tag, os.Stdout, os.Stderr); err != nil {
		return err
	}
	fmt.Printf("published %s as %s:%s in %s (%s)\n", image, *name, *tag, l.Images(), time.Since(started).Round(time.Second))
	return nil
}

func runDepotLs(args []string) error {
	fs := flag.NewFlagSet("depot ls", flag.ContinueOnError)
	dir := depotFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	imgs, err := (&depot.Registry{Dir: depot.Layout{Dir: *dir}.Images()}).Images()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(imgs))
	for n := range imgs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Printf("%s\t%s\n", n, strings.Join(imgs[n], " "))
	}
	return nil
}

func runDepotServe(args []string) error {
	fs := flag.NewFlagSet("depot serve", flag.ContinueOnError)
	dir := depotFlags(fs)
	listen := fs.String("listen", ":8080", "HTTP listen address (registry and netboot files)")
	noMDNS := fs.Bool("no-mdns", false, "do not publish "+depot.Hostname+" over mDNS")
	if err := fs.Parse(args); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	l := depot.Layout{Dir: *dir}
	for _, d := range []string{l.Images(), l.Boot()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ip, err := depot.LANAddress()
	if err != nil {
		log.Warn("no LAN address", "err", err)
		ip = "127.0.0.1"
	}
	if !*noMDNS {
		ad, err := mdns.PublishHosts([]string{depot.Hostname}, []string{ip})
		if err != nil {
			log.Warn("mDNS name not published (is avahi running?)", "name", depot.Hostname, "err", err)
		} else {
			defer ad.Close()
		}
	}
	srv := &http.Server{Handler: logging(log, depot.Handler(l)), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	imgs, _ := (&depot.Registry{Dir: l.Images()}).Images()
	fmt.Printf("\n%s\n\n  Maître Depot  %s\n\n", ascii.Banner(), l.Dir)
	fmt.Printf("  Registry: %s:%d  (also %s:%d)\n", ip, port, depot.Hostname, port)
	fmt.Printf("  Netboot:  http://%s:%d/boot/\n\n", ip, port)
	if len(imgs) == 0 {
		fmt.Printf("  No images yet. Publish one with:\n    maitre depot publish localhost/maitre-server:<tag>\n\n")
	}
	for n, tags := range imgs {
		for _, t := range tags {
			fmt.Printf("  Update a Node to: %s:%d/%s:%s\n", ip, port, n, t)
		}
	}
	fmt.Println()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func logging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		if strings.Contains(r.URL.Path, "/blobs/") && r.Method == http.MethodHead {
			return // skopeo probes every layer; too noisy
		}
		log.Info("http", "method", r.Method, "path", r.URL.Path, "from", r.RemoteAddr, "ms", time.Since(started).Milliseconds())
	})
}
