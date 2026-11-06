package direct

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

func TestValidHost(t *testing.T) {
	for _, ok := range []string{"photos.example.com", "a.bc", "x-y.example.co.uk"} {
		if !ValidHost(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "localhost", "Photos.Example.com", "photos.example.com.", "-a.example.com", "a..b", "a.b/c", "jellyfin.local"} {
		if ValidHost(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

// From home a browser goes to the public address, and only a router that
// loops it back in gets it to the web entry Node (decision 329).
func TestCheckHome(t *testing.T) {
	m := New(slog.Default())
	var asked string
	loops := true
	m.dial = func(_ context.Context, addr, host string) error {
		asked = addr + " " + host
		if !loops {
			return errors.New("connection refused")
		}
		return nil
	}
	if got := m.checkHome(context.Background(), "stash.example.com", "203.0.113.7"); got != "up" || asked != "203.0.113.7:443 stash.example.com" {
		t.Fatalf("%q after dialling %q", got, asked)
	}
	loops = false
	if got := m.checkHome(context.Background(), "stash.example.com", "203.0.113.7"); got != "not from home: the router does not send 203.0.113.7 back in" {
		t.Fatalf("no loopback: %q", got)
	}
	if got := m.checkHome(context.Background(), "stash.example.com", ""); got != "" {
		t.Fatalf("no public address yet: %q", got)
	}
}
