package installer_test

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"golang.org/x/net/http2"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/installer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/pairing"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
)

func startInstaller(t *testing.T, in *installer.Installer, id *identity.Identity) *httptest.Server {
	rpc := connect.NewServer()
	maitrev1connect.RegisterInstallerServiceHandler(rpc, in)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, rpc)
	srv := httptest.NewUnstartedServer(peer.Middleware(mux))
	srv.TLS = mtls.InstallerServerConfig(id)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func client(t *testing.T, srv *httptest.Server, device, installerID *identity.Identity) maitrev1connect.InstallerServiceClient {
	list := trust.New()
	list.Add(installerID.Fingerprint())
	cfg := mtls.ClientConfig(device, list)
	hc := &http.Client{Transport: &http2.Transport{
		TLSClientConfig: cfg,
		DialTLSContext: func(ctx context.Context, network, addr string, c *tls.Config) (net.Conn, error) {
			return (&tls.Dialer{Config: c}).DialContext(ctx, network, addr)
		},
	}}
	return maitrev1connect.NewInstallerServiceClient(connect.NewClient(connecthttp.NewTransport(hc, srv.URL)))
}

func TestPairProvisionAndSeed(t *testing.T) {
	ctx := context.Background()
	instID, _ := identity.Generate()
	device, _ := identity.Generate()
	stranger, _ := identity.Generate()
	cfgDir := filepath.Join(t.TempDir(), "config")
	rebooted := make(chan struct{})
	in := installer.New(instID, installer.Options{
		Version:  "test",
		ImageRef: "example/maitre:test",
		Runner:   &installer.FakeRunner{Dir: cfgDir},
		Disks:    installer.FakeDisks,
		Folders:  installer.FakeFolders{},
		Reboot:   func() error { close(rebooted); return nil },
	})
	srv := startInstaller(t, in, instID)
	c := client(t, srv, device, instID)

	st, err := c.GetInstallerStatus(ctx, &maitrev1.GetInstallerStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if want := pairing.Code(instID.PublicKey(), device.PublicKey()); st.GetStatus().GetPairingCode() != want {
		t.Fatalf("pairing code %q, want %q", st.GetStatus().GetPairingCode(), want)
	}
	if st.GetStatus().GetInstallerId() != instID.Fingerprint() || len(st.GetStatus().GetDisks()) != 3 {
		t.Fatalf("unexpected status %v", st.GetStatus())
	}
	// Decision 262: a disk can be looked inside before it is chosen.
	if r, err := c.ListFolder(ctx, &maitrev1.ListFolderRequest{Device: "/dev/fake-ssd3", Path: "Users/sam"}); err != nil || len(r.GetEntries()) != 4 || !r.GetEntries()[0].GetDir() {
		t.Fatalf("ListFolder: %v %v", r, err)
	}
	if !contains(in.Screen(), st.GetStatus().GetPairingCode()) {
		t.Fatalf("screen does not show the code:\n%s", in.Screen())
	}

	fleet := &maitrev1.Fleet{FleetId: "fleet-1", Name: "Home"}
	devices := []*maitrev1.TrustedDevice{{Name: "Laptop", PublicKeyPem: string(device.PublicKeyPEM())}}

	// Refused: target is the installer media.
	_, err = c.Provision(ctx, &maitrev1.ProvisionRequest{Fleet: fleet, NodeName: "n1", TargetDisk: "/dev/fake-usb", TrustedDevices: devices})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("install onto installer media: %v", err)
	}
	// Refused: the caller would lock itself out.
	_, err = c.Provision(ctx, &maitrev1.ProvisionRequest{Fleet: fleet, NodeName: "n1", TargetDisk: "/dev/fake-ssd",
		TrustedDevices: []*maitrev1.TrustedDevice{{Name: "Other", PublicKeyPem: string(stranger.PublicKeyPEM())}}})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("caller not in trust list: %v", err)
	}

	if _, err := c.Provision(ctx, &maitrev1.ProvisionRequest{Fleet: fleet, NodeName: "n1", TargetDisk: "/dev/fake-ssd", TrustedDevices: devices}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-in.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("provisioning did not finish")
	}
	st, _ = c.GetInstallerStatus(ctx, &maitrev1.GetInstallerStatusRequest{})
	if st.GetStatus().GetProvisioning().GetPhase() != maitrev1.Provisioning_PHASE_REBOOTING {
		t.Fatalf("phase %v", st.GetStatus().GetProvisioning())
	}
	select {
	case <-rebooted:
	case <-time.After(10 * time.Second):
		t.Fatal("no reboot")
	}

	// The seeded config partition is what `maitre agent` loads.
	nodeID, err := identity.Load(filepath.Join(cfgDir, "node"))
	if err != nil || nodeID.Fingerprint() != instID.Fingerprint() {
		t.Fatalf("node identity: %v", err)
	}
	db, err := store.Open(filepath.Join(cfgDir, "fleet.db"), nodeID.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var f maitrev1.Fleet
	if _, err := db.Get(ctx, store.KindFleet, "fleet-1", &f); err != nil || f.Name != "Home" || f.CreatedUnixMs == 0 {
		t.Fatalf("fleet record: %v %v", err, &f)
	}
	var n maitrev1.Node
	if _, err := db.Get(ctx, store.KindNode, nodeID.Fingerprint(), &n); err != nil || n.Name != "n1" {
		t.Fatalf("node record: %v", err)
	}
	var d maitrev1.TrustedDevice
	if _, err := db.Get(ctx, store.KindTrustedDevice, device.Fingerprint(), &d); err != nil || d.Name != "Laptop" {
		t.Fatalf("trusted device record: %v", err)
	}
}

func contains(s, sub string) bool { return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0 }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
