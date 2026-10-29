// Package quadlet validates Quadlet units with Podman's own generator before
// the Node installs them, so an entry Podman would reject fails the install
// call with Podman's own message instead of leaving a unit that never starts
// (plan C6).
package quadlet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// generatorPaths are where podman's Quadlet generator lives: /usr/libexec on
// Fedora (the Node image), /usr/lib on some others (dev boxes).
var generatorPaths = []string{
	"/usr/libexec/podman/quadlet",
	"/usr/lib/podman/quadlet",
}

// Generator returns the path to podman's Quadlet generator, or "" when none
// is installed (a dev box or test host without podman).
func Generator() string {
	for _, p := range generatorPaths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("quadlet"); err == nil {
		return p
	}
	return ""
}

// ErrNoGenerator means no Quadlet generator is installed, so DryRun could not
// check the units. Callers on the Node treat this as fatal; dev and test
// hosts skip the check.
var ErrNoGenerator = errors.New("no Quadlet generator installed")

// DryRun writes every unit (name -> contents, e.g. "jellyfin.container") to a
// temporary directory and runs the Quadlet generator over them. It returns
// nil when the generator accepts them all, ErrNoGenerator when none is
// installed, or an error carrying the generator's own diagnostics. Units that
// reference one another (a container on a .network, say) must be passed
// together, since the generator resolves those references.
func DryRun(ctx context.Context, units map[string]string) error {
	gen := Generator()
	if gen == "" {
		return ErrNoGenerator
	}
	dir, err := os.MkdirTemp("", "maitre-quadlet-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for name, body := range units {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	// -dryrun prints the generated service units to stdout and reports
	// conversion problems on stderr with a non-zero exit.
	cmd := exec.CommandContext(ctx, gen, "-dryrun")
	cmd.Env = append(os.Environ(), "QUADLET_UNIT_DIRS="+dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	return fmt.Errorf("Quadlet rejected the unit: %s", firstProblem(out))
}

// firstProblem pulls the useful line out of the generator's output: it logs
// one "converting ...: <reason>" line per bad unit, amid "Loading source"
// and short-name warnings.
func firstProblem(out []byte) string {
	var warn string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Drop the "quadlet-generator[pid]: " prefix systemd-style logging adds.
		if i := strings.Index(line, ": "); i > 0 && strings.HasPrefix(line, "quadlet-generator[") {
			line = line[i+2:]
		}
		switch {
		case strings.HasPrefix(line, "Loading source"):
			continue
		case strings.HasPrefix(line, "converting "):
			return line
		case strings.HasPrefix(line, "Warning:"):
			if warn == "" {
				warn = line
			}
		case strings.Contains(line, "processing encountered some errors"):
			continue
		default:
			if warn == "" {
				warn = line
			}
		}
	}
	if warn != "" {
		return warn
	}
	return "see the Node's logs for details"
}
