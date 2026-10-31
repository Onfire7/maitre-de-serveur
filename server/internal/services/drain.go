package services

import (
	"context"
	"fmt"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// Drainer is the Manager as Drain sees it (decision 242): the Services on
// this Node with a volume in a Space, and stopping and starting one while
// its files move. Starting one that was not running before is harmless:
// systemd starts it, as a Service whose Drive drains is a Service the user
// keeps.
type Drainer struct{ m *Manager }

// Drainer wraps m for the drain package.
func (m *Manager) Drainer() *Drainer { return &Drainer{m} }

// InSpace lists this Node's Services with a volume in spaceID.
func (d *Drainer) InSpace(ctx context.Context, spaceID string) []string {
	specs, _ := store.ListLive(ctx, d.m.store, KindService, func() *maitrev1.ServiceSpec { return &maitrev1.ServiceSpec{} })
	var out []string
	for _, s := range specs {
		if s.GetNodeId() != d.m.store.NodeID() {
			continue
		}
		for _, sp := range s.GetVolumeSpaces() {
			if sp == spaceID {
				out = append(out, s.GetName())
				break
			}
		}
	}
	return out
}

// Stop stops a Service's unit and waits for it.
func (d *Drainer) Stop(ctx context.Context, name string) error {
	spec, err := d.spec(ctx, name)
	if err != nil {
		return err
	}
	_, err = d.m.run.Run(ctx, "systemctl", "stop", lifecycleUnit(spec))
	return err
}

// Start starts a Service's unit again.
func (d *Drainer) Start(ctx context.Context, name string) error {
	spec, err := d.spec(ctx, name)
	if err != nil {
		return err
	}
	_, err = d.m.run.Run(ctx, "systemctl", "start", "--no-block", lifecycleUnit(spec))
	return err
}

func (d *Drainer) spec(ctx context.Context, name string) (*maitrev1.ServiceSpec, error) {
	var spec maitrev1.ServiceSpec
	if _, err := d.m.store.Get(ctx, KindService, name, &spec); err != nil {
		return nil, fmt.Errorf("no Service %s", name)
	}
	return &spec, nil
}
