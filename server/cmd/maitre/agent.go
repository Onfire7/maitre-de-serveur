package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
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
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/installer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mdns"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/nodeservice"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
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

	svc := nodeservice.New(id, nodeservice.Info{Name: self.GetName(), FleetID: fleet.GetFleetId(), Version: version}, st)
	svc.OnChange = func(r *maitrev1.Record) {
		if r.GetKind() == store.KindTrustedDevice {
			refreshTrust()
		}
	}
	rpc := connect.NewServer()
	maitrev1connect.RegisterNodeServiceHandler(rpc, svc)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, rpc)

	if !*noMDNS {
		ad, err := mdns.Publish(mdns.InstanceName(self.GetName(), id.Fingerprint()), mdns.TypeNode, listenPort(*listen), map[string]string{
			"id": id.Fingerprint(), "fleet": fleet.GetFleetId(), "name": self.GetName(), "v": version,
		})
		if err != nil {
			log.Warn("mDNS advertisement unavailable", "err", err)
		} else {
			defer ad.Close()
		}
	}
	return serve(ctx, log, *listen, peer.Middleware(mux), mtls.ServerConfig(id, list))
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
	opts := installer.Options{Version: version, ImageRef: *image}
	if *fake != "" {
		opts.Runner = &installer.FakeRunner{Dir: *fake, Delay: 2 * time.Second}
		opts.Disks = installer.FakeDisks
	}
	if !*noReboot {
		opts.Reboot = func() error { return exec.Command("systemctl", "reboot").Run() }
	}
	in := installer.New(id, opts)

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
