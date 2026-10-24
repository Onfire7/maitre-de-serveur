package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/pool"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
)

// runPoolUp is `maitre pool-up`, run by maitre-pool.service at boot.
func runPoolUp(args []string) error {
	st, err := pool.Up(context.Background())
	if err != nil {
		return err
	}
	if st.Fallback {
		fmt.Printf("pool-up: no Drive found; default Pool %s is a directory on the OS partition\n", st.Mount)
	} else {
		fmt.Printf("pool-up: default Pool %s over %s\n", st.Mount, strings.Join(st.Branches, ", "))
	}
	return nil
}

// runService is `maitre service <catalog|install|list|logs|start|stop|restart|remove>`:
// a CLI stand-in for the Console's Service screens, used by tests and CI.
func runService(args []string) error {
	fs := flag.NewFlagSet("service", flag.ExitOnError)
	idDir := fs.String("identity", filepath.Join(defaultState(), "device"), "directory holding the caller's identity")
	trustDir := fs.String("trust", filepath.Join(defaultState(), "device", "trusted"), "directory of *.pub files for Nodes to trust")
	addr := fs.String("addr", fmt.Sprintf("127.0.0.1:%d", port), "Node address")
	wait := fs.Duration("wait", 0, "install: wait up to this long for the Service to be running and healthy")
	setFlags := fs.String("set", "", "install: settings as key=value,key=value")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: maitre service [flags] <catalog|install <entry> [name]|list|logs <name>|start|stop|restart|remove <name>>")
	}
	id, err := identity.Load(*idDir)
	if err != nil {
		return err
	}
	list := trust.New()
	if err := list.LoadDir(*trustDir); err != nil {
		return err
	}
	transport := connecthttp.NewTransport(NewHTTPClient(mtls.ClientConfig(id, list)), "https://"+*addr)
	c := maitrev1connect.NewServiceServiceClient(connect.NewClient(transport))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	switch fs.Arg(0) {
	case "catalog":
		res, err := c.GetCatalog(ctx, &maitrev1.GetCatalogRequest{})
		if err != nil {
			return err
		}
		idx, err := catalog.ParseIndex(res.GetIndexJson())
		if err != nil {
			return err
		}
		fmt.Printf("catalog from %s\n", res.GetSource())
		for _, e := range idx.Entries {
			fmt.Printf("  %-14s %-16s %s\n", e.ID, e.Version, e.Summary)
		}
	case "install":
		if fs.NArg() < 2 {
			return fmt.Errorf("install needs a catalog entry id")
		}
		req := &maitrev1.InstallServiceRequest{CatalogId: fs.Arg(1), Name: fs.Arg(2), Settings: map[string]string{}}
		for _, kv := range strings.Split(*setFlags, ",") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				req.Settings[k] = v
			}
		}
		res, err := c.InstallService(ctx, req)
		if err != nil {
			return err
		}
		printStatus(res.GetService())
		if *wait > 0 {
			deadline := time.Now().Add(*wait)
			for time.Now().Before(deadline) {
				time.Sleep(3 * time.Second)
				l, err := c.ListServices(ctx, &maitrev1.ListServicesRequest{})
				if err != nil {
					return err
				}
				for _, s := range l.GetServices() {
					if s.GetSpec().GetName() == res.GetService().GetSpec().GetName() {
						printStatus(s)
						if s.GetState() == maitrev1.ServiceStatus_STATE_RUNNING && s.GetHealthy() {
							return nil
						}
						if s.GetState() == maitrev1.ServiceStatus_STATE_FAILED {
							return fmt.Errorf("service failed: %s", s.GetMessage())
						}
					}
				}
			}
			return fmt.Errorf("service did not become healthy within %s", *wait)
		}
	case "list":
		res, err := c.ListServices(ctx, &maitrev1.ListServicesRequest{})
		if err != nil {
			return err
		}
		for _, s := range res.GetServices() {
			printStatus(s)
		}
	case "logs":
		res, err := c.GetServiceLogs(ctx, &maitrev1.GetServiceLogsRequest{Name: fs.Arg(1), Lines: 100})
		if err != nil {
			return err
		}
		for _, l := range res.GetLines() {
			fmt.Println(l)
		}
	case "start", "stop", "restart", "remove":
		actions := map[string]maitrev1.ControlServiceRequest_Action{
			"start": maitrev1.ControlServiceRequest_ACTION_START, "stop": maitrev1.ControlServiceRequest_ACTION_STOP,
			"restart": maitrev1.ControlServiceRequest_ACTION_RESTART, "remove": maitrev1.ControlServiceRequest_ACTION_REMOVE,
		}
		res, err := c.ControlService(ctx, &maitrev1.ControlServiceRequest{Name: fs.Arg(1), Action: actions[fs.Arg(0)]})
		if err != nil {
			return err
		}
		printStatus(res.GetService())
	default:
		return fmt.Errorf("unknown service command %q", fs.Arg(0))
	}
	return nil
}

func printStatus(s *maitrev1.ServiceStatus) {
	health := ""
	if s.GetState() == maitrev1.ServiceStatus_STATE_RUNNING {
		if s.GetHealthy() {
			health = " healthy"
		} else {
			health = " not-healthy-yet"
		}
	}
	fmt.Fprintf(os.Stdout, "%-14s %-9s%s restarts=%d %s %s\n", s.GetSpec().GetName(),
		strings.TrimPrefix(s.GetState().String(), "STATE_"), health, s.GetRestarts(), s.GetUrl(), s.GetMessage())
}
