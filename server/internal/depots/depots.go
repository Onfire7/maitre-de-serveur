// Package depots is a Node's view of the Depots it can reach: the registry
// it boots from, and every Depot announcing itself on the Node's network.
// The Node reports what they offer in its UpdateStatus, so a Console that
// is not on the same network as the Depot (away from home) still sees the
// builds and can pick one; before this, the Console scanned for Depots
// itself and had to be on the LAN with them.
package depots

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/update"
)

// Listing is a Depot's answer at /api/images.
type Listing struct {
	Registry string               `json:"registry"`
	Images   map[string][]TagInfo `json:"images"`
}

// TagInfo is one tag in a Listing.
type TagInfo struct {
	Tag     string `json:"tag"`
	Digest  string `json:"digest"`
	Version string `json:"version"`
	Created string `json:"created"`
}

// Every is how often Run refreshes on its own; Stale is how old a listing
// may be before Poke (a Console asking) refreshes it again.
const (
	Every = 5 * time.Minute
	Stale = 30 * time.Second
)

// Watcher keeps the listing of every reachable Depot.
type Watcher struct {
	// Fetch asks the Depot at host (host:port) for its listing. The default
	// is plain HTTP, as a Depot serves (decision 91).
	Fetch func(ctx context.Context, host string) (*Listing, error)
	// Booted returns the image reference this Node runs, whose registry is
	// asked as a Depot too; "" when not on bootc.
	Booted func(ctx context.Context) string

	mu     sync.Mutex
	hosts  map[string]string // mDNS id -> host:port
	images []*maitrev1.DepotImage
	at     time.Time
	kick   chan struct{}
}

// New builds a Watcher; booted may be nil.
func New(booted func(ctx context.Context) string) *Watcher {
	return &Watcher{Fetch: fetchHTTP, Booted: booted, hosts: map[string]string{}, kick: make(chan struct{}, 1)}
}

// Discovered records a Depot found on the network (mDNS) and refreshes soon.
func (w *Watcher) Discovered(id, host string) {
	w.mu.Lock()
	w.hosts[id] = host
	w.mu.Unlock()
	w.Poke()
}

// Images is what the Depots offered when last asked, newest build first.
func (w *Watcher) Images() []*maitrev1.DepotImage {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.images
}

// Poke asks Run to refresh if the listing is older than Stale. It never
// blocks: a Console's status call must not wait on a Depot.
func (w *Watcher) Poke() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// Refresh asks every known Depot now. A Depot that does not answer drops
// out of the listing; the others stay.
func (w *Watcher) Refresh(ctx context.Context) {
	hosts := map[string]bool{}
	if w.Booted != nil {
		if h := update.RegistryHost(w.Booted(ctx)); h != "" {
			hosts[h] = true
		}
	}
	w.mu.Lock()
	for _, h := range w.hosts {
		hosts[h] = true
	}
	w.mu.Unlock()
	seen := map[string]bool{}
	var out []*maitrev1.DepotImage
	for h := range hosts {
		l, err := w.Fetch(ctx, h)
		if err != nil || l == nil || l.Registry == "" {
			continue
		}
		for name, tags := range l.Images {
			for _, t := range tags {
				ref := fmt.Sprintf("%s/%s:%s", l.Registry, name, t.Tag)
				if seen[ref] {
					continue
				}
				seen[ref] = true
				out = append(out, &maitrev1.DepotImage{Ref: ref, Depot: l.Registry, Tag: t.Tag, Version: t.Version, Created: t.Created, Digest: t.Digest})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].GetCreated() > out[j].GetCreated() })
	w.mu.Lock()
	w.images, w.at = out, time.Now()
	w.mu.Unlock()
}

// Run refreshes now, every Every, and on Poke once the listing is Stale.
func (w *Watcher) Run(ctx context.Context) {
	w.Refresh(ctx)
	t := time.NewTicker(Every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Refresh(ctx)
		case <-w.kick:
			w.mu.Lock()
			stale := time.Since(w.at) >= Stale
			w.mu.Unlock()
			if stale {
				w.Refresh(ctx)
			}
		}
	}
}

// Offered is the digest a Depot offers for the booted reference, or "" when
// no Depot lists it. With rollout.Newer this lights the Console's Update
// button as soon as the Depot has a new build, without waiting for the
// registry check.
func Offered(booted string, imgs []*maitrev1.DepotImage) string {
	for _, i := range imgs {
		if i.GetRef() == booted {
			return i.GetDigest()
		}
	}
	return ""
}

func fetchHTTP(ctx context.Context, host string) (*Listing, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+"/api/images", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", host, resp.Status)
	}
	var l Listing
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&l); err != nil {
		return nil, err
	}
	return &l, nil
}
