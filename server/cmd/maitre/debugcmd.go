package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Onfire7/maitre-de-serveur/server/internal/debugconsole"
)

// runDebug is `maitre debug`: the Debug Console (decision 323), and with a
// command, the same console in a form a script can use.
//
// A connection is given once and remembered, as Maître Desktop remembers
// one: the first call needs the address and the Node's key, and every call
// after it needs nothing. Flags come before the command, as everywhere
// else in this CLI.
//
//	maitre debug -addr HOST:7443 -trust DIR     the first time, once
//	maitre debug                                the screen, that Node
//	maitre debug status                         the same facts, printed
//	maitre debug -node boulder space            another Node of the Fleet
//	maitre debug nodes                          what has been saved
//	maitre debug read /etc/containers/storage.conf
//	maitre debug run systemctl status jellyfin
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
	node := fs.String("node", "", "a saved Node, by name, id or address (default: the one used last)")
	addr := fs.String("addr", "", "Node address, for a Node not saved yet or a new way to one that is")
	idDir := fs.String("identity", "", "directory holding the caller's identity (default: the one this Node was reached with, else ~/.maitre/device, else Maître Desktop's)")
	trustDir := fs.String("trust", "", "directory of *.pub files, needed only for a Node not saved yet")
	fingerprint := fs.String("fingerprint", "", "the Node's id, instead of a -trust directory, for a first connection")
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

	// The two commands that are about saved connections rather than about
	// a Node, so they never touch the network.
	switch {
	case fs.Arg(0) == "nodes":
		return runDebugNodes(os.Stdout)
	case fs.Arg(0) == "forget":
		if fs.NArg() != 2 {
			return fmt.Errorf("usage: maitre debug forget <node>")
		}
		return runDebugForget(os.Stdout, fs.Arg(1))
	}

	// --demo needs no identity and no Node: the invented one is of
	// 2026-11-04, which is the state every page here was designed to make
	// legible.
	if *demo {
		return dispatch(demoDebugSource(), fs.Args(), opt, "demo")
	}

	// A command gets long enough for a prune or a store move, both of
	// which walk or copy the whole store.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()
	// Resolving says which Node it reached on stderr, so a -json command's
	// output stays the Node's answer and nothing else.
	src, info, addr2, err := resolve(ctx, want{
		node: *node, addr: *addr, identity: *idDir, trust: *trustDir, fingerprint: *fingerprint,
	}, os.Stderr)
	if err != nil {
		return err
	}
	where := info.GetName()
	if where == "" {
		where = addr2
	} else {
		where += " (" + addr2 + ")"
	}
	fmt.Fprintf(os.Stderr, "maitre debug: %s\n", where)
	return dispatch(src, fs.Args(), opt, where)
}

// dispatch runs the screen when no command was given, and the command
// otherwise. Both go through the same Source, so there is no page the
// screen can reach and a script cannot.
func dispatch(src debugconsole.Source, args []string, opt debugconsole.Options, addr string) error {
	if len(args) == 0 {
		return debugconsole.Run(context.Background(), src, addr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()
	return debugconsole.Command(ctx, src, os.Stdout, args, opt)
}
