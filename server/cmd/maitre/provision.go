package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/pairing"
	"github.com/Onfire7/maitre-de-serveur/server/internal/recovery"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
)

// runProvision is `maitre provision`: a command-line stand-in for the Console
// used by tests and CI. It pairs with an Installer, shows the pairing code,
// and provisions it into a new Fleet.
func runProvision(args []string) error {
	fs := flag.NewFlagSet("provision", flag.ExitOnError)
	idDir := fs.String("identity", filepath.Join(defaultState(), "device"), "directory holding the caller's identity")
	addr := fs.String("addr", fmt.Sprintf("127.0.0.1:%d", port), "Installer address")
	fleetName := fs.String("fleet", "Home", "Fleet name (a new Fleet is created)")
	nodeName := fs.String("name", "", "Node name (required)")
	disk := fs.String("disk", "", "target disk path (required)")
	data := fs.Bool("data", false, "create a data partition from the remaining space")
	yes := fs.Bool("yes", false, "do not ask to confirm the pairing code")
	wait := fs.Bool("wait", true, "poll until the install finishes")
	savePub := fs.String("save-node-pub", "", "write the new Node's public key PEM here (it keeps the Installer's key)")
	joinFrom := fs.String("join-from", "", "address of a Node of an existing Fleet to join instead of creating one (its Fleet, devices and Nodes are copied; this device must be trusted there)")
	trustDir := fs.String("trust", filepath.Join(defaultState(), "device", "trusted"), "with --join-from: directory of *.pub files for Nodes to trust")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *nodeName == "" || *disk == "" {
		return fmt.Errorf("--name and --disk are required")
	}
	id, err := identity.LoadOrGenerate(*idDir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	// First contact learns the Installer's key; the pairing code is what
	// confirms it (Q66). Everything after pins that key.
	instKey, err := mtls.PeekServerPublicKey(ctx, *addr)
	if err != nil {
		return fmt.Errorf("reach installer: %w", err)
	}
	list := trust.New()
	list.Add(identity.Fingerprint(instKey))
	transport := connecthttp.NewTransport(NewHTTPClient(mtls.ClientConfig(id, list)), "https://"+*addr)
	c := maitrev1connect.NewInstallerServiceClient(connect.NewClient(transport))

	st, err := c.GetInstallerStatus(ctx, &maitrev1.GetInstallerStatusRequest{})
	if err != nil {
		return err
	}
	s := st.GetStatus()
	fmt.Printf("installer  %s  (%s, version %s)\n", s.GetInstallerId(), s.GetMachineName(), s.GetVersion())
	for _, d := range s.GetDisks() {
		tag := ""
		if d.GetIsInstallerMedia() {
			tag = "  [installer media]"
		}
		fmt.Printf("  disk %-28s %-24s %6d GB%s\n", d.GetPath(), d.GetModel(), d.GetSizeBytes()>>30, tag)
	}
	if want := pairing.Code(instKey, id.PublicKey()); want != s.GetPairingCode() {
		return fmt.Errorf("pairing code mismatch: installer says %s, we compute %s", s.GetPairingCode(), want)
	}
	fmt.Printf("\nPairing code: %s\n", s.GetPairingCode())
	if !*yes {
		fmt.Print("Does the machine's screen show the same code? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y") {
			return fmt.Errorf("pairing not confirmed")
		}
	}

	if *savePub != "" {
		instID, err := identity.FromPublicKey(instKey)
		if err != nil {
			return err
		}
		if err := os.WriteFile(*savePub, instID.PublicKeyPEM(), 0o644); err != nil {
			return err
		}
	}

	req := &maitrev1.ProvisionRequest{NodeName: *nodeName, TargetDisk: *disk, DataPartition: *data}
	var joined maitrev1connect.NodeServiceClient
	if *joinFrom != "" {
		// Join: everything comes from an existing Node (Milestone 3).
		nl := trust.New()
		if err := nl.LoadDir(*trustDir); err != nil {
			return err
		}
		joined = maitrev1connect.NewNodeServiceClient(connect.NewClient(connecthttp.NewTransport(NewHTTPClient(mtls.ClientConfig(id, nl)), "https://"+*joinFrom)))
		recs, err := joined.ListRecords(ctx, &maitrev1.ListRecordsRequest{})
		if err != nil {
			return fmt.Errorf("read the Fleet from %s: %w", *joinFrom, err)
		}
		for _, r := range recs.GetRecords() {
			if r.GetDeleted() {
				continue
			}
			switch r.GetKind() {
			case store.KindFleet:
				f := &maitrev1.Fleet{}
				if err := proto.Unmarshal(r.GetBody(), f); err == nil {
					req.Fleet = f
				}
			case store.KindTrustedDevice:
				d := &maitrev1.TrustedDevice{}
				if err := proto.Unmarshal(r.GetBody(), d); err == nil {
					req.TrustedDevices = append(req.TrustedDevices, d)
				}
			case store.KindNode:
				n := &maitrev1.Node{}
				if err := proto.Unmarshal(r.GetBody(), n); err == nil {
					if n.GetName() == *nodeName {
						return fmt.Errorf("the Fleet already has a Node named %q", *nodeName)
					}
					req.Nodes = append(req.Nodes, n)
				}
			}
		}
		if req.Fleet == nil {
			return fmt.Errorf("%s holds no Fleet record", *joinFrom)
		}
		fmt.Printf("\njoining Fleet %q with %d Node(s)\n\n", req.Fleet.GetName(), len(req.Nodes))
	} else {
		// A new Fleet: random id, Recovery Phrase shown once.
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		phrase, err := recovery.Generate()
		if err != nil {
			return err
		}
		ent, _ := recovery.Parse(phrase.String())
		recID, err := identity.FromPrivateKey(recovery.Key(ent))
		if err != nil {
			return err
		}
		fmt.Printf("\nRecovery Phrase (write it down, it is not shown again):\n\n    %s\n\n", phrase)
		req.Fleet = &maitrev1.Fleet{FleetId: hex.EncodeToString(raw), Name: *fleetName, CreatedUnixMs: time.Now().UnixMilli()}
		req.TrustedDevices = []*maitrev1.TrustedDevice{
			{Name: hostname(), PublicKeyPem: string(id.PublicKeyPEM())},
			{Name: "Recovery Phrase", PublicKeyPem: string(recID.PublicKeyPEM()), RecoveryOnly: true},
		}
	}
	if _, err := c.Provision(ctx, req); err != nil {
		return err
	}
	fmt.Println("provisioning started")
	if joined != nil {
		// Introduce the new Node to the Fleet so existing Nodes trust it and
		// find it; the Installer's key becomes the Node's key.
		instID, err := identity.FromPublicKey(instKey)
		if err != nil {
			return err
		}
		body, _ := proto.Marshal(&maitrev1.Node{NodeId: instID.Fingerprint(), Name: *nodeName, PublicKeyPem: string(instID.PublicKeyPEM()), ProvisionedUnixMs: time.Now().UnixMilli()})
		if _, err := joined.PutRecord(ctx, &maitrev1.PutRecordRequest{Record: &maitrev1.Record{Kind: store.KindNode, Id: instID.Fingerprint(), Body: body}}); err != nil {
			return fmt.Errorf("introduce the new Node to %s: %w", *joinFrom, err)
		}
		fmt.Println("introduced to the Fleet")
	}
	if !*wait {
		return nil
	}
	last := ""
	for {
		time.Sleep(time.Second)
		st, err := c.GetInstallerStatus(ctx, &maitrev1.GetInstallerStatusRequest{})
		if err != nil {
			// The Installer reboots when done; losing it after REBOOTING is success.
			if strings.HasPrefix(last, "REBOOTING") {
				fmt.Println("installer went away after reporting success; node is rebooting")
				return nil
			}
			return err
		}
		p := st.GetStatus().GetProvisioning()
		line := fmt.Sprintf("%s %3d%% %s", strings.TrimPrefix(p.GetPhase().String(), "PHASE_"), p.GetPercent(), p.GetMessage())
		if line != last {
			fmt.Println(line)
			last = line
		}
		switch p.GetPhase() {
		case maitrev1.Provisioning_PHASE_REBOOTING:
			return nil
		case maitrev1.Provisioning_PHASE_FAILED:
			return fmt.Errorf("install failed: %s", p.GetMessage())
		}
	}
}
