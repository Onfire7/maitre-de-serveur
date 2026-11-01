package timezone_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/timezone"
)

type fakeRunner struct{ cmds []string }

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.cmds = append(f.cmds, name+" "+strings.Join(args, " "))
	return "", nil
}

// Decision 282: a Node sets its clock to the Fleet's zone when one is set
// and differs from its own; nothing happens without a record, with the
// zone it already has, or with a name it does not know.
func TestApply(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run := &fakeRunner{}

	if changed, err := timezone.Apply(ctx, st, run, nil); err != nil || changed {
		t.Fatalf("no record: changed=%v err=%v", changed, err)
	}
	if timezone.Resolve(ctx, st) != timezone.Local() {
		t.Fatalf("auto without a Fleet zone should be the Node's own, got %q", timezone.Resolve(ctx, st))
	}

	other := "Pacific/Auckland"
	if timezone.Local() == other {
		other = "America/Sao_Paulo"
	}
	if _, err := st.Put(ctx, timezone.Kind, timezone.ID, &maitrev1.FleetSettings{TimeZone: other}, true); err != nil {
		t.Fatal(err)
	}
	if changed, err := timezone.Apply(ctx, st, run, nil); err != nil || !changed {
		t.Fatalf("a new zone: changed=%v err=%v", changed, err)
	}
	if len(run.cmds) != 1 || run.cmds[0] != "timedatectl set-timezone "+other {
		t.Fatalf("commands %q", run.cmds)
	}
	if timezone.Fleet(ctx, st) != other || timezone.Resolve(ctx, st) != other {
		t.Fatalf("Fleet zone %q, resolved %q", timezone.Fleet(ctx, st), timezone.Resolve(ctx, st))
	}

	run.cmds = nil
	if _, err := st.Put(ctx, timezone.Kind, timezone.ID, &maitrev1.FleetSettings{TimeZone: timezone.Local()}, true); err != nil {
		t.Fatal(err)
	}
	if changed, err := timezone.Apply(ctx, st, run, nil); err != nil || changed || len(run.cmds) != 0 {
		t.Fatalf("the zone already set: changed=%v err=%v cmds=%q", changed, err, run.cmds)
	}

	for _, bad := range []string{"Mars/Olympus", "../etc/passwd", "/etc/localtime", "Local", "Europe/Paris x"} {
		if _, err := st.Put(ctx, timezone.Kind, timezone.ID, &maitrev1.FleetSettings{TimeZone: bad}, true); err != nil {
			t.Fatal(err)
		}
		if changed, err := timezone.Apply(ctx, st, run, nil); err == nil || changed || len(run.cmds) != 0 {
			t.Errorf("%q: changed=%v err=%v cmds=%q", bad, changed, err, run.cmds)
		}
		if timezone.Fleet(ctx, st) != "" {
			t.Errorf("%q: an unknown zone should read as none, got %q", bad, timezone.Fleet(ctx, st))
		}
	}
}
