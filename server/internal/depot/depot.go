package depot

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Hostname is the mDNS name a Depot publishes for itself (decision 81).
const Hostname = "maitre-depot.local"

// DefaultDir is where the Depot keeps images and netboot files (decision 85).
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".maitre/depot"
	}
	return filepath.Join(home, ".maitre", "depot")
}

// Layout of a Depot directory.
type Layout struct {
	Dir string
	// BootDir overrides Dir/boot (the Desktop keeps only boot files).
	BootDir string
}

// Images is the directory of OCI layouts, one per image name.
func (l Layout) Images() string { return filepath.Join(l.Dir, "images") }

// Boot is the directory of netboot files served over HTTP and TFTP.
func (l Layout) Boot() string {
	if l.BootDir != "" {
		return l.BootDir
	}
	return filepath.Join(l.Dir, "boot")
}

// Publish copies a local container image into the Depot as name:tag using
// podman's oci: transport, so no registry protocol is involved.
func Publish(ctx context.Context, l Layout, image, name, tag string, stdout, stderr *os.File) error {
	if !nameRE.MatchString(name) || !tagRE.MatchString(tag) {
		return fmt.Errorf("invalid name or tag %q:%q", name, tag)
	}
	dest := filepath.Join(l.Images(), name)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "podman", "push", image, "oci:"+dest+":"+tag)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("podman push %s: %w", image, err)
	}
	return nil
}

// Handler serves the registry under /v2/, the boot directory under /boot/,
// a JSON list of images at /api/images for the Console (decision 91), and a
// plain-text index at / for humans. registry is the host:port Nodes should
// pull from.
func Handler(l Layout, registry string) http.Handler {
	reg := &Registry{Dir: l.Images()}
	mux := http.NewServeMux()
	mux.Handle("/v2/", reg)
	mux.HandleFunc("/api/images", func(w http.ResponseWriter, r *http.Request) {
		imgs, err := reg.Images()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Read-only, public on the LAN anyway; let the Console's webview read it.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"registry": registry, "images": imgs})
	})
	mux.Handle("/boot/", http.StripPrefix("/boot/", http.FileServer(http.Dir(l.Boot()))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		imgs, _ := reg.Images()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "Maître Depot\n\nImages (pull as %s/<name>:<tag>):\n", r.Host)
		for n, tags := range imgs {
			fmt.Fprintf(w, "  %s  %s\n", n, strings.Join(tags, " "))
		}
		fmt.Fprintf(w, "\nNetboot files: /boot/\n")
	})
	return mux
}

// LANAddress returns the IPv4 address this machine would use to reach the
// local network: the source address of a UDP socket toward a LAN-ish
// destination. No packet is sent.
func LANAddress() (string, error) {
	conn, err := net.Dial("udp4", "192.0.2.1:9") // TEST-NET-1, never routed
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}
