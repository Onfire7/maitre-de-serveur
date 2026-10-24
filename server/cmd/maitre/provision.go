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

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/pairing"
	"github.com/Onfire7/maitre-de-serveur/server/internal/recovery"
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

	req := &maitrev1.ProvisionRequest{
		Fleet:      &maitrev1.Fleet{FleetId: hex.EncodeToString(raw), Name: *fleetName, CreatedUnixMs: time.Now().UnixMilli()},
		NodeName:   *nodeName,
		TargetDisk: *disk,
		TrustedDevices: []*maitrev1.TrustedDevice{
			{Name: hostname(), PublicKeyPem: string(id.PublicKeyPEM())},
			{Name: "Recovery Phrase", PublicKeyPem: string(recID.PublicKeyPEM()), RecoveryOnly: true},
		},
		DataPartition: *data,
	}
	if _, err := c.Provision(ctx, req); err != nil {
		return err
	}
	fmt.Println("provisioning started")
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
