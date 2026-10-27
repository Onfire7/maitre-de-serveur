package installer

import "testing"

func TestVirtualInterfaces(t *testing.T) {
	for _, n := range []string{"podman0", "podman1", "veth1a2b", "cni-podman0", "docker0", "br-3f2a", "virbr0", "vnet3"} {
		if !virtualInterface(n) {
			t.Errorf("%s is a container or VM network", n)
		}
	}
	for _, n := range []string{"enp3s0", "eth0", "wlp4s0", "eno1", "br0", "bond0", "tailscale0"} {
		if virtualInterface(n) {
			t.Errorf("%s may carry the LAN", n)
		}
	}
}
