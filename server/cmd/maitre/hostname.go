package main

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
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
	if label == "" {
		return
	}
	cur, _ := os.Hostname()
	if cur == label {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// hostnamed tells Avahi and friends; the syscall is the fallback (and
	// is what the live Installer has, where hostnamed may not be up).
	if err := exec.CommandContext(ctx, "hostnamectl", "set-hostname", label).Run(); err != nil {
		if err := syscall.Sethostname([]byte(label)); err != nil {
			log.Warn("hostname not set", "want", label, "err", err)
			return
		}
		_ = os.WriteFile("/etc/hostname", []byte(label+"\n"), 0o644)
	}
	log.Info("hostname set", "hostname", label, "was", cur)
}
