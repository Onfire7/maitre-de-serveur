//go:build maitredebug

package debugsurface

import (
	"context"
	"strings"
	"testing"
)

// The debug build reads only what the allowlist covers, and never the
// Node's identity: the one secret a diagnosis does not need.
func TestTheAllowlistHolds(t *testing.T) {
	if _, err := ReadFile("/var/lib/maitre/node/identity.key"); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("the Node's identity was readable: %v", err)
	}
	if _, err := ReadFile("/var/lib/maitre/fleet.db"); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("the Fleet database was readable: %v", err)
	}
	if _, err := ReadFile("/root/.ssh/id_ed25519"); err == nil || !strings.Contains(err.Error(), "outside what a debug build will read") {
		t.Fatalf("a path outside the allowlist was readable: %v", err)
	}
	if _, err := ReadFile("etc/containers/storage.conf"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("a relative path was accepted: %v", err)
	}
	// Traversal out of an allowed directory does not survive Clean.
	if _, err := ReadFile("/etc/containers/../../var/lib/maitre/node/identity.key"); err == nil {
		t.Fatal("traversal out of the allowlist worked")
	}
	// And the one procedure that makes this a build tag works here.
	out, err := Exec(context.Background(), "echo", []string{"diagnosis"})
	if err != nil || !strings.Contains(out, "diagnosis") {
		t.Fatalf("exec: %q %v", out, err)
	}
}
