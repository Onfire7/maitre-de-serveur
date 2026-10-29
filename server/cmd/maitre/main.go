// Command maitre is the Maître Server agent binary.
//
//	maitre agent      run on Maître Server (state on the config partition)
//	maitre installer  run from installer media
//	maitre dev        run on any Linux host for development
//	maitre keygen     create a Trusted Device identity for testing
//	maitre info       call GetNodeInfo on a Node as a Trusted Device
//	maitre provision  pair with an Installer and provision it (CLI stand-in for the Console)
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"golang.org/x/net/http2"
	"golang.org/x/net/websocket"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/nodeservice"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/svcproxy"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
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
	case "metrics":
		err = runInfo(append([]string{"-metrics"}, os.Args[2:]...))
	case "drives":
		err = runInfo(append([]string{"-drives"}, os.Args[2:]...))
	case "agent":
		err = runAgent(os.Args[2:])
	case "installer":
		err = runInstaller(os.Args[2:])
	case "provision":
		err = runProvision(os.Args[2:])
	case "catalog":
		err = runCatalog(os.Args[2:])
	case "catalog-notice":
		err = runCatalogNotice(os.Args[2:])
	case "catalog-check":
		err = runCatalogCheck(os.Args[2:])
	case "catalog-index":
		err = runCatalogIndex(os.Args[2:])
	case "names-index":
		err = runNamesIndex(os.Args[2:])
	case "files":
		err = runFiles(os.Args[2:])
	case "space":
		err = runSpace(os.Args[2:])
	case "pool-up":
		err = runPoolUp(os.Args[2:])
	case "service":
		err = runService(os.Args[2:])
	case "depot":
		err = runDepot(os.Args[2:])
	case "update":
		err = runUpdate(os.Args[2:])
	case "netboot":
		err = runNetboot(os.Args[2:])
	case "tui":
		err = runTUI(os.Args[2:])
	case "record":
		err = runRecord(os.Args[2:])
	case "recover":
		err = runRecover(os.Args[2:])
	case "authorize":
		err = runAuthorize(os.Args[2:])
	case "recovery-code":
		err = runRecoveryCode(os.Args[2:])
	case "media":
		err = runMedia(os.Args[2:])
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
	fmt.Fprintln(os.Stderr, "usage: maitre <agent|installer|pool-up|dev|keygen|info|provision|service|catalog-index|catalog-check|catalog-notice|catalog|depot|update|netboot|tui|record|recover|authorize|media> [flags]")
}

func runDev(args []string) error {
	fs := flag.NewFlagSet("dev", flag.ExitOnError)
	state := fs.String("state", defaultState(), "directory for identity and trust list")
	listen := fs.String("listen", "127.0.0.1:7443", "address to listen on")
	name := fs.String("name", hostname(), "Node name to report")
	fake := fs.String("fake-service", "", "register a Service of this name backed by a built-in echo server (HTTP and WebSocket), to exercise the Console's tunnel")
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
	}, nil))
	mux := http.NewServeMux()
	connecthttp.Mount(mux, rpc)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var h http.Handler = mux
	if *fake != "" {
		st, err := store.Open(filepath.Join(*state, "dev.db"), id.Fingerprint())
		if err != nil {
			return err
		}
		defer st.Close()
		port, err := startEchoService(ctx)
		if err != nil {
			return err
		}
		if _, err := st.Put(ctx, services.KindService, *fake, &maitrev1.ServiceSpec{Name: *fake, WebHostPort: uint32(port)}, false); err != nil {
			return err
		}
		log.Info("fake service registered", "name", *fake, "port", port)
		h = svcproxy.Handler(st, mux)
	}
	return serve(ctx, log, *listen, peer.Middleware(h), mtls.ServerConfig(id, list))
}

