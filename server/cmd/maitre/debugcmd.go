package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Onfire7/maitre-de-serveur/server/internal/debugconsole"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
)

// runDebug is `maitre debug`: the Debug Console (decision 323), and with a
// command, the same console in a form a script can use.
//
// Flags come before the command, as everywhere else in this CLI:
//
//	maitre debug --addr HOST:7443                     the screen
//	maitre debug --addr HOST:7443 status              the same facts, printed
//	maitre debug --addr HOST:7443 --json space
//	maitre debug --addr HOST:7443 read /etc/containers/storage.conf
//	maitre debug --addr HOST:7443 run systemctl status jellyfin
//
// Decision 326 put these in two tiers, and the Console shows both and says
// which is which. `status`, `space`, `services`, `logs` and `prune` are the
// basic tier, answered by every shipped image for a Trusted Device.
// `read` and `run` are the debug tier: a Node built without the tag
// answers "not a debug build", which is the whole design — the procedures
// are absent from a shipped binary rather than guarded inside one, so
// there is nothing to switch on.
func runDebug(args []string) error {
	fs := flag.NewFlagSet("debug", flag.ExitOnError)
	idDir := fs.String("identity", filepath.Join(defaultState(), "device"), "directory holding the caller's identity")
	trustDir := fs.String("trust", filepath.Join(defaultState(), "device", "trusted"), "directory of *.pub files for Nodes to trust")
	addr := fs.String("addr", "127.0.0.1:7443", "Node address")
	lines := fs.Int("lines", 300, "logs: how many lines, at most 5000")
	asJSON := fs.Bool("json", false, "print the Node's answer as protobuf JSON instead of a table")
	dropRollback := fs.Bool("drop-rollback", false, "prune: also delete each Service's previous_image, its Rollback target (decision 320)")
	storeDir := fs.String("store-dir", "", "move-store: where to put it; empty lets the Node choose (decision 319)")
	demo := fs.Bool("demo", false, "run against made-up data, to look at the Console without a Node")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, debugconsole.Usage)
		fmt.Fprintln(os.Stderr)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	opt := debugconsole.Options{
		Lines:        *lines,
		JSON:         *asJSON,
		DropRollback: *dropRollback,
		StoreDir:     *storeDir,
	}
	// --demo needs no identity and no Node: the invented one is of
	// 2026-11-04, which is the state every page here was designed to make
	// legible.
	if *demo {
		return dispatch(demoDebugSource(), fs.Args(), opt, "demo")
	}
	id, err := identity.Load(*idDir)
	if err != nil {
		return fmt.Errorf("load identity (run `maitre keygen` first): %w", err)
	}
	list := trust.New()
	if err := list.LoadDir(*trustDir); err != nil {
		return err
	}
	return dispatch(debugconsole.Dial(NewHTTPClient(mtls.ClientConfig(id, list)), *addr), fs.Args(), opt, *addr)
}

// dispatch runs the screen when no command was given, and the command
// otherwise. Both go through the same Source, so there is no page the
// screen can reach and a script cannot.
func dispatch(src debugconsole.Source, args []string, opt debugconsole.Options, addr string) error {
	if len(args) == 0 {
		return debugconsole.Run(context.Background(), src, addr)
	}
	// A command gets long enough for a prune or a store move, both of
	// which walk or copy the whole store.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()
	return debugconsole.Command(ctx, src, os.Stdout, args, opt)
}
