package mtls_test

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"golang.org/x/net/http2"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/nodeservice"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
)

func startNode(t *testing.T, node *identity.Identity, list *trust.List) *httptest.Server {
	t.Helper()
	rpc := connect.NewServer()
	maitrev1connect.RegisterNodeServiceHandler(rpc, nodeservice.New(node, nodeservice.Info{Name: "test", DevMode: true}))
	mux := http.NewServeMux()
	connecthttp.Mount(mux, rpc)
	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = mtls.ServerConfig(node, list)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func client(cfg *tls.Config, url string) maitrev1connect.NodeServiceClient {
	hc := &http.Client{Transport: &http2.Transport{
		TLSClientConfig: cfg,
		DialTLSContext: func(ctx context.Context, network, addr string, c *tls.Config) (net.Conn, error) {
			return (&tls.Dialer{Config: c}).DialContext(ctx, network, addr)
		},
	}}
	return maitrev1connect.NewNodeServiceClient(connect.NewClient(connecthttp.NewTransport(hc, url)))
}

func TestTrustedDeviceCanCall(t *testing.T) {
	node, _ := identity.Generate()
	device, _ := identity.Generate()

	nodeTrust := trust.New()
	nodeTrust.Add(device.Fingerprint())
	deviceTrust := trust.New()
	deviceTrust.Add(node.Fingerprint())

	srv := startNode(t, node, nodeTrust)
	res, err := client(mtls.ClientConfig(device, deviceTrust), srv.URL).
		GetNodeInfo(context.Background(), &maitrev1.GetNodeInfoRequest{})
	if err != nil {
		t.Fatalf("trusted call failed: %v", err)
	}
	if got := res.GetNode().GetNodeId(); got != node.Fingerprint() {
		t.Fatalf("node_id = %q, want %q", got, node.Fingerprint())
	}
}

func TestUntrustedDeviceIsRefused(t *testing.T) {
	node, _ := identity.Generate()
	device, _ := identity.Generate()

	nodeTrust := trust.New() // device deliberately absent
	deviceTrust := trust.New()
	deviceTrust.Add(node.Fingerprint())

	srv := startNode(t, node, nodeTrust)
	_, err := client(mtls.ClientConfig(device, deviceTrust), srv.URL).
		GetNodeInfo(context.Background(), &maitrev1.GetNodeInfoRequest{})
	if err == nil {
		t.Fatal("untrusted device was allowed to call GetNodeInfo")
	}
}

func TestDeviceRefusesUntrustedNode(t *testing.T) {
	node, _ := identity.Generate()
	device, _ := identity.Generate()

	nodeTrust := trust.New()
	nodeTrust.Add(device.Fingerprint())
	deviceTrust := trust.New() // node deliberately absent

	srv := startNode(t, node, nodeTrust)
	_, err := client(mtls.ClientConfig(device, deviceTrust), srv.URL).
		GetNodeInfo(context.Background(), &maitrev1.GetNodeInfoRequest{})
	if err == nil {
		t.Fatal("device connected to a node that is not in its trust list")
	}
	if !strings.Contains(err.Error(), "trust") {
		t.Fatalf("expected a trust error, got: %v", err)
	}
}
