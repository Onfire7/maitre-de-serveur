package debugsurface

import (
	"context"
	"errors"
	"testing"
)

// Decisions 323 and 326: a shipped image does not contain the procedures
// at all, and says so rather than refusing — a refusal would imply a way
// in. This test runs without the build tag, which is what ships.
func TestAShippedImageHasNoDebugSurface(t *testing.T) {
	if Enabled {
		t.Skip("built with -tags maitredebug; the other test covers that")
	}
	if _, err := ReadFile("/etc/containers/storage.conf"); !errors.Is(err, ErrNotADebugBuild) {
		t.Fatalf("read answered %v, want ErrNotADebugBuild", err)
	}
	if _, err := Exec(context.Background(), "echo", []string{"hello"}); !errors.Is(err, ErrNotADebugBuild) {
		t.Fatalf("exec answered %v, want ErrNotADebugBuild", err)
	}
}
