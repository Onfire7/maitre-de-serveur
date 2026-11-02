package tailnet

import (
	"reflect"
	"testing"
)

func TestIsTailnetAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"100.64.0.5:7443":               true,
		"100.127.255.1:7443":            true,
		"100.128.0.1:7443":              false,
		"[fd7a:115c:a1e0::1]:7443":      true,
		"alder.home-fleet.ts.net.:7443": true,
		"alder.home-fleet.ts.net":       true,
		"192.168.4.74:7443":             false,
		"alder.local:7443":              false,
		"":                              false,
	} {
		if got := IsTailnetAddr(addr); got != want {
			t.Errorf("IsTailnetAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestKeepTailnet(t *testing.T) {
	prev := []string{"192.168.4.70:7443", "100.64.0.5:7443", "[fd7a:115c:a1e0::1]:7443"}
	got := KeepTailnet([]string{"192.168.4.74:7443"}, prev)
	want := []string{"192.168.4.74:7443", "100.64.0.5:7443", "[fd7a:115c:a1e0::1]:7443"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := KeepTailnet([]string{"10.0.0.1:7443"}, nil); !reflect.DeepEqual(got, []string{"10.0.0.1:7443"}) {
		t.Errorf("no prev: got %v", got)
	}
}
