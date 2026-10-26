package portmap

import (
	"context"
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