// startEchoService serves a stand-in Service on a loopback port: GET /path
// answers with the request line and Host, and /ws echoes WebSocket messages.
func startEchoService(ctx context.Context) (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	mux := http.NewServeMux()
	mux.Handle("/ws", websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler:   func(ws *websocket.Conn) { _, _ = io.Copy(ws, ws) },
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "echo %s %s host=%s", r.Method, r.URL.RequestURI(), r.Host)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); _ = srv.Close() }()
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().(*net.TCPAddr).Port, nil
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
	metrics := fs.Bool("metrics", false, "print the Node's gauges (CPU, memory, Pool, network, temperature, load) instead")
	drives := fs.Bool("drives", false, "print the Node's Drives (ListDrives) instead")
	rename := fs.String("rename", "", "give the Node this name instead")
	hist := fs.Duration("history", 0, "print the Node's stored metrics over this long (e.g. 1h, 168h) instead: each series' step, latest average and peak")
	series := fs.String("series", "", "with --history: comma-separated series or prefixes ending in / (default node/ and drive/)")
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if *rename != "" {
		res, err := client.RenameNode(ctx, &maitrev1.RenameNodeRequest{Name: *rename})
		if err != nil {
			return err
		}
		fmt.Printf("renamed to %s\n", res.GetNode().GetName())
		return nil
	}
	if *hist > 0 {
		var names []string
		if *series != "" {
			names = strings.Split(*series, ",")
		}
		res, err := client.GetMetricsHistory(ctx, &maitrev1.GetMetricsHistoryRequest{Series: names, FromUnixMs: time.Now().Add(-*hist).UnixMilli()})
		if err != nil {
			return err
		}
		for _, s := range res.GetSeries() {
			last, peak, n := math.NaN(), math.NaN(), 0
			for i, a := range s.GetAvg() {
				if math.IsNaN(a) {
					continue
				}
				n++
				last = a
				if m := s.GetMax()[i]; math.IsNaN(peak) || m > peak {
					peak = m
				}
			}
			fmt.Printf("%-34s step=%-6s points=%-4d with-data=%-4d last=%-12.2f peak=%.2f\n", s.GetName(), time.Duration(s.GetStepMs())*time.Millisecond, len(s.GetAvg()), n, last, peak)
		}
		return nil
	}
	if *drives {
		sc := maitrev1connect.NewStorageServiceClient(connect.NewClient(transport))
		res, err := sc.ListDrives(ctx, &maitrev1.ListDrivesRequest{})
		if err != nil {
			return err
		}
		for _, d := range res.GetDrives() {
			fmt.Printf("%-14s %-9s id=%-36s size=%6dG fstype=%-6s label=%-14s system=%-5v adopted=%-5v model=%q tran=%s tags=%v name=%q\n",
				d.GetDevice(), d.GetTransport(), d.GetDriveId(), d.GetSizeBytes()>>30, d.GetFstype(), d.GetLabel(), d.GetSystem(), d.GetAdopted(), d.GetModel(), d.GetTransport(), d.GetAutoTags(), d.GetRecord().GetName())
		}
		return nil
	}
	if *metrics {
		// Two readings: the first since the agent's previous reading primes the
		// rates, the second covers a known two-second window.
		if _, err := client.GetNodeMetrics(ctx, &maitrev1.GetNodeMetricsRequest{}); err != nil {
			return err
		}
		time.Sleep(2 * time.Second)
		res, err := client.GetNodeMetrics(ctx, &maitrev1.GetNodeMetricsRequest{})
		if err != nil {
			return err
		}
		m := res.GetMetrics()
		fmt.Printf("cpu_percent  %.1f\nload_1m      %.2f\nmemory       %d / %d MiB\npool         %d / %d GiB (fallback=%v)\nnet          rx %.0f B/s, tx %.0f B/s\ntemperature  %.0f C\n",
			m.GetCpuPercent(), m.GetLoad_1M(), m.GetMemoryUsedBytes()>>20, m.GetMemoryTotalBytes()>>20,
			m.GetPoolUsedBytes()>>30, m.GetPoolTotalBytes()>>30, m.GetPoolFallback(),
			m.GetNetRxBytesPerSec(), m.GetNetTxBytesPerSec(), m.GetTemperatureC())
		return nil
	}
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
