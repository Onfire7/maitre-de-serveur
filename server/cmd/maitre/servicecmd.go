package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/connectproto"
	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/pool"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
)

// runPoolUp is `maitre pool-up`, run by maitre-pool.service at boot.
func runPoolUp(args []string) error {
	fs := flag.NewFlagSet("pool-up", flag.ExitOnError)
	state := fs.String("state", "/var/lib/maitre", "config partition mount point (Drive records)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	// Mount this Node's Drives and assemble every Space's Pool over their
	// local folders; the agent adds peers' Pools for spanning Spaces later.
	id, err := identity.Load(filepath.Join(*state, "node"))
	if err != nil {
		fmt.Println("pool-up: no Node identity yet; nothing to assemble")
		return nil
	}
	db, err := store.Open(filepath.Join(*state, "fleet.db"), id.Fingerprint())
	if err != nil {
		return err
	}
	defer db.Close()
	sm := storage.New(db, nil)
	if _, err := sm.AdoptInternal(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "pool-up: built-in drive:", err)
	}
	_, errs := sm.MountAll(ctx)
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "pool-up:", e)
	}
	bySpace, noCreate, err := sm.LocalBranches(ctx)
	if err != nil {
		return err
	}
	spaces, _ := storage.Spaces(ctx, db)
	placement := map[string]pool.Placement{}
	for _, b := range spaces {
		placement[b.GetSpaceId()] = pool.PlacementOf(b.GetPlacement(), b.GetPreferLocal())
	}
	if len(bySpace) == 0 {
		fmt.Println("pool-up: the Fleet has no Storage Spaces yet")
	}
	for space, branches := range bySpace {
		storage.MarkSpaceFolders(branches)
		if err := pool.Assemble(ctx, space, branches, len(branches), placement[space], noCreate); err != nil {
			fmt.Fprintln(os.Stderr, "pool-up:", err)
		} else if len(branches) > 0 {
			fmt.Printf("pool-up: Pool %s over %s\n", space, strings.Join(branches, ", "))
		}
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
	deleteData := fs.Bool("delete-data", false, "remove: also delete the Service's volumes")
	allow := fs.Bool("allow", false, "install: sign, as this device, the grants the entry needs (decision 189), as the Console's Allow does")
	without := fs.String("without", "", "install: the entry's optional features to leave out, comma separated (docs/popularity-and-grants.md, G8)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: maitre service [flags first] <catalog|install <entry> [name]|list|logs <name>|start|stop|restart|remove <name>>")
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
		// Decision 78: the caller builds the spec; the CLI uses the index the
		// Node still carries for its own convenience.
		cres, err := c.GetCatalog(ctx, &maitrev1.GetCatalogRequest{})
		if err != nil {
			return err
		}
		idx, err := catalog.ParseIndex(cres.GetIndexJson())
		if err != nil {
			return err
		}
		entry := idx.Find(fs.Arg(1))
		if entry == nil {
			return fmt.Errorf("catalog entry %q not found", fs.Arg(1))
		}
		settings := map[string]string{}
		for _, kv := range strings.Split(*setFlags, ",") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				settings[k] = v
			}
		}
		var off []string
		if *without != "" {
			off = strings.Split(*without, ",")
		}
		spec := catalog.SpecWithout(entry, fs.Arg(2), settings, "", off)
		res, err := c.InstallService(ctx, &maitrev1.InstallServiceRequest{Spec: spec})
		if need := grantsNeeded(err); need != nil && *allow {
			nodes := maitrev1connect.NewNodeServiceClient(connect.NewClient(transport))
			if err := signGrants(ctx, nodes, id, need); err != nil {
				return err
			}
			res, err = c.InstallService(ctx, &maitrev1.InstallServiceRequest{Spec: spec})
		}
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
		res, err := c.ControlService(ctx, &maitrev1.ControlServiceRequest{Name: fs.Arg(1), Action: actions[fs.Arg(0)], DeleteData: *deleteData && fs.Arg(0) == "remove"})
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

// grantsNeeded is the GrantsNeeded detail of a refused Service call.
func grantsNeeded(err error) *maitrev1.GrantsNeeded {
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeFailedPrecondition {
		return nil
	}
	for _, d := range ce.Details() {
		if v, err := connectproto.UnmarshalErrorDetail(d); err == nil {
			if n, ok := v.(*maitrev1.GrantsNeeded); ok {
				return n
			}
		}
	}
	return nil
}

// signGrants signs a grant for each option, as this device, the way the
// Console's Allow does.
func signGrants(ctx context.Context, nodes maitrev1connect.NodeServiceClient, id *identity.Identity, need *maitrev1.GrantsNeeded) error {
	for i, option := range need.GetOptions() {
		g := &maitrev1.Grant{NodeId: need.GetNodeId(), Service: need.GetService(), Option: option, ImageRepository: need.GetImageRepository(), GrantedUnixMs: time.Now().UnixMilli(), DeviceName: "maitre service --allow"}
		if i < len(need.GetComponents()) {
			g.Component, g.ImageRepository = need.GetComponents()[i], need.GetImageRepositories()[i]
		}
		body, err := proto.Marshal(g)
		if err != nil {
			return err
		}
		if _, err := nodes.PutRecord(ctx, &maitrev1.PutRecordRequest{Record: signed(id, &maitrev1.Record{Kind: "grant", Id: need.GetGrantIds()[i], Body: body})}); err != nil {
			return fmt.Errorf("grant %s: %w", option, err)
		}
		fmt.Printf("granted %s\n", option)
	}
	return nil
}
