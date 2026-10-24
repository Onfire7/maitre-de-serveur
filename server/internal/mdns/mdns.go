// Package mdns advertises this machine on the local network through the
// host's Avahi daemon over D-Bus. Avahi owns UDP 5353 on Maître Server, so
// the agent must not run its own responder. In dev mode, when Avahi is not
// reachable, advertising is skipped and the caller logs that.
package mdns

import (
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/holoplot/go-avahi"
)

// Service types browsed by the Console.
const (
	TypeInstaller = "_maitre-installer._tcp"
	TypeNode      = "_maitre-node._tcp"
)

// Advertisement is one published DNS-SD service.
type Advertisement struct {
	conn  *dbus.Conn
	group *avahi.EntryGroup
}

// Publish announces instance `name` of `serviceType` on `port` with TXT
// records built from txt (key=value). It stays published until Close.
func Publish(name, serviceType string, port uint16, txt map[string]string) (*Advertisement, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, fmt.Errorf("mdns: system bus: %w", err)
	}
	server, err := avahi.ServerNew(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("mdns: avahi: %w", err)
	}
	group, err := server.EntryGroupNew()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("mdns: entry group: %w", err)
	}
	var records [][]byte
	for k, v := range txt {
		records = append(records, []byte(k+"="+v))
	}
	if err := group.AddService(avahi.InterfaceUnspec, avahi.ProtoUnspec, 0, name, serviceType, "local", "", port, records); err != nil {
		conn.Close()
		return nil, fmt.Errorf("mdns: add service: %w", err)
	}
	if err := group.Commit(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("mdns: commit: %w", err)
	}
	return &Advertisement{conn: conn, group: group}, nil
}

// Close withdraws the advertisement.
func (a *Advertisement) Close() error {
	if a == nil {
		return nil
	}
	_ = a.group.Reset()
	return a.conn.Close()
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
// machine's IPv4 addresses on the local network, e.g. "jellyfin.maitre.local".
// Returns a closer that withdraws them.
func PublishHosts(names []string, addrs []string) (*Advertisement, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, fmt.Errorf("mdns: system bus: %w", err)
	}
	server, err := avahi.ServerNew(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("mdns: avahi: %w", err)
	}
	group, err := server.EntryGroupNew()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("mdns: entry group: %w", err)
	}
	for _, n := range names {
		for _, a := range addrs {
			if err := group.AddAddress(avahi.InterfaceUnspec, avahi.ProtoUnspec, 0, n, a); err != nil {
				conn.Close()
				return nil, fmt.Errorf("mdns: add address %s: %w", n, err)
			}
		}
	}
	if len(names) > 0 {
		if err := group.Commit(); err != nil {
			conn.Close()
			return nil, fmt.Errorf("mdns: commit: %w", err)
		}
	}
	return &Advertisement{conn: conn, group: group}, nil
}
