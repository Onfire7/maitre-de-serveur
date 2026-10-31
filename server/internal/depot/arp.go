package depot

import (
	"bufio"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
)

// MACFor is the hardware address the neighbour table holds for ip, "" when
// it holds none. A machine that has just fetched a boot file from this
// computer is in the table (decision 270). Linux reads /proc/net/arp; the
// others ask arp(8).
func MACFor(ip string) string {
	if f, err := os.Open("/proc/net/arp"); err == nil {
		defer f.Close()
		return macInTable(f, ip)
	}
	out, err := exec.Command("arp", "-n", ip).Output()
	if err != nil {
		return ""
	}
	return macInTable(strings.NewReader(string(out)), ip)
}

// macInTable finds ip's line in a neighbour table listing and returns the
// hardware address on it: the first field that parses as one and is not
// all zeros (an incomplete entry).
func macInTable(r io.Reader, ip string) string {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		onLine := false
		for _, f := range fields {
			if strings.Trim(f, "()") == ip {
				onLine = true
			}
		}
		if !onLine {
			continue
		}
		for _, f := range fields {
			hw, err := net.ParseMAC(f)
			if err != nil || len(hw) != 6 || hw.String() == "00:00:00:00:00:00" {
				continue
			}
			return hw.String()
		}
	}
	return ""
}
