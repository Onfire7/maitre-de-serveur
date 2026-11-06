package services_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// netRunner answers `podman network inspect` from subnets and records the
// rest, so a test can say what Podman has and see what the Node does.
type netRunner struct {
	cmds    []string
	subnets string // what `podman network inspect` prints; "" means no network
	absent  bool   // the network is not there at all: inspect fails
	// created records that maitre-network.service has been restarted, so
	// inspect afterwards answers as Podman would once the network exists.
	created bool
	// badCreate leaves the network on another subnet even after creating
	// it, which is what the verifying inspect is there to catch.
	badCreate bool
}

func (f *netRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.cmds = append(f.cmds, name+" "+strings.Join(args, " "))
	if name == "podman" && len(args) > 1 && args[0] == "network" && args[1] == "inspect" {
		if f.created && !f.badCreate {
			return services.FleetSubnet + " ", nil
		}
		if f.absent && !f.created {
			return "", errorString("network not found")
		}
		return f.subnets, nil
	}
	if name == "systemctl" && len(args) > 1 && args[0] == "restart" && args[1] == "maitre-network.service" {
		f.created = true
		return "", nil
	}
	if name == "systemctl" && len(args) > 1 && args[0] == "show" {
		return "LoadState=loaded\nActiveState=inactive\nSubState=dead\nNRestarts=0\nResult=success\n", nil
	}
	return "", nil
}

type errorString string

func (e errorString) Error() string { return string(e) }

func (f *netRunner) ran(want string) bool {
	for _, c := range f.cmds {
		if c == want {
			return true
		}
	}
	return false
}

// ADR 0010's shared network has to be reconciled against what Podman
// actually has, never against the unit file: image/Containerfile ships
// /etc/containers/systemd/maitre.network with exactly the bytes
// NetworkUnitBody writes, so a Node that compared them would always match
// and never repair anything. These are the two cases that reach a real
// Node, and neither had a test before 2026-11-06.
func TestMigrateEnsuresSharedNetwork(t *testing.T) {
	for _, tc := range []struct {
		name    string
		absent  bool
		subnets string
		// The unit file is already there with the right contents, as the
		// image ships it.
		preWrite bool
	}{
		{name: "podman has no network at all", absent: true, preWrite: true},
		{name: "podman has it on another subnet", subnets: "10.89.0.0/24 ", preWrite: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
			defer st.Close()
			dir := t.TempDir()
			units := filepath.Join(dir, "units")
			if tc.preWrite {
				os.MkdirAll(units, 0o755)
				if err := os.WriteFile(filepath.Join(units, "maitre.network"), []byte(services.NetworkUnitBody()), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			r := &netRunner{subnets: tc.subnets, absent: tc.absent}
			m := services.New(services.Config{UnitsDir: units, SystemdDir: filepath.Join(dir, "systemd"), PoolsDir: filepath.Join(dir, "pools")}, st, r)

			m.Migrate(ctx)

			// The addresses fleetnet holds inside FleetSubnet are what
			// makes Podman refuse to create the network, so they have to
			// go first.
			if !r.ran("ip link del " + services.FleetIface) {
				t.Errorf("the fleet interface was not cleared before creating the network; ran %v", r.cmds)
			}
			// restart, not start: the unit is a oneshot and may already
			// have run in this boot, and `start` on one that has is a
			// no-op reporting success — which left Avalon with no network
			// at all and every Service failing to find it.
			if !r.ran("systemctl restart maitre-network.service") {
				t.Errorf("the shared network was never created; ran %v", r.cmds)
			}
			if r.ran("systemctl start maitre-network.service") {
				t.Error("used `systemctl start` on a oneshot that may already have run")
			}
			// The result is checked, not assumed: removing a network and
			// not replacing it is worse than leaving a wrong one alone.
			inspects := 0
			for _, c := range r.cmds {
				if strings.HasPrefix(c, "podman network inspect") {
					inspects++
				}
			}
			if inspects < 2 {
				t.Errorf("the created network was never inspected again; ran %v", r.cmds)
			}
			// A network on the wrong subnet has to go; one that is not
			// there cannot be removed.
			removed := r.ran("podman network rm -f maitre")
			if tc.absent && removed {
				t.Error("removed a network that was not there")
			}
			if !tc.absent && !removed {
				t.Errorf("kept a network on the wrong subnet; ran %v", r.cmds)
			}
			b, err := os.ReadFile(filepath.Join(units, "maitre.network"))
			if err != nil || string(b) != services.NetworkUnitBody() {
				t.Errorf("unit file = %q, %v", b, err)
			}
		})
	}
}

// A network already on the Fleet's subnet is left alone: no Service is
// stopped and nothing is recreated.
func TestMigrateLeavesAGoodNetworkAlone(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	defer st.Close()
	dir := t.TempDir()
	r := &netRunner{subnets: services.FleetSubnet + " "}
	m := services.New(services.Config{UnitsDir: filepath.Join(dir, "units"), SystemdDir: filepath.Join(dir, "systemd"), PoolsDir: filepath.Join(dir, "pools")}, st, r)

	m.Migrate(ctx)

	for _, unwanted := range []string{
		"podman network rm -f maitre",
		"ip link del " + services.FleetIface,
		"systemctl restart maitre-network.service",
	} {
		if r.ran(unwanted) {
			t.Errorf("a network already on %s was disturbed by %q", services.FleetSubnet, unwanted)
		}
	}
}
