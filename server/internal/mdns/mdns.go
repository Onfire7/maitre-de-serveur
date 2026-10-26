// Package mdns advertises this machine on the local network through the
// host's Avahi daemon over D-Bus. Avahi owns UDP 5353 on Maître Server, so
// the agent must not run its own responder. In dev mode, when Avahi is not
// reachable, advertising is skipped and the caller logs that.
package mdns

import (
	"context"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/holoplot/go-avahi"
)

// Service types browsed by the Console.
const (
	TypeInstaller = "_maitre-installer._tcp"
	TypeNode      = "_maitre-node._tcp"
	TypeDepot     = "_maitre-depot._tcp"
)

// Advertisement is one published DNS-SD service.
type Advertisement struct {
	conn   *dbus.Conn
	server *avahi.Server
	group  *avahi.EntryGroup
}

// connect opens a private connection to the system bus and an Avahi client
// on it. Every publisher and browser gets its own: godbus's shared
// SystemBus() connection is closed for the whole process by the first
// Close, which closes every Avahi client's signal channel at once, and
// go-avahi's dispatch loop then spins on the closed channel forever (one
// core per client; this put a Node at 75% CPU with no Services running).
func connect() (*dbus.Conn, *avahi.Server, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, nil, fmt.Errorf("mdns: system bus: %w", err)
	}
	server, err := avahi.ServerNew(conn)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("mdns: avahi: %w", err)
	}
	return conn, server, nil
}

// closeAll stops the Avahi client's dispatch loop first, then the
// connection, in that order: the loop must not see its channel close.
func closeAll(conn *dbus.Conn, server *avahi.Server) error {
	server.Close()
	return conn.Close()
}

// Publish announces instance `name` of `serviceType` on `port` with TXT
// records built from txt (key=value). It stays published until Close.
func Publish(name, serviceType string, port uint16, txt map[string]string) (*Advertisement, error) {
	conn, server, err := connect()
	if err != nil {
		return nil, err
	}
	group, err := server.EntryGroupNew()
	if err != nil {
		closeAll(conn, server)
		return nil, fmt.Errorf("mdns: entry group: %w", err)
	}
	var records [][]byte
	for k, v := range txt {
		records = append(records, []byte(k+"="+v))
	}
	if err := group.AddService(avahi.InterfaceUnspec, avahi.ProtoUnspec, 0, name, serviceType, "local", "", port, records); err != nil {
		closeAll(conn, server)
		return nil, fmt.Errorf("mdns: add service: %w", err)
	}
	if err := group.Commit(); err != nil {
		closeAll(conn, server)
		return nil, fmt.Errorf("mdns: commit: %w", err)
	}
	return &Advertisement{conn: conn, server: server, group: group}, nil
}

// Close withdraws the advertisement and releases its Avahi client.
func (a *Advertisement) Close() error {
	if a == nil {
		return nil
	}
	_ = a.group.Reset()
	return closeAll(a.conn, a.server)
}

// InstanceName makes a readable, unique mDNS instance name from a human
// label and a key fingerprint, e.g. "Maître Installer (f8eeab5e)".
func InstanceName(label, fingerprint string) string {
	short := fingerprint
	if len(short) > 8 {
		short = short[:8]
	}
	return strings.TrimSpace(label) + " (" + short + ")"
}

// PublishHosts publishes address records so <name> resolves to this
// machine's IPv4 addresses on the local network, e.g. "jellyfin.local".
// No reverse (PTR) records: the machine's own hostname owns those, and
// Avahi reports a local name collision otherwise. Returns a closer that
// withdraws them.
func PublishHosts(names []string, addrs []string) (*Advertisement, error) {
	conn, server, err := connect()
	if err != nil {
		return nil, err
	}
	group, err := server.EntryGroupNew()
	if err != nil {
		closeAll(conn, server)
		return nil, fmt.Errorf("mdns: entry group: %w", err)
	}
	for _, n := range names {
		for _, a := range addrs {
			if err := group.AddAddress(avahi.InterfaceUnspec, avahi.ProtoUnspec, avahi.PublishNoReverse, n, a); err != nil {
				closeAll(conn, server)
				return nil, fmt.Errorf("mdns: add address %s: %w", n, err)
			}
		}
	}
	if len(names) > 0 {
		if err := group.Commit(); err != nil {
			closeAll(conn, server)
			return nil, fmt.Errorf("mdns: commit: %w", err)
		}
	}
	return &Advertisement{conn: conn, server: server, group: group}, nil
}

// SetHostName tells Avahi to announce under name (without ".local") from
// now on. Avahi reads the system host name only at startup, so a Node that
// renames itself after boot must say so, or it keeps answering for the old
// name, "fedora.local" on every fresh install.
func SetHostName(name string) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("mdns: system bus: %w", err)
	}
	defer conn.Close()
	obj := conn.Object("org.freedesktop.Avahi", "/")
	var cur string
	if err := obj.Call("org.freedesktop.Avahi.Server.GetHostName", 0).Store(&cur); err == nil && cur == name {
		return nil
	}
	if err := obj.Call("org.freedesktop.Avahi.Server.SetHostName", 0, name).Err; err != nil {
		return fmt.Errorf("mdns: set host name: %w", err)
	}
	return nil
}

// Browse watches for services of serviceType and calls found with each
// resolved instance's TXT id and fleet, address and port, until ctx ends.
// Used by Nodes to find their peers.
func Browse(ctx context.Context, serviceType string, found func(id, fleet, addr string, port uint16)) error {
	conn, server, err := connect()
	if err != nil {
		return err
	}
	sb, err := server.ServiceBrowserNew(avahi.InterfaceUnspec, avahi.ProtoInet, serviceType, "local", 0)
	if err != nil {
		closeAll(conn, server)
		return fmt.Errorf("mdns: browse: %w", err)
	}
	go func() {
		defer closeAll(conn, server)
		for {
			select {
			case <-ctx.Done():
				return
			case svc, ok := <-sb.AddChannel:
				if !ok {
					return
				}
				r, err := server.ResolveService(svc.Interface, svc.Protocol, svc.Name, svc.Type, svc.Domain, avahi.ProtoInet, 0)
				if err != nil {
					continue
				}
				txt := map[string]string{}
				for _, kv := range r.Txt {
					if k, v, ok := strings.Cut(string(kv), "="); ok {
						txt[k] = v
					}
				}
				if txt["id"] != "" {
					found(txt["id"], txt["fleet"], r.Address, r.Port)
				}
			case _, ok := <-sb.RemoveChannel:
				if !ok {
					return
				}
			}
		}
	}()
	return nil
}
