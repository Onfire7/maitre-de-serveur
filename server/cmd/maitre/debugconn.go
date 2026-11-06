package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/debugconsole"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/nodeconn"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
)

// want is the connection a caller asked for on the command line. Every
// field may be empty, which is the point: the first connection is told
// everything and the ones after it are told nothing.
type want struct {
	node        string // a saved Node, by name, id prefix or address
	addr        string
	identity    string
	trust       string
	fingerprint string
}

// resolve turns what was asked for, plus what was saved, into a connection
// that answered, and saves what it learned.
//
// Decision 284's shape, in a CLI: every way to a Node is tried, the one
// that answered is promoted, and the ways that failed are kept behind it
// rather than dropped. The pin is a Node's own id, so once a Node is
// known, nothing but its name is needed again.
func resolve(ctx context.Context, w want, log io.Writer) (*debugconsole.Client, *maitrev1.NodeInfo, string, error) {
	store, err := nodeconn.Load(defaultState())
	if err != nil {
		return nil, nil, "", err
	}
	known := pick(store, w)

	idDir, err := identityDir(w.identity, known, log)
	if err != nil {
		return nil, nil, "", err
	}
	id, err := identity.Load(idDir)
	if err != nil {
		return nil, nil, "", fmt.Errorf("load identity from %s (run `maitre keygen` first): %w", idDir, err)
	}

	list := trust.New()
	// The Node's own id is the fingerprint of its key, so a saved Node
	// needs no trust directory at all.
	if known != nil && known.NodeID != "" {
		list.Add(known.NodeID)
	}
	if w.fingerprint != "" {
		list.Add(strings.ToLower(strings.TrimSpace(w.fingerprint)))
	}
	if w.trust != "" {
		if err := list.LoadDir(w.trust); err != nil {
			return nil, nil, "", err
		}
	}

	addrs := candidates(w.addr, known)
	if len(addrs) == 0 || list.Len() == 0 {
		return nil, nil, "", firstConnectionHelp(store, len(addrs) == 0)
	}

	client, info, addr, err := dial(ctx, NewHTTPClient(mtls.ClientConfig(id, list)), addrs)
	if err != nil {
		return nil, nil, "", err
	}
	remember(store, info, addr, idDir)
	if err := store.Save(); err != nil {
		fmt.Fprintf(log, "maitre debug: could not save the connection to %s: %v\n", store.Path(), err)
	}
	return client, info, addr, nil
}

// pick is the saved Node this command is about: the one named, the one
// that owns the address given, or the one used last.
func pick(store *nodeconn.Store, w want) *nodeconn.Known {
	if w.node != "" {
		return store.Find(w.node)
	}
	if w.addr != "" {
		// An address already known belongs to a Node already pinned, so a
		// caller who types a familiar address still needs no trust list.
		return store.Find(w.addr)
	}
	return store.Find("")
}

// candidates is every way to try, in order: the address asked for first,
// then the ways that worked before.
func candidates(addr string, known *nodeconn.Known) []string {
	var out []string
	if addr != "" {
		out = append(out, addr)
	}
	if known != nil {
		for _, a := range known.Addresses {
			if a != addr {
				out = append(out, a)
			}
		}
	}
	return out
}

// identityDir is the caller's own key: the one asked for, the one this
// Node was reached with before, the CLI's own, or Maître Desktop's.
func identityDir(asked string, known *nodeconn.Known, log io.Writer) (string, error) {
	if asked != "" {
		return asked, nil
	}
	if known != nil && known.Identity != "" && hasKey(known.Identity) {
		return known.Identity, nil
	}
	cli := filepath.Join(defaultState(), "device")
	if hasKey(cli) {
		return cli, nil
	}
	// This computer's Console is very often already a Trusted Device while
	// the CLI has never been given one. Borrowing it beats asking the user
	// to pair a second identity for the same person on the same machine —
	// said out loud, because a silently chosen key is the wrong kind of
	// convenience.
	if desktop := nodeconn.DesktopIdentityDir(); hasKey(desktop) {
		fmt.Fprintf(log, "maitre debug: using Maître Desktop's device identity at %s\n", desktop)
		return desktop, nil
	}
	return "", fmt.Errorf("no device identity: run `maitre keygen` to make one at %s, or pass -identity", cli)
}

func hasKey(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "identity.pub"))
	return err == nil
}

