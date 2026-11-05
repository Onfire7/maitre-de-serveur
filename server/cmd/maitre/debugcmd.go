package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
)

// runDebug is `maitre debug`: decision 326's middle tier, the diagnostics
// that only an image built for them will answer.
//
//	maitre debug --addr HOST:7443 read /etc/containers/storage.conf
//	maitre debug --addr HOST:7443 run systemctl status jellyfin
//
// A Node built without the debug tag answers "not a debug build" — which
// is the whole design: the procedures are absent from a shipped binary
// rather than guarded inside one, so there is nothing to switch on.
func runDebug(args []string) error {
	fs := flag.NewFlagSet("debug", flag.ExitOnError)
	idDir := fs.String("identity", filepath.Join(defaultState(), "device"), "directory holding the caller's identity")
	trustDir := fs.String("trust", filepath.Join(defaultState(), "device", "trusted"), "directory of *.pub files for Nodes to trust")
	addr := fs.String("addr", "127.0.0.1:7443", "Node address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("usage: maitre debug [flags first] <read <path>|run <command> [args...]>")
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
	c := maitrev1connect.NewNodeServiceClient(connect.NewClient(transport))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	switch rest[0] {
	case "read":
		if len(rest) != 2 {
			return fmt.Errorf("usage: maitre debug read <path>")
		}
		res, err := c.DebugReadFile(ctx, &maitrev1.DebugReadFileRequest{Path: rest[1]})
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(res.GetContent())
		return err
	case "run":
		if len(rest) < 2 {
			return fmt.Errorf("usage: maitre debug run <command> [args...]")
		}
		res, err := c.DebugExec(ctx, &maitrev1.DebugExecRequest{Command: rest[1], Args: rest[2:]})
		// The output is the diagnosis even when the command failed, so it
		// is printed either way.
		if out := res.GetOutput(); out != "" {
			fmt.Print(out)
		}
		return err
	}
	return fmt.Errorf("usage: maitre debug <read|run>")
}
