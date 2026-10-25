package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"github.com/Onfire7/maitre-de-serveur/server/internal/ascii"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/caddy"
	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/installer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mdns"
	"github.com/Onfire7/maitre-de-serveur/server/internal/metrics"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/nodeservice"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/pool"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/serviceservice"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/svcproxy"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
	"github.com/Onfire7/maitre-de-serveur/server/internal/update"
)

// Port every Node and Installer listens on.
const port = 7443

// runAgent is `maitre agent`: the service on an installed Node. State lives
// on the config partition, mounted at --state (/var/lib/maitre on the image).
func runAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	state := fs.String("state", "/var/lib/maitre", "config partition mount point")
	listen := fs.String("listen", fmt.Sprintf("0.0.0.0:%d", port), "address to listen on")
	noMDNS := fs.Bool("no-mdns", false, "do not advertise on mDNS")
	catalogPath := fs.String("catalog", "/usr/share/maitre/catalog.json", "Catalog index baked into the image")
	noScreen := fs.Bool("no-screen", false, "do not draw the status page on stdout")
	unitsDir := fs.String("units", "/etc/containers/systemd", "Quadlet units directory")
	poolDir := fs.String("pool", pool.DefaultPool, "default Pool directory for Service volumes")
	caddyAdmin := fs.String("caddy", "127.0.0.1:2019", "Caddy admin address; empty disables")
	if err := fs.Parse(args); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	id, err := identity.Load(filepath.Join(*state, "node"))
	if err != nil {
		return fmt.Errorf("this machine has not been provisioned (no identity in %s): %w", *state, err)
	}
	st, err := store.Open(filepath.Join(*state, "fleet.db"), id.Fingerprint())
	if err != nil {
		return err
	}
	defer st.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fleet := &maitrev1.Fleet{}
	fleets, err := store.ListLive(ctx, st, store.KindFleet, func() *maitrev1.Fleet { return &maitrev1.Fleet{} })
	if err != nil {
		return err
	}
	if len(fleets) == 1 {
		fleet = fleets[0]
	}
	var self maitrev1.Node
	if _, err := st.Get(ctx, store.KindNode, id.Fingerprint(), &self); err != nil {
		return fmt.Errorf("own node record: %w", err)
	}

	list := trust.New()
	refreshTrust := func() {
		devices, err := store.ListLive(ctx, st, store.KindTrustedDevice, func() *maitrev1.TrustedDevice { return &maitrev1.TrustedDevice{} })
		if err != nil {
			log.Error("load trust list", "err", err)
			return
		}
		fps := make([]string, 0, len(devices))
		for _, d := range devices {
			if d.GetRecoveryOnly() {
				continue // the recovery key may only approve devices; that path lands in Milestone 3
			}
			fps = append(fps, d.GetDeviceId())
		}
		list.Replace(fps)
	}
	refreshTrust()
	log.Info("maitre agent", "node_id", id.Fingerprint(), "name", self.GetName(), "fleet", fleet.GetName(), "trusted_devices", list.Len())

	ensureHostname(log, hostLabel(self.GetName()))
	svc := nodeservice.New(id, nodeservice.Info{Name: self.GetName(), FleetID: fleet.GetFleetId(), Version: version}, st)
	svc.OnChange = func(r *maitrev1.Record) {
		if r.GetKind() == store.KindTrustedDevice {
			refreshTrust()
		}
	}
	svc.Metrics = metrics.New(*poolDir, *poolDir == pool.FallbackDir || !isMountpoint(*poolDir)).Read
	svc.Updates = update.New(nil, log)
	// Services: Catalog from the image, Quadlet units, Caddy routes, mDNS names.
	cat := serviceservice.NewCatalog(nil, "")
	if raw, err := os.ReadFile(*catalogPath); err == nil {
		if idx, err := catalog.ParseIndex(raw); err == nil {
			cat.Set(idx, "image:"+*catalogPath)
			log.Info("catalog loaded", "entries", len(idx.Entries), "path", *catalogPath)
		} else {
			log.Error("catalog index invalid", "err", err)
		}
	} else {
		log.Warn("no catalog index", "path", *catalogPath)
	}
	mgr := services.New(services.Config{UnitsDir: *unitsDir, PoolDir: *poolDir}, st, nil)
	ssvc := serviceservice.New(mgr, cat)
	var hostAd *mdns.Advertisement
	publish := func() {
		specs, err := store.ListLive(ctx, st, services.KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
		if err != nil {
			log.Error("list services", "err", err)
			return
		}
		var routes []caddy.Route
		var names []string
		for _, sp := range specs {
			if sp.GetWebHostPort() == 0 {
				continue
			}
			host := sp.GetName() + "." + services.DefaultSuffix
			routes = append(routes, caddy.Route{Host: host, Upstream: fmt.Sprintf("127.0.0.1:%d", sp.GetWebHostPort())})
			names = append(names, host)
		}
		if *caddyAdmin != "" {
			if err := caddy.Load(ctx, *caddyAdmin, caddy.Config(routes)); err != nil {
				log.Warn("caddy config not applied", "err", err)
			}
		}
		if !*noMDNS {
			if hostAd != nil {
				hostAd.Close()
			}
			var v4 []string
			for _, a := range installer.Addresses() {
				if !strings.Contains(a, ":") {
					v4 = append(v4, a)
				}
			}
			ad, err := mdns.PublishHosts(names, v4)
			if err != nil {
				log.Warn("mDNS names not published", "err", err)
			} else {
				hostAd = ad
			}
		}
	}
	ssvc.OnChange = publish
	publish()
	defer func() {
		if hostAd != nil {
			hostAd.Close()
		}
	}()

	rpc := connect.NewServer()
	maitrev1connect.RegisterNodeServiceHandler(rpc, svc)
	maitrev1connect.RegisterServiceServiceHandler(rpc, ssvc)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, rpc)

	mdnsState := "off"
	if !*noMDNS {
		ad, err := mdns.Publish(mdns.InstanceName(self.GetName(), id.Fingerprint()), mdns.TypeNode, listenPort(*listen), map[string]string{
			"id": id.Fingerprint(), "fleet": fleet.GetFleetId(), "name": self.GetName(), "v": version,
		})
		if err != nil {
			log.Warn("mDNS advertisement unavailable", "err", err)
			mdnsState = "not announced: " + err.Error()
		} else {
			defer ad.Close()
			mdnsState = "announced as " + self.GetName()
		}
	}
	// The machine's console is a live status page (decision 72): addresses
	// change after boot and a blank banner told nobody anything.
	if !*noScreen {
		started := time.Now()
		go func() {
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				var b strings.Builder
				fmt.Fprintf(&b, "\n%s\n\n  Maître Server %s\n  ─────────────────────────────────────────────\n", ascii.Banner(), version)
				fmt.Fprintf(&b, "  Node:          %s   in Fleet %q\n", self.GetName(), fleet.GetName())
				if addrs := installer.Addresses(); len(addrs) == 0 {
					b.WriteString("  Network:       waiting for a cable and an address...\n")
				} else {
					fmt.Fprintf(&b, "  Network:       %s\n", strings.Join(addrs, ", "))
				}
				fmt.Fprintf(&b, "  Discovery:     %s\n", mdnsState)
				fmt.Fprintf(&b, "  Trusted:       %d device(s)\n", list.Len())
				if specs, err := store.ListLive(ctx, st, services.KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} }); err == nil {
					names := make([]string, 0, len(specs))
					for _, sp := range specs {
						names = append(names, sp.GetName())
					}
					if len(names) == 0 {
						b.WriteString("  Services:      none yet\n")
					} else {
						fmt.Fprintf(&b, "  Services:      %s\n", strings.Join(names, ", "))
					}
				}
				fmt.Fprintf(&b, "  Up:            %s\n\n  Open Maître Desktop on a computer on this network to manage it.\n", time.Since(started).Round(time.Second))
				fmt.Print("\033[H\033[2J" + b.String())
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
	return serve(ctx, log, *listen, peer.Middleware(svcproxy.Handler(st, mux)), mtls.ServerConfig(id, list))
}

