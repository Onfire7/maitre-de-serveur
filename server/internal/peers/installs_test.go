package peers

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/installer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	caller "github.com/Onfire7/maitre-de-serveur/server/internal/peer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
)

// startInstaller serves an Installer with key id; returns its host:port.
func startInstaller(t *testing.T, id *identity.Identity) string {
	in := installer.New(id, installer.Options{Runner: &installer.FakeRunner{}, Disks: installer.FakeDisks})
	rpc := connect.NewServer()
	maitrev1connect.RegisterInstallerServiceHandler(rpc, in)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, rpc)
	srv := httptest.NewUnstartedServer(caller.Middleware(mux))
	srv.TLS = mtls.InstallerServerConfig(id)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "https://")
}

// A Node whose record arrived but that never gossiped is asked through its
// Installer how far the install got; once it talks, that is forgotten.
func TestWatchInstallsAsksTheInstaller(t *testing.T) {
	newID, _ := identity.Generate()
	addr := startInstaller(t, newID)

	self, _ := identity.Generate()
	list := trust.New()
	list.Add(newID.Fingerprint())
	m := New(nil, self, list, slog.Default())
	m.peers[newID.Fingerprint()] = &peer{id: newID.Fingerprint(), name: "Cedar", addrs: []string{addr}, provisioned: time.Now()}
	// A Node installed long ago and silent now is Unreachable, not
	// Provisioning: it is not asked.
	m.peers["old"] = &peer{id: "old", name: "Old", addrs: []string{addr}, provisioned: time.Now().Add(-2 * InstallWindow)}

	m.pollInstalls(context.Background())
	if got := m.Installing(newID.Fingerprint()); got.GetPhase() != maitrev1.Provisioning_PHASE_IDLE {
		t.Fatalf("installing = %v, want the Installer's idle state", got)
	}
	if m.Installing("old") != nil {
		t.Fatal("a Node outside the install window is not installing")
	}

	m.Received(&maitrev1.FleetNodeStatus{NodeId: newID.Fingerprint()})
	m.pollInstalls(context.Background())
	if m.Installing(newID.Fingerprint()) != nil {
		t.Fatal("a Node that gossiped is no longer installing")
	}
}

// An address answering with another Installer's key is not believed.
func TestWatchInstallsChecksTheKey(t *testing.T) {
	otherID, _ := identity.Generate()
	addr := startInstaller(t, otherID)

	self, _ := identity.Generate()
	list := trust.New()
	list.Add("cedar")
	m := New(nil, self, list, slog.Default())
	m.peers["cedar"] = &peer{id: "cedar", name: "Cedar", addrs: []string{addr}, provisioned: time.Now()}
	m.pollInstalls(context.Background())
	if m.Installing("cedar") != nil {
		t.Fatal("an untrusted key must not report for Cedar")
	}
}
