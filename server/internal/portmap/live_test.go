package portmap

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

// MAITRE_UPNP_PROBE=1 go test ./internal/portmap -run Live -v: says whether
// a UPnP router answers on this LAN. Never runs by default.
func TestDiscoverLive(t *testing.T) {
	if os.Getenv("MAITRE_UPNP_PROBE") == "" {
		t.Skip("set MAITRE_UPNP_PROBE=1 to look for a router")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := discoverUPnP(ctx)
	if err != nil {
		t.Fatalf("no router: %v", err)
	}
	t.Logf("router: %s", r.Name())
}

// MAITRE_UPNP_PROBE=1 also checks Delete against the real router with a
// throwaway port: a forward to another address is left, our own is
// removed, and one already gone is no error.
func TestDeleteLive(t *testing.T) {
	if os.Getenv("MAITRE_UPNP_PROBE") == "" {
		t.Skip("set MAITRE_UPNP_PROBE=1 to look for a router")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := discoverUPnP(ctx)
	if err != nil {
		t.Fatalf("no router: %v", err)
	}
	ip, err := lanIP()
	if err != nil {
		t.Fatal(err)
	}
	m := Mapping{Port: 47913, Proto: "tcp", Owner: "probe"}
	if err := r.Add(ctx, m, ip, 5*time.Minute); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := r.Delete(ctx, m, "192.0.2.1"); !errors.Is(err, ErrNotOurs) {
		r.Delete(ctx, m, ip)
		t.Fatalf("delete for another address: %v, want ErrNotOurs", err)
	}
	if err := r.Delete(ctx, m, ip); err != nil {
		t.Fatalf("delete our own: %v", err)
	}
	if err := r.Delete(ctx, m, ip); err != nil {
		t.Fatalf("delete one already gone: %v", err)
	}
}

func lanIP() (string, error) {
	c, err := net.Dial("udp", "192.0.2.1:9")
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String(), nil
}
