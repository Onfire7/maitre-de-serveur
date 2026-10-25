package services

import (
	"context"
	"fmt"
	"strings"
)

// PinAddress makes the Node's current IPv4 configuration static through
// NetworkManager (Milestone 3): an Infrastructure Service such as DNS or
// DHCP must keep the address other devices were told. Current address,
// gateway and DNS servers are copied as they are; nothing changes on the
// wire, only the lease stops mattering. Returns what was pinned.
func PinAddress(ctx context.Context, run Runner) (string, error) {
	out, err := run.Run(ctx, "nmcli", "-t", "-f", "NAME,TYPE,DEVICE", "connection", "show", "--active")
	if err != nil {
		return "", fmt.Errorf("list connections: %w", err)
	}
	var conn, dev string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, ":")
		if len(f) >= 3 && (f[1] == "802-3-ethernet" || f[1] == "ethernet" || f[1] == "bridge") && f[2] != "" {
			conn, dev = f[0], f[2]
			break
		}
	}
	if conn == "" {
		return "", fmt.Errorf("no active wired connection to pin")
	}
	out, err = run.Run(ctx, "nmcli", "-t", "-g", "IP4.ADDRESS,IP4.GATEWAY,IP4.DNS", "device", "show", dev)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", dev, err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 || lines[0] == "" {
		return "", fmt.Errorf("%s has no IPv4 address to pin", dev)
	}
	addr := strings.Split(lines[0], "|")[0] // first address if several
	gw := ""
	if len(lines) > 1 {
		gw = lines[1]
	}
	dns := ""
	if len(lines) > 2 {
		var servers []string
		for _, d := range strings.Split(lines[2], "|") {
			if d = strings.TrimSpace(d); d != "" {
				servers = append(servers, d)
			}
		}
		dns = strings.Join(servers, ",")
	}
	args := []string{"connection", "modify", conn, "ipv4.method", "manual", "ipv4.addresses", addr}
	if gw != "" {
		args = append(args, "ipv4.gateway", gw)
	}
	if dns != "" {
		args = append(args, "ipv4.dns", dns)
	}
	if _, err := run.Run(ctx, "nmcli", args...); err != nil {
		return "", fmt.Errorf("pin %s on %s: %w", addr, conn, err)
	}
	return addr, nil
}
