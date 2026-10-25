package main

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/Onfire7/maitre-de-serveur/server/internal/mdns"
)

var hostnameClean = regexp.MustCompile(`[^a-z0-9-]+`)

// hostLabel turns a human name into a DNS label: "Test 1" -> "test-1".
func hostLabel(name string) string {
	s := hostnameClean.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}

// ensureHostname names this machine `label` so its mDNS host record is its
// own. Every Node and Installer used to be "fedora", so two of them announced
// the same fedora.local and resolved to whichever answered first.
func ensureHostname(log *slog.Logger, label string) {
	if label == "" || os.Geteuid() != 0 {
		// Only a Node (root) renames the machine; on a developer's box the
		// polkit prompt behind hostnamectl would stall the start.
		return
	}
	cur, _ := os.Hostname()
	if cur != label {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// hostnamed persists it; the syscall is the fallback (the live
		// Installer may not have hostnamed up).
		if err := exec.CommandContext(ctx, "hostnamectl", "set-hostname", label).Run(); err != nil {
			if err := sethostname(label); err != nil {
				log.Warn("hostname not set", "want", label, "err", err)
				return
			}
			_ = os.WriteFile("/etc/hostname", []byte(label+"\n"), 0o644)
		}
		log.Info("hostname set", "hostname", label, "was", cur)
	}
	// Avahi does not follow: tell it, every start, so the mDNS host record
	// is this machine's own even on a system that booted with another name.
	if err := mdns.SetHostName(label); err != nil {
		log.Warn("mDNS host name not set", "want", label, "err", err)
	}
}
