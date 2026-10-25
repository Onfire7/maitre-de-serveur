package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/recovery"
	"github.com/Onfire7/maitre-de-serveur/server/internal/recoverymode"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
)

// runRecover is `maitre recover --phrase "..." --addr NODE`: the Recovery
// Phrase path (decision 100). The phrase's key asks the Node to add this
// device as a Trusted Device; afterwards the device's own key works.
func runRecover(args []string) error {
	fs := flag.NewFlagSet("recover", flag.ExitOnError)
	idDir := fs.String("identity", filepath.Join(defaultState(), "device"), "directory holding this device's identity (created if missing)")
	trustDir := fs.String("trust", filepath.Join(defaultState(), "device", "trusted"), "directory of *.pub files for Nodes to trust")
	addr := fs.String("addr", fmt.Sprintf("127.0.0.1:%d", port), "Node address")
	phrase := fs.String("phrase", "", "the twelve-word Recovery Phrase")
	name := fs.String("name", hostname(), "name for this device")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ent, err := recovery.Parse(*phrase)
	if err != nil {
		return err
	}
	recID, err := identity.FromPrivateKey(recovery.Key(ent))
	if err != nil {
		return err
	}
	me, err := identity.LoadOrGenerate(*idDir)
	if err != nil {
		return err
	}
	list := trust.New()
	if err := list.LoadDir(*trustDir); err != nil {
		return err
	}
	c := maitrev1connect.NewNodeServiceClient(connect.NewClient(connecthttp.NewTransport(NewHTTPClient(mtls.ClientConfig(recID, list)), "https://"+*addr)))
	body, _ := proto.Marshal(&maitrev1.TrustedDevice{DeviceId: me.Fingerprint(), Name: *name, PublicKeyPem: string(me.PublicKeyPEM()), AddedUnixMs: time.Now().UnixMilli()})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := c.PutRecord(ctx, &maitrev1.PutRecordRequest{Record: &maitrev1.Record{Kind: store.KindTrustedDevice, Id: me.Fingerprint(), Body: body}}); err != nil {
		return fmt.Errorf("the Node refused the phrase: %w", err)
	}
	fmt.Printf("this device (%s) is now trusted by the Fleet\n", me.Fingerprint()[:8])
	return nil
}

// runAuthorize is `maitre authorize --code 12345678 --addr NODE:7444`: the
// recovery-code path (decision 101).
func runAuthorize(args []string) error {
	fs := flag.NewFlagSet("authorize", flag.ExitOnError)
	idDir := fs.String("identity", filepath.Join(defaultState(), "device"), "directory holding this device's identity (created if missing)")
	trustDir := fs.String("trust", filepath.Join(defaultState(), "device", "trusted"), "directory of *.pub files for Nodes to trust")
	addr := fs.String("addr", fmt.Sprintf("127.0.0.1:%d", recoverymode.Port), "Node recovery address")
	code := fs.String("code", "", "the code from the Node's recovery screen")
	name := fs.String("name", hostname(), "name for this device")
	if err := fs.Parse(args); err != nil {
		return err
	}
	me, err := identity.LoadOrGenerate(*idDir)
	if err != nil {
		return err
	}
	list := trust.New()
	if err := list.LoadDir(*trustDir); err != nil {
		return err
	}
	c := maitrev1connect.NewRecoveryServiceClient(connect.NewClient(connecthttp.NewTransport(NewHTTPClient(mtls.ClientConfig(me, list)), "https://"+*addr)))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := c.Authorize(ctx, &maitrev1.AuthorizeRequest{Code: *code, Name: *name, PublicKeyPem: string(me.PublicKeyPEM())})
	if err != nil {
		return err
	}
	fmt.Printf("this device is now trusted by Fleet %q (through Node %s)\n", res.GetFleetName(), res.GetNodeId()[:8])
	return nil
}

// runRecoveryCode is `maitre recovery-code --state DIR`: what the recovery
// screen does, for tests; prints the code.
func runRecoveryCode(args []string) error {
	fs := flag.NewFlagSet("recovery-code", flag.ExitOnError)
	state := fs.String("state", "/var/lib/maitre", "config partition mount point")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := os.Stat(*state); err != nil {
		return err
	}
	code, err := recoverymode.NewCode(*state)
	if err != nil {
		return err
	}
	fmt.Println(code)
	return nil
}