// runInstaller is `maitre installer`: the mode booted from installer media.
func runInstaller(args []string) error {
	fs := flag.NewFlagSet("installer", flag.ExitOnError)
	state := fs.String("state", "/run/maitre-installer", "scratch directory for the Installer's identity")
	listen := fs.String("listen", fmt.Sprintf("0.0.0.0:%d", port), "address to listen on")
	image := fs.String("image", kernelArg("maitre.image"), "container image to install (default from maitre.image= kernel argument)")
	insecure := fs.String("insecure-registry", kernelArg("maitre.insecure_registry"), "testing: registry host:port to pull from without TLS (default from maitre.insecure_registry=)")
	noMDNS := fs.Bool("no-mdns", false, "do not advertise on mDNS")
	noScreen := fs.Bool("no-screen", false, "do not redraw the status screen on stdout")
	noReboot := fs.Bool("no-reboot", false, "do not reboot after a successful install")
	fake := fs.String("fake-install", "", "development: skip bootc and seed the config partition into this directory")
	direct := fs.Bool("direct", hasKernelArg("maitre.netboot"), "run bootc on this system with --source-imgref instead of from a podman container (default when booted with maitre.netboot)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *image == "" {
		return errors.New("no image to install: pass --image or boot with maitre.image=<ref>")
	}
	if *insecure != "" && *fake == "" {
		if err := installer.AllowInsecureRegistry(*insecure); err != nil {
			return err
		}
		log.Warn("pulling from an insecure registry (testing only)", "registry", *insecure)
	}

	id, err := identity.LoadOrGenerate(filepath.Join(*state, "identity"))
	if err != nil {
		return err
	}
	opts := installer.Options{Version: version, ImageRef: *image, InsecureRegistry: *insecure}
	if *fake == "" {
		// Decision 76: media may carry the image; then bootc installs from it
		// directly, as the netboot path already does.
		local, err := installer.FindLocalImage(context.Background())
		if err != nil {
			log.Warn("media image unusable", "err", err)
		}
		if local != nil {
			src, why := installer.ChooseSource(context.Background(), *image, local)
			log.Info("install source", "source", src, "why", why)
			opts.Source = src
			*direct = true
		}
	}
	if *direct && *fake == "" {
		opts.Runner = &installer.BootcRunner{Direct: true}
		log.Info("installing with bootc --source-imgref", "image", *image, "source", opts.Source)
	}
	if *fake != "" {
		opts.Runner = &installer.FakeRunner{Dir: *fake, Delay: 2 * time.Second}
		opts.Disks = installer.FakeDisks
	}
	if !*noReboot {
		opts.Reboot = func() error { return exec.Command("systemctl", "reboot").Run() }
	}
	in := installer.New(id, opts)
	if *fake == "" {
		ensureHostname(log, "maitre-installer-"+id.Fingerprint()[:8])
	}

	rpc := connect.NewServer()
	maitrev1connect.RegisterInstallerServiceHandler(rpc, in)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, rpc)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !*noMDNS {
		ad, err := mdns.Publish(mdns.InstanceName("Maître Installer", id.Fingerprint()), mdns.TypeInstaller, listenPort(*listen), map[string]string{
			"id": id.Fingerprint(), "v": version, "machine": installer.MachineName(),
		})
		if err != nil {
			log.Warn("mDNS advertisement unavailable", "err", err)
		} else {
			defer ad.Close()
		}
	}
	if !*noScreen {
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				// Clear and redraw: this is a status screen, not a log.
				fmt.Print("\033[H\033[2J" + in.Screen())
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
	log.Info("maitre installer", "installer_id", id.Fingerprint(), "image", *image)
	return serve(ctx, log, *listen, peer.Middleware(mux), mtls.InstallerServerConfig(id))
}

// serve runs an HTTPS server until ctx is done.
func serve(ctx context.Context, log *slog.Logger, addr string, h http.Handler, tlsCfg *tls.Config) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Info("listening", "addr", addr)
	// Certificates come from TLSConfig, so the file arguments are empty.
	if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// isMountpoint reports whether dir is a mount (the Pool) rather than a plain
// directory on the OS partition.
func isMountpoint(dir string) bool {
	b, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) > 1 && f[1] == dir {
			return true
		}
	}
	return false
}

// listenPort extracts the port from a host:port listen address.
func listenPort(addr string) uint16 {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return port
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return port
	}
	return uint16(n)
}

// hasKernelArg reports whether a bare flag (or key=anything) is on /proc/cmdline.
func hasKernelArg(key string) bool {
	b, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return false
	}
	for _, f := range strings.Fields(string(b)) {
		if f == key || strings.HasPrefix(f, key+"=") {
			return true
		}
	}
	return false
}

// kernelArg reads key=<value> from /proc/cmdline.
func kernelArg(key string) string {
	b, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return ""
	}
	for _, f := range strings.Fields(string(b)) {
		if v, ok := strings.CutPrefix(f, key+"="); ok {
			return v
		}
	}
	return ""
}
