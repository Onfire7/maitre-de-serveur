// Command maitre is the Maître Server agent binary.
//
//	maitre agent      run on Maître Server (not yet implemented)
//	maitre installer  run from installer media (not yet implemented)
//	maitre dev        run on any Linux host for development
//	maitre keygen     create a Trusted Device identity for testing
//	maitre info       call GetNodeInfo on a Node as a Trusted Device
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
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"golang.org/x/net/http2"

	maitrev1 "github.com/maitre-de-serveur/maitre/server/gen/maitre/v1"
	"github.com/maitre-de-serveur/maitre/server/gen/maitre/v1/maitrev1connect"
	"github.com/maitre-de-serveur/maitre/server/internal/identity"
	"github.com/maitre-de-serveur/maitre/server/internal/mtls"
	"github.com/maitre-de-serveur/maitre/server/internal/nodeservice"
	"github.com/maitre-de-serveur/maitre/server/internal/trust"
)

// version is set by the build; "dev" otherwise.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "dev":
		err = runDev(os.Args[2:])
	case "keygen":
		err = runKeygen(os.Args[2:])
	case "info":
		err = runInfo(os.Args[2:])
	case "agent", "installer":
		err = fmt.Errorf("%s: not implemented yet (Milestone 1)", os.Args[1])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "maitre:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: maitre <dev|keygen|info|agent|installer> [flags]")
}

func runDev(args []string) error {
	fs := flag.NewFlagSet("dev", flag.ExitOnError)
	state := fs.String("state", defaultState(), "directory for identity and trust list")
	listen := fs.String("listen", "127.0.0.1:7443", "address to listen on")
	name := fs.String("name", hostname(), "Node name to report")
	if err := fs.Parse(args); err != nil {
		return err
	}

	id, err := identity.LoadOrGenerate(filepath.Join(*state, "node"))
	if err != nil {
		return err
	}
	list := trust.New()
	trustDir := filepath.Join(*state, "trusted")
	if err := list.LoadDir(trustDir); err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	log.Info("maitre dev", "node_id", id.Fingerprint(), "trusted_keys", list.Len(), "trust_dir", trustDir)
	if list.Len() == 0 {
		log.Warn("trust list is empty; no client can connect. Drop a *.pub from `maitre keygen` into the trust dir.")
	}

	rpc := connect.NewServer()
	maitrev1connect.RegisterNodeServiceHandler(rpc, nodeservice.New(id, nodeservice.Info{
		Name:    *name,
		Version: version,
		DevMode: true,
	}))
	mux := http.NewServeMux()
	connecthttp.Mount(mux, rpc)

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		TLSConfig:         mtls.ServerConfig(id, list),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info("listening", "addr", *listen)
	// Certificates come from TLSConfig, so the file arguments are empty.
	if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func runKeygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", filepath.Join(defaultState(), "device"), "directory to write the identity into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id, err := identity.LoadOrGenerate(*out)
	if err != nil {
		return err
	}
	fmt.Printf("identity %s\npublic key: %s\n", id.Fingerprint(), filepath.Join(*out, "identity.pub"))
	return nil
}

func runInfo(args []string) error {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	idDir := fs.String("identity", filepath.Join(defaultState(), "device"), "directory holding the caller's identity")
	trustDir := fs.String("trust", filepath.Join(defaultState(), "device", "trusted"), "directory of *.pub files for Nodes to trust")
	addr := fs.String("addr", "127.0.0.1:7443", "Node address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id, err := identity.Load(*idDir)
	if err != nil {
		return fmt.Errorf("load identity (run `maitre keygen` first): %w", err)
	}
	list := trust.New()
	if err := list.LoadDir(*trustDir); err != nil {
		return err
	}
	transport := connecthttp.NewTransport(NewHTTPClient(mtls.ClientConfig(id, list)), "https://"+*addr)
	client := maitrev1connect.NewNodeServiceClient(connect.NewClient(transport))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := client.GetNodeInfo(ctx, &maitrev1.GetNodeInfoRequest{})
	if err != nil {
		return err
	}
	n := res.GetNode()
	fmt.Printf("node_id   %s\nname      %s\nfleet_id  %s\nversion   %s\nuptime    %ds\ndev_mode  %v\n",
		n.GetNodeId(), n.GetName(), n.GetFleetId(), n.GetVersion(), n.GetUptimeSeconds(), n.GetDevMode())
	return nil
}

// NewHTTPClient returns an HTTP/2 client that uses cfg for every connection.
func NewHTTPClient(cfg *tls.Config) *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			TLSClientConfig: cfg,
			DialTLSContext: func(ctx context.Context, network, addr string, c *tls.Config) (net.Conn, error) {
				d := &tls.Dialer{Config: c}
				return d.DialContext(ctx, network, addr)
			},
		},
	}
}

func defaultState() string {
	if v := os.Getenv("MAITRE_STATE"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".maitre"
	}
	return filepath.Join(home, ".maitre")
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "dev-node"
	}
	return h
}
