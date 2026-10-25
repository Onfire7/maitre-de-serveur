package recoverymode

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
	"testing"

	"connectrpc.com/connect/v2"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

func TestCodeLifecycle(t *testing.T) {
	dir := t.TempDir()
	if Requested(dir) {
		t.Fatal("requested before any request")
	}
	if err := Request(dir); err != nil {
		t.Fatal(err)
	}
	if !Requested(dir) || Requested(dir) {
		t.Fatal("request must be honoured exactly once")
	}
	code, err := NewCode(dir)
	if err != nil || len(code) != 8 {
		t.Fatalf("code %q %v", code, err)
	}
	if !Pending(dir) {
		t.Fatal("code should be pending")
	}
	st, _ := store.Open(filepath.Join(dir, "fleet.db"), "node-a")
	defer st.Close()
	svc := NewService(dir, st, "node-a", &maitrev1.Fleet{FleetId: "f", Name: "Home"})
	dev, _ := identity.Generate()
	ctx := peer.WithInfo(context.Background(), peer.Info{Fingerprint: dev.Fingerprint(), PublicKey: dev.PublicKey()})
	req := &maitrev1.AuthorizeRequest{Code: "00000000", Name: "laptop", PublicKeyPem: string(dev.PublicKeyPEM())}
	if code == "00000000" {
		req.Code = "11111111"
	}
	if _, err := svc.Authorize(ctx, req); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("wrong code accepted: %v", err)
	}
	req.Code = code
	res, err := svc.Authorize(ctx, req)
	if err != nil || res.GetFleetName() != "Home" || res.GetNodeId() != "node-a" {
		t.Fatalf("authorize: %v %v", res, err)
	}
	if Pending(dir) {
		t.Fatal("code should be spent")
	}
	if _, err := svc.Authorize(ctx, req); err == nil {
		t.Fatal("second use accepted")
	}
	devices, _ := store.ListLive(context.Background(), st, store.KindTrustedDevice, func() *maitrev1.TrustedDevice { return &maitrev1.TrustedDevice{} })
	if len(devices) != 1 || devices[0].GetName() != "laptop" || devices[0].GetRecoveryOnly() {
		t.Fatalf("devices %v", devices)
	}
	// A key that is not the connection's is refused.
	other, _ := identity.Generate()
	_ = ed25519.PublicKey(other.PublicKey())
	if _, err := svc.Authorize(ctx, &maitrev1.AuthorizeRequest{Code: code, Name: "x", PublicKeyPem: string(other.PublicKeyPEM())}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("mismatched key: %v", err)
	}
}
