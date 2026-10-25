package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
	"github.com/Onfire7/maitre-de-serveur/server/internal/identity"
	"github.com/Onfire7/maitre-de-serveur/server/internal/mtls"
	"github.com/Onfire7/maitre-de-serveur/server/internal/trust"
)

// runUpdate is `maitre update`: the Console's Update panel for a terminal.
//
//	maitre update --addr HOST:7443                      # show booted/staged/rollback images
//	maitre update --addr HOST:7443 --image REF [--insecure] [--no-reboot] [--wait]
//	maitre update --addr HOST:7443 --rollback [--no-reboot]
//	maitre update --addr HOST:7443 --reboot
func runUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	idDir := fs.String("identity", filepath.Join(defaultState(), "device"), "directory holding the caller's identity")
	trustDir := fs.String("trust", filepath.Join(defaultState(), "device", "trusted"), "directory of *.pub files for Nodes to trust")
	addr := fs.String("addr", "127.0.0.1:7443", "Node address")
	image := fs.String("image", "", "image reference to stage (bootc switch)")
	insecure := fs.Bool("insecure", false, "the image's registry speaks plain HTTP (a local Depot)")
	noReboot := fs.Bool("no-reboot", false, "stage only; do not reboot afterwards")
	rollback := fs.Bool("rollback", false, "boot the previous image next")
	reboot := fs.Bool("reboot", false, "just reboot the Node")
	wait := fs.Bool("wait", false, "with --image: poll until staging finishes or fails")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id, err := identity.Load(*idDir)
	if err != nil {
		return fmt.Errorf("load identity (run `maitre keygen` first): %w", err)
	}
	list := trust.New()
	if err := list.LoadDir(*trustDir); err != nil {
		return err
	}
	transport := connecthttp.NewTransport(NewHTTPClient(mtls.ClientConfig(id, list)), "https://"+*addr)
	client := maitrev1connect.NewNodeServiceClient(connect.NewClient(transport))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	switch {
	case *image != "":
		if _, err := client.UpdateImage(ctx, &maitrev1.UpdateImageRequest{Image: *image, Reboot: !*noReboot, InsecureRegistry: *insecure}); err != nil {
			return err
		}
		fmt.Printf("update to %s started\n", *image)
		if !*wait {
			return nil
		}
		last := ""
		for {
			res, err := client.GetUpdateStatus(ctx, &maitrev1.GetUpdateStatusRequest{})
			if err != nil {
				return fmt.Errorf("status: %w (if the Node is rebooting, that is expected)", err)
			}
			st := res.GetStatus()
			if line := fmt.Sprintf("%-9s %3.0f%% %s", st.GetState().String()[6:], st.GetProgress()*100, st.GetMessage()); line != last {
				fmt.Println(line)
				last = line
			}
			if st.GetState() != maitrev1.UpdateStatus_STATE_UPDATING {
				return nil
			}
			time.Sleep(time.Second)
		}
	case *rollback:
		if _, err := client.Rollback(ctx, &maitrev1.RollbackRequest{Reboot: !*noReboot}); err != nil {
			return err
		}
		fmt.Println("rollback queued")
		return nil
	case *reboot:
		_, err := client.Reboot(ctx, &maitrev1.RebootRequest{})
		return err
	}
	res, err := client.GetUpdateStatus(ctx, &maitrev1.GetUpdateStatusRequest{})
	if err != nil {
		return err
	}
	st := res.GetStatus()
	if !st.GetSupported() {
		fmt.Println("this Node does not run on bootc")
		return nil
	}
	show := func(label string, d *maitrev1.ImageDeployment) {
		if d != nil {
			fmt.Printf("%-9s %s  %s  %s\n", label, d.GetImage(), d.GetDigest(), d.GetVersion())
		}
	}
	show("booted", st.GetBooted())
	show("staged", st.GetStaged())
	show("rollback", st.GetRollback())
	fmt.Printf("state     %s", st.GetState().String()[6:])
	if st.GetMessage() != "" {
		fmt.Printf("  %s", st.GetMessage())
	}
	if st.GetRollbackQueued() {
		fmt.Print("  (rollback boots next)")
	}
	fmt.Println()
	return nil
}
