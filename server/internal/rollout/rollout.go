// Package rollout is the staged, automatic Maître Server update (Milestone
// 6): one Node of the Fleet, the first by id, takes a new image first; the
// others follow once it has run that image for the policy's stage period
// without greenboot rolling it back. Everything happens inside the policy's
// window and stops while it is paused.
package rollout

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/installer"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
	"github.com/Onfire7/maitre-de-serveur/server/internal/update"
)

// KindPolicy is the Record kind for UpdatePolicy.
const KindPolicy = "update_policy"

// Decision is what a Node should do now, with the reason for the Console.
type Decision struct {
	Update bool
	Reason string
}

// Decide applies the policy for this Node. nodes are every Node record of
// the Fleet (including ours), self our id, booted our digest, newest the
// registry's digest for our reference, now the local time.
func Decide(policy *maitrev1.UpdatePolicy, nodes []*maitrev1.Node, self, booted, newest string, now time.Time) Decision {
	if policy == nil || !policy.GetAutomatic() {
		return Decision{Reason: "automatic updates are off"}
	}
	if policy.GetPaused() {
		return Decision{Reason: "updates are paused"}
	}
	if newest == "" || newest == booted {
		return Decision{Reason: "up to date"}
	}
	h := now.Hour()
	start, end := int(policy.GetWindowStartHour()), int(policy.GetWindowEndHour())
	inWindow := start == end || (start < end && h >= start && h < end) || (start > end && (h >= start || h < end))
	if !inWindow {
		return Decision{Reason: "an update is ready; waiting for the update window"}
	}
	ids := make([]string, 0, len(nodes))
	byID := map[string]*maitrev1.Node{}
	for _, n := range nodes {
		ids = append(ids, n.GetNodeId())
		byID[n.GetNodeId()] = n
	}
	sort.Strings(ids)
	if len(ids) == 0 || ids[0] == self {
		return Decision{Update: true, Reason: "first Node of the Fleet: taking the update now"}
	}
	first := byID[ids[0]]
	stage := time.Duration(policy.GetStageHours()) * time.Hour
	if stage <= 0 {
		stage = 24 * time.Hour
	}
	if first.GetImageDigest() != newest {
		return Decision{Reason: "an update is ready; waiting for " + first.GetName() + " to take it first" + usually(first)}
	}
	ran := now.Sub(time.UnixMilli(first.GetBootedUnixMs()))
	if ran < stage {
		return Decision{Reason: "an update is ready; " + first.GetName() + " has run it " + ran.Round(time.Minute).String() + " of " + stage.String()}
	}
	return Decision{Update: true, Reason: first.GetName() + " has run the update for " + stage.String() + "; taking it now"}
}

// usually is how long n's updates usually take, " (usually about 6
// minutes)", from n's own runs through update.Typical, the same figure
// GetUpdateStatus reports; empty without one. It is the first Node's figure
// only, never an estimate for the whole rollout.
//
// Its wording and rounding (whole minutes, half up; "under a minute"
// below one) are the Console's for the same figure, minutes() and
// updateTimesLine in desktop/src/app/updateTimes.ts: change both together.
func usually(n *maitrev1.Node) string {
	total, _ := update.Typical(n.GetUpdates())
	switch m := int(total.Round(time.Minute) / time.Minute); {
	case total == 0:
		return ""
	case total < time.Minute:
		return " (usually under a minute)"
	case m == 1:
		return " (usually about 1 minute)"
	default:
		return " (usually about " + strconv.Itoa(m) + " minutes)"
	}
}

// Runner checks hourly and acts on the decision.
type Runner struct {
	st      *store.Store
	up      *update.Manager
	log     *slog.Logger
	self    string
	ref     string
	Current func() (ref, digest string)
	// Last decision, for NodeInfo.
	Last Decision
	// Status and Digest are the Node's bootc status and the registry
	// lookup; Check uses them to find a newer image.
	Status func(ctx context.Context) (*maitrev1.UpdateStatus, error)
	Digest func(ctx context.Context, ref string) (string, error)
	// Terminals lists every Terminal the Node holds; an automatic update
	// waits up to a day for the running ones (ADR 0011). Nil: none.
	Terminals func(ctx context.Context) ([]*maitrev1.Terminal, error)
	wait      TerminalWait

	mu    sync.Mutex
	avail string
}

// New builds a Runner for this Node.
func New(st *store.Store, up *update.Manager, log *slog.Logger, self string) *Runner {
	return &Runner{st: st, up: up, log: log, self: self, Status: up.Status, Digest: installer.RegistryDigest}
}

// Available is the newer image's digest Check last found, or empty.
func (r *Runner) Available() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.avail
}

// Check asks the registry for the newest digest of the booted reference
// and remembers it when the Node neither runs nor has staged it. A failed
// lookup keeps the last answer.
func (r *Runner) Check(ctx context.Context) {
	st, err := r.Status(ctx)
	if err != nil {
		return
	}
	ref := st.GetBooted().GetImage()
	if ref == "" {
		r.setAvailable("")
		return
	}
	newest, err := r.Digest(ctx, ref)
	if err != nil {
		return
	}
	r.setAvailable(Newer(st, newest))
}

