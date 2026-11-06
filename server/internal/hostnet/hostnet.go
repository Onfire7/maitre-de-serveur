// Package hostnet reads the Node's own place on the home network: the
// interface it reaches the LAN through, that network, and its gateway.
// A Service given an address of its own on the home network (decision
// 327) needs all three, because the macvlan it joins is created over that
// interface and takes its subnet and gateway from the same network the
// Node is on.
package hostnet

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strings"
)

// RouteTable is the kernel's IPv4 route table; a test points it elsewhere.
var RouteTable = "/proc/net/route"

// LAN is what the Node knows about the home network it is on.
type LAN struct {
	// Iface is the interface name, the macvlan's parent.
	Iface string
	// Address is the Node's own address on it.
	Address string
	// Subnet is the network in CIDR form, e.g. 192.168.4.0/24.
	Subnet string
	// Gateway is the router.
	Gateway string
}

// Read works out the Node's place on the home network. The address comes
// from the source address the kernel would use toward the LAN rather than
// from a guess at interface names: a Node may have several interfaces up,
// and the one that carries its traffic is the one a Service should sit
// beside. No packet is sent.
func Read() (LAN, error) {
	ip, err := localAddress()
	if err != nil {
		return LAN{}, err
	}
	ifc, ipnet, err := interfaceOf(ip)
	if err != nil {
		return LAN{}, err
	}
	// A macvlan child cannot be created over a bridge, a bond's member or
	// another macvlan, and on wireless it is dropped by the access point.
	// Better to say so here than to leave a Service failing to start with
	// netavark's own words.
	if err := usableParent(ifc); err != nil {
		return LAN{}, err
	}
	gw, err := gateway(ifc.Name)
	if err != nil {
		return LAN{}, err
	}
	return LAN{
		Iface:   ifc.Name,
		Address: ip.String(),
		Subnet:  (&net.IPNet{IP: ip.Mask(ipnet.Mask), Mask: ipnet.Mask}).String(),
		Gateway: gw,
	}, nil
}

// localAddress is the IPv4 address the Node would use to reach the local
// network, the same way depot.LANAddress finds it.
func localAddress() (net.IP, error) {
	conn, err := net.Dial("udp4", "192.0.2.1:9") // TEST-NET-1, never routed
	if err != nil {
		return nil, fmt.Errorf("this Node has no address on the home network: %w", err)
	}
	defer conn.Close()
	ip := conn.LocalAddr().(*net.UDPAddr).IP.To4()
	if ip == nil {
		return nil, fmt.Errorf("this Node has no IPv4 address on the home network")
	}
	return ip, nil
}

// interfaceOf finds the interface holding an address, and the address's
// own network, which is where its prefix length comes from: the dial
// above reports the address alone.
func interfaceOf(ip net.IP) (*net.Interface, *net.IPNet, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, err
	}
	for i := range ifaces {
		addrs, err := ifaces[i].Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if ok && ipn.IP.To4() != nil && ipn.IP.To4().Equal(ip) {
				return &ifaces[i], ipn, nil
			}
		}
	}
	return nil, nil, fmt.Errorf("no interface of this Node holds %s", ip)
}

// usableParent refuses an interface a macvlan cannot be built over.
func usableParent(ifc *net.Interface) error {
	if _, err := os.Stat("/sys/class/net/" + ifc.Name + "/wireless"); err == nil {
		return fmt.Errorf("%s is a wireless interface: an access point drops the frames of a second MAC address, so a Service cannot have an address of its own on the home network through it", ifc.Name)
	}
	if _, err := os.Stat("/sys/class/net/" + ifc.Name + "/bridge"); err == nil {
		return fmt.Errorf("%s is a bridge, which cannot be a macvlan's parent", ifc.Name)
	}
	return nil
}

// gateway reads the default route of an interface out of the kernel's
// table. The fields are the interface name, then the destination, then
// the gateway, each a little-endian hexadecimal word.
func gateway(iface string) (string, error) {
	b, err := os.ReadFile(RouteTable)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 3 || f[0] != iface || f[1] != "00000000" {
			continue
		}
		ip, err := hexWord(f[2])
		if err != nil {
			return "", err
		}
		return ip.String(), nil
	}
	return "", fmt.Errorf("%s has no default route, so the home network's gateway is not known", iface)
}

// hexWord decodes one of the route table's little-endian address words.
func hexWord(s string) (net.IP, error) {
	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) != 4 {
		return nil, fmt.Errorf("route table: %q is not an address", s)
	}
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], binary.LittleEndian.Uint32(raw))
	return net.IPv4(b[0], b[1], b[2], b[3]).To4(), nil
}