// dial tries each way in turn and returns the first that answers.
func dial(ctx context.Context, hc *http.Client, addrs []string) (*debugconsole.Client, *maitrev1.NodeInfo, string, error) {
	var errs []string
	for _, addr := range addrs {
		c := debugconsole.Dial(hc, addr)
		// GetNodeInfo is the core call every Node answers, and the answer
		// is what names the Node, pins it and lists its peers.
		probe, cancel := context.WithTimeout(ctx, 15*time.Second)
		info, err := c.Info(probe)
		cancel()
		if err == nil {
			return c, info, addr, nil
		}
		errs = append(errs, fmt.Sprintf("  %s: %v", addr, err))
	}
	return nil, nil, "", fmt.Errorf("no way to the Node answered:\n%s", strings.Join(errs, "\n"))
}

// remember saves this Node and, free with the same answer, every peer it
// named: a Node's id is its pin and PeerStatus carries the address each
// was last reached at, so one connection to one Node of a Fleet is enough
// to know them all — which is how the Desktop's own list is filled.
func remember(store *nodeconn.Store, info *maitrev1.NodeInfo, addr, idDir string) {
	now := time.Now().Unix()
	store.Remember(nodeconn.Known{
		NodeID: info.GetNodeId(), Name: info.GetName(),
		Addresses: []string{addr}, Identity: idDir, LastSeenUnix: now,
	}, true)
	for _, p := range info.GetPeers() {
		if p.GetNodeId() == "" || p.GetNodeId() == info.GetNodeId() {
			continue
		}
		var addrs []string
		if a := p.GetAddress(); a != "" {
			addrs = []string{a}
		}
		store.Remember(nodeconn.Known{
			NodeID: p.GetNodeId(), Name: p.GetName(), Addresses: addrs, Identity: idDir,
		}, false)
	}
}

// firstConnectionHelp says what a first connection needs, and what is
// already saved, because "no way to the Node" is useless on its own.
func firstConnectionHelp(store *nodeconn.Store, noAddr bool) error {
	var b strings.Builder
	if noAddr {
		b.WriteString("no saved Node and no -addr.\n")
	} else {
		b.WriteString("this Node is not saved yet, so its key is needed once.\n")
	}
	b.WriteString("\nThe first connection needs the address and the Node's key, either way round:\n")
	b.WriteString("    maitre debug -addr HOST:7443 -trust DIR\n")
	b.WriteString("    maitre debug -addr HOST:7443 -fingerprint NODE_ID\n")
	b.WriteString("\nAfter that `maitre debug` remembers it, and its peers, in " + store.Path() + ".\n")
	if len(store.Nodes) == 0 {
		b.WriteString("\nNothing is saved yet.")
		return fmt.Errorf("%s", b.String())
	}
	b.WriteString("\nSaved Nodes, for -node:\n")
	for i := range store.Nodes {
		b.WriteString("    " + nodeLine(&store.Nodes[i], store.Last) + "\n")
	}
	return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
}

// runDebugNodes is `maitre debug nodes`: what has been saved, without
// touching the network.
func runDebugNodes(out io.Writer) error {
	store, err := nodeconn.Load(defaultState())
	if err != nil {
		return err
	}
	if len(store.Nodes) == 0 {
		fmt.Fprintf(out, "No Nodes saved yet in %s.\nConnect once with -addr and -trust (or -fingerprint) and they will be.\n", store.Path())
		return nil
	}
	fmt.Fprintf(out, "%s\n", store.Path())
	for i := range store.Nodes {
		fmt.Fprintf(out, "%s\n", nodeLine(&store.Nodes[i], store.Last))
	}
	return nil
}

func nodeLine(k *nodeconn.Known, last string) string {
	mark := "  "
	if k.Key() == last {
		mark = "* "
	}
	where := "no address yet"
	if len(k.Addresses) > 0 {
		where = strings.Join(k.Addresses, ", ")
	}
	seen := ""
	if t := k.LastSeen(); !t.IsZero() {
		seen = fmt.Sprintf("  (last answered %s ago)", time.Since(t).Round(time.Minute))
	}
	return fmt.Sprintf("%s%-16s %-13s %s%s", mark, k.Key(), k.Short(), where, seen)
}

// runDebugForget is `maitre debug forget <node>`.
func runDebugForget(out io.Writer, q string) error {
	store, err := nodeconn.Load(defaultState())
	if err != nil {
		return err
	}
	if !store.Forget(q) {
		return fmt.Errorf("no saved Node matches %q; `maitre debug nodes` lists them", q)
	}
	if err := store.Save(); err != nil {
		return err
	}
	fmt.Fprintf(out, "forgot %s\n", q)
	return nil
}
