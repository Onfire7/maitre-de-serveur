package quadlet_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/quadlet"
)

// These exercise the real podman Quadlet generator; they skip where none is
// installed (so they run on the Node image in CI and on dev boxes that have
// podman, and are skipped elsewhere).
func TestDryRun(t *testing.T) {
	if quadlet.Generator() == "" {
		t.Skip("no Quadlet generator installed")
	}
	ctx := context.Background()
	net := "[Network]\nNetworkName=maitre\nOptions=isolate=true\n"

	t.Run("accepts a good unit", func(t *testing.T) {
		units := map[string]string{
			"maitre.network": net,
			"ok.container":   "[Container]\nImage=docker.io/library/alpine\nNetwork=maitre.network\nExec=sleep 1\n",
		}
		if err := quadlet.DryRun(ctx, units); err != nil {
			t.Fatalf("good unit rejected: %v", err)
		}
	})

	t.Run("reports an unknown key", func(t *testing.T) {
		units := map[string]string{
			"bad.container": "[Container]\nImage=docker.io/library/alpine\nVolumes=/a:/b\n",
		}
		err := quadlet.DryRun(ctx, units)
		if err == nil {
			t.Fatal("unknown key accepted")
		}
		if !strings.Contains(err.Error(), "Volumes") {
			t.Fatalf("message does not name the bad key: %v", err)
		}
	})

	t.Run("reports a missing referenced unit", func(t *testing.T) {
		units := map[string]string{
			"ref.container": "[Container]\nImage=docker.io/library/alpine\nNetwork=absent.network\n",
		}
		if err := quadlet.DryRun(ctx, units); err == nil {
			t.Fatal("missing network accepted")
		}
	})
}
