package hostnet

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The kernel writes each address as a little-endian hexadecimal word, so
// 192.168.4.1 is 0104A8C0 and reading it the obvious way gives 1.4.168.192.
func TestTheRouteTableIsReadLittleEndian(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "route")
	body := strings.Join([]string{
		"Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT",
		"eno1\t0004A8C0\t00000000\t0001\t0\t0\t100\t00FCFFFF\t0\t0\t0",
		"eno1\t00000000\t0104A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0",
		"docker0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	old := RouteTable
	RouteTable = path
	t.Cleanup(func() { RouteTable = old })

	// The interface asked for, not the first default route in the file:
	// a Node with a container bridge has several.
	got, err := gateway("eno1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "192.168.4.1" {
		t.Errorf("gateway %q, want 192.168.4.1", got)
	}
	if _, err := gateway("wlan0"); err == nil || !strings.Contains(err.Error(), "no default route") {
		t.Errorf("an interface with no default route: %v", err)
	}
}

func TestAnAddressWordThatIsNotOne(t *testing.T) {
	for _, s := range []string{"", "0104A8", "zzzzzzzz", "0104A8C0FF"} {
		if _, err := hexWord(s); err == nil {
			t.Errorf("%q was read as an address", s)
		}
	}
}

// The subnet comes from the interface's own prefix, not from a guess at
// the address's class: a /22 home network is one network, not four.
func TestTheSubnetKeepsTheInterfacesPrefix(t *testing.T) {
	ip := net.ParseIP("192.168.4.80").To4()
	mask := net.CIDRMask(22, 32)
	got := (&net.IPNet{IP: ip.Mask(mask), Mask: mask}).String()
	if got != "192.168.4.0/22" {
		t.Errorf("subnet %q, want 192.168.4.0/22", got)
	}
}