func (r *Runner) setAvailable(d string) {
	r.mu.Lock()
	r.avail = d
	r.mu.Unlock()
}

// Newer is newest when it is neither the booted nor the staged image.
func Newer(st *maitrev1.UpdateStatus, newest string) string {
	if newest == "" || newest == st.GetBooted().GetDigest() || newest == st.GetStaged().GetDigest() {
		return ""
	}
	return newest
}

// Run loops until ctx ends: Check now and every 15 minutes, Tick hourly.
func (r *Runner) Run(ctx context.Context) {
	r.Check(ctx)
	check := time.NewTicker(15 * time.Minute)
	defer check.Stop()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-check.C:
			r.Check(ctx)
		case <-t.C:
			r.Tick(ctx)
		}
	}
}

// Tick evaluates the policy once.
func (r *Runner) Tick(ctx context.Context) {
	var policy maitrev1.UpdatePolicy
	if _, err := r.st.Get(ctx, KindPolicy, "fleet", &policy); err != nil {
		r.Last = Decision{Reason: "no update policy set"}
		return
	}
	if !policy.GetAutomatic() || policy.GetPaused() {
		r.Last = Decide(&policy, nil, r.self, "", "", time.Now())
		return
	}
	ref, booted := r.Current()
	if ref == "" {
		r.Last = Decision{Reason: "not running a bootc image"}
		return
	}
	newest, err := installer.RegistryDigest(ctx, ref)
	if err != nil {
		r.Last = Decision{Reason: "registry unreachable: " + strings.TrimSpace(err.Error())}
		return
	}
	nodes, err := store.ListLive(ctx, r.st, store.KindNode, func() *maitrev1.Node { return &maitrev1.Node{} })
	if err != nil {
		return
	}
	d := Decide(&policy, nodes, r.self, booted, newest, time.Now())
	if d.Update && r.Terminals != nil {
		terms, err := r.Terminals(ctx)
		if err != nil {
			r.log.Warn("staged rollout: listing Terminals", "err", err)
		}
		d = r.wait.Hold(d, terms, time.Now())
	}
	r.Last = d
	if d.Update && !r.up.Busy() {
		r.log.Info("staged rollout: updating", "reason", d.Reason, "ref", ref)
		if err := r.up.Start(ref, true, update.PlainHTTP(ref), update.ByRollout); err != nil {
			r.log.Warn("staged rollout", "err", err)
		}
	}
}

// terminalPatience is how long an automatic update waits for a Node's
// running Terminals before it ends them (ADR 0011).
const terminalPatience = 24 * time.Hour

// TerminalWait holds an automatic update back while the Node's Terminals
// run, for up to a day from when it first had to wait. Only automatic
// rollouts wait; an update a person starts warns instead (the Console).
type TerminalWait struct {
	since time.Time
}

// Hold is d, or a decision to wait while any of terms is running and the
// day is not over. It forgets the wait once none is running.
func (w *TerminalWait) Hold(d Decision, terms []*maitrev1.Terminal, now time.Time) Decision {
	if !d.Update {
		return d
	}
	var running []*maitrev1.Terminal
	for _, t := range terms {
		if t.GetRunning() {
			running = append(running, t)
		}
	}
	if len(running) == 0 {
		w.since = time.Time{}
		return d
	}
	if w.since.IsZero() {
		w.since = now
	}
	until := w.since.Add(terminalPatience)
	if !now.Before(until) {
		return Decision{Update: true, Reason: d.Reason + "; its Terminals ran for a day, so they end"}
	}
	return Decision{Reason: "an update is ready; waiting for " + terminalsIn(running) + " to finish (at most until " + until.Format("15:04") + " " + day(until, now) + ")"}
}

// terminalsIn is "a Terminal in nextcloud", "2 Terminals in kuma and vault".
func terminalsIn(running []*maitrev1.Terminal) string {
	var services []string
	seen := map[string]bool{}
	for _, t := range running {
		if !seen[t.GetService()] {
			seen[t.GetService()] = true
			services = append(services, t.GetService())
		}
	}
	sort.Strings(services)
	in := services[0]
	if len(services) > 1 {
		in = strings.Join(services[:len(services)-1], ", ") + " and " + services[len(services)-1]
	}
	if len(running) == 1 {
		return "a Terminal in " + in
	}
	return strconv.Itoa(len(running)) + " Terminals in " + in
}

// day is "today" or "tomorrow": until is at most a day after now.
func day(until, now time.Time) string {
	y1, m1, d1 := until.Date()
	y2, m2, d2 := now.Date()
	if y1 == y2 && m1 == m2 && d1 == d2 {
		return "today"
	}
	return "tomorrow"
}
