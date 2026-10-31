package depot

import (
	"strings"
	"testing"
)

func TestMACInTable(t *testing.T) {
	linux := `IP address       HW type     Flags       HW address            Mask     Device
192.168.4.81     0x1         0x2         d8:bb:c1:4a:9e:10     *        enp5s0
192.168.4.1      0x1         0x2         3c:37:86:0f:aa:01     *        enp5s0
192.168.4.90     0x1         0x0         00:00:00:00:00:00     *        enp5s0
`
	if got := macInTable(strings.NewReader(linux), "192.168.4.81"); got != "d8:bb:c1:4a:9e:10" {
		t.Errorf("linux: %q", got)
	}
	if got := macInTable(strings.NewReader(linux), "192.168.4.8"); got != "" {
		t.Errorf("prefix of another address matched: %q", got)
	}
	if got := macInTable(strings.NewReader(linux), "192.168.4.90"); got != "" {
		t.Errorf("incomplete entry: %q", got)
	}
	mac := "? (192.168.4.81) at d8:bb:c1:4a:9e:10 on en0 ifscope [ethernet]\n"
	if got := macInTable(strings.NewReader(mac), "192.168.4.81"); got != "d8:bb:c1:4a:9e:10" {
		t.Errorf("macOS: %q", got)
	}
	win := "Interface: 192.168.4.54 --- 0xb\n  Internet Address      Physical Address      Type\n  192.168.4.81          d8-bb-c1-4a-9e-10     dynamic\n"
	if got := macInTable(strings.NewReader(win), "192.168.4.81"); got != "d8:bb:c1:4a:9e:10" {
		t.Errorf("windows: %q", got)
	}
}
