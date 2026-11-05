//go:build maitredebug

package debugsurface

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Enabled reports whether this binary carries the debug surface.
//
// True here, which the Console advertises loudly: decision 323 turns the
// whole image box amber on both the Update and Node screens and puts a
// "Debug Console" pill in it, because the risk this tier adds is not a
// debug image existing but a debug image running in production unnoticed.
const Enabled = true

// Allowed are the directories ReadFile will look in. An allowlist and not
// a denylist: the point of the debug tier is diagnosis, and a procedure
// that can read anything is a procedure that can read the Node's keys.
var Allowed = []string{
	"/etc/containers",
	"/etc/systemd/system",
	"/etc/greenboot",
	"/var/log",
	"/var/lib/maitre/last-rollback",
	"/var/lib/maitre/red-boots",
	"/proc/mounts",
	"/proc/meminfo",
	"/proc/net/dev",
}

// Denied are paths no debug build reads, whatever the allowlist says: the
// Node's own identity is the one secret a diagnosis never needs.
var Denied = []string{"/var/lib/maitre/node", "/var/lib/maitre/fleet.db"}

// ReadFile reads a file for diagnosis, if the allowlist covers it.
func ReadFile(path string) ([]byte, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return nil, fmt.Errorf("%s is not an absolute path", path)
	}
	for _, d := range Denied {
		if clean == d || strings.HasPrefix(clean, d+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s holds this Node's identity and is never read", d)
		}
	}
	for _, d := range Allowed {
		if clean == d || strings.HasPrefix(clean, d+string(filepath.Separator)) {
			return os.ReadFile(clean)
		}
	}
	return nil, fmt.Errorf("%s is outside what a debug build will read (%s)", clean, strings.Join(Allowed, ", "))
}

// Exec runs a command on the Node. This is the dangerous procedure, and
// the reason the whole surface is a build tag: nothing diagnostic depends
// on it, so a shipped image loses only this.
func Exec(ctx context.Context, name string, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %w", name, err)
	}
	return string(out), nil
}
