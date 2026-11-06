package services

import (
	"context"
	"fmt"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/imagestore"
	"github.com/Onfire7/maitre-de-serveur/server/internal/metrics"
	"github.com/Onfire7/maitre-de-serveur/server/internal/storage"
)

// MoveImageStore puts the image store where decision 319 says it belongs
// and gives the root partition back the space it was never sized to lend.
//
// The Services of this Node stop for it and start again after, because
// removing the old store under a running container would take its mounted
// layers with it. Only the ones that were running are started again: a
// Service someone stopped stays stopped.
func (m *Manager) MoveImageStore(ctx context.Context, dir string) (*maitrev1.MoveImageStoreResponse, error) {
	to := dir
	if to == "" {
		if m.cfg.ImageStore == nil {
			return nil, fmt.Errorf("this Node cannot choose a place for its image store")
		}
		chosen, err := m.cfg.ImageStore(ctx)
		if err != nil {
			return nil, err
		}
		to = chosen
	}
	if to == "" {
		return nil, fmt.Errorf("the image store needs a Drive to live on, and this Node has none; add one first")
	}
	// Not storeDir: podman's graphroot is /var/lib/containers/storage
	// whether or not a Drive is mounted there, which is the whole point of
	// the arrangement. Backing is where the bytes actually are.
	from := imagestore.Backing()
	res := &maitrev1.MoveImageStoreResponse{From: from, To: to}
	if from == to {
		return res, nil
	}
	// Not freeBytes("/"): on a booted bootc Node that is the deployment's
	// composefs image, a few megabytes, and the numbers that are the whole
	// point of this move would both read zero (metrics.SystemDiskDir).
	root := metrics.SystemDiskDir()
	res.RootFreeBeforeBytes = m.freeBytes(root)

	// Stop what is running, remembering it.
	specs, err := m.mine(ctx)
	if err != nil {
		return nil, err
	}
	var running []*maitrev1.ServiceSpec
	for _, sp := range specs {
		if st, err := m.Status(ctx, sp); err == nil && st.GetState() == maitrev1.ServiceStatus_STATE_RUNNING {
			running = append(running, sp)
			res.Services = append(res.Services, sp.GetName())
		}
	}
	for _, sp := range running {
		if _, err := m.run.Run(ctx, "systemctl", "stop", lifecycleUnit(sp)); err != nil {
			m.logf("%s: stopping it to move the image store: %v", sp.GetName(), err)
		}
	}
	defer func() {
		for _, sp := range running {
			if _, err := m.run.Run(ctx, "systemctl", "start", "--no-block", lifecycleUnit(sp)); err != nil {
				m.logf("%s: starting it again after the move: %v", sp.GetName(), err)
			}
		}
	}()

	moved, err := imagestore.Move(ctx, m.run, from, to)
	res.MovedBytes = moved
	// The graphroot changed under us, so the cached answer must go
	// whatever happened.
	m.storeMu.Lock()
	m.store_ = ""
	m.storeMu.Unlock()
	res.RootFreeAfterBytes = m.freeBytes(root)
	if err != nil {
		return res, err
	}
	return res, nil
}

// EnsureImageStore arranges the image store at startup, for a Node that
// has somewhere to put it and nothing there yet (decision 319). It moves
// no bytes: a Node whose store already holds images keeps them until
// someone asks for MoveImageStore, since the copy is large and freeing a
// root is not a thing to do silently.
func (m *Manager) EnsureImageStore(ctx context.Context) {
	if m.cfg.ImageStore == nil {
		return
	}
	// A Node that decision 319's first design moved by configuration
	// cannot stage an update at all, so this is repaired before anything
	// else and whether or not anyone asked. It copies nothing: the same
	// directory is simply reached by podman's own graphroot from here on.
	if did, err := imagestore.Migrate(ctx, m.run); err != nil {
		m.logf("the image store was moved by configuration, which stops this Node updating, and it could not be re-mounted instead: %v", err)
		return
	} else if did {
		m.logf("the image store is now bind-mounted onto %s instead of being configured, which is what `bootc upgrade` needs (decision 319)", imagestore.DefaultRoot)
		m.storeMu.Lock()
		m.store_ = ""
		m.storeMu.Unlock()
		return
	}
	// Already arranged: leave it alone.
	if cur := imagestore.Configured(); cur != "" {
		return
	}
	to, err := m.cfg.ImageStore(ctx)
	if err != nil || to == "" {
		// No Drive yet. Install refuses a Service without a Space anyway,
		// and this runs again when one is adopted.
		return
	}
	// A store already on root with images in it is a move, not a switch:
	// switching would leave them stranded there and pull everything again.
	if storage.FreeBytes(imagestore.DefaultRoot) > 0 && hasImages(ctx, m) {
		m.logf("the image store is still on the root partition; `maitre service move-image-store` puts it on %s", to)
		return
	}
	changed, err := imagestore.Ensure(ctx, m.run, to)
	if err != nil {
		m.logf("image store at %s: %v", to, err)
		return
	}
	if changed {
		m.logf("the image store is %s, mounted at %s (decision 319: off the root partition)", to, imagestore.DefaultRoot)
		m.storeMu.Lock()
		m.store_ = ""
		m.storeMu.Unlock()
	}
}

// hasImages reports whether podman's current store holds anything, which
// decides whether EnsureImageStore may switch or must defer to a move.
func hasImages(ctx context.Context, m *Manager) bool {
	out, err := m.run.Run(ctx, "podman", "images", "--quiet")
	return err == nil && len(out) > 0
}
