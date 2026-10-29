// Package entry elects the Node the home router forwards to (decision 218):
// one for the app (7443) and one for the web (80 and 443, Caddy). Every
// Node runs the same election over what it hears by gossip, so they agree
// without a vote: a pinned Node while it is reachable, else the Node that
// already holds the role, kept until it goes away or stays much busier than
// another, else the least busy. Busyness is counted in coarse steps so
// small differences in what each Node last heard do not split the vote.
package entry

import (
	"context"
	"slices"
	"sort"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// Record kind and id of the Fleet's domain settings.
const (
	Kind = "fleet_domain"
	ID   = "fleet"
)

// Roles.
const (
	App = "app"
	Web = "web"
)

// AppPort is where the app reaches a Node; the router forwards it as is.
const AppPort = 7443

// Candidate is one Node as this Node sees it.
type Candidate struct {
	ID        string
	Reachable bool
	// Busy is Busyness of its last metrics.
	Busy int
	// Holds is the roles it said it holds.
	Holds []string
}

// step is how many percentage points one step of busyness spans.
const step = 20

// handover is how many steps busier than the least busy Node a holder
// must be before the role moves.
const handover = 2

// Busyness is the higher of CPU and memory use in steps of 20%: 0 for a
// quiet Node, 4 or 5 for a full one. A Node without metrics counts as
// quiet.
func Busyness(m *maitrev1.NodeMetrics) int {
	if m == nil {
		return 0
	}
	pct := m.GetCpuPercent()
	if t := m.GetMemoryTotalBytes(); t > 0 {
		if mem := float64(m.GetMemoryUsedBytes()) * 100 / float64(t); mem > pct {
			pct = mem
		}
	}
	return int(pct) / step
}

func holds(c Candidate, role string) bool {
	for _, r := range c.Holds {
		if r == role {
			return true
		}
	}
	return false
}

// Elect returns the Node that should hold role, "" when it is off or no
// Node is reachable.
func Elect(e *maitrev1.EntryNode, role string, nodes []Candidate) string {
	if !e.GetEnabled() {
		return ""
	}
	var up []Candidate
	for _, c := range nodes {
		if c.Reachable {
			up = append(up, c)
		}
	}
	if len(up) == 0 {
		return ""
	}
	for _, c := range up {
		if c.ID == e.GetNodeId() {
			return c.ID
		}
	}
	sort.Slice(up, func(i, j int) bool {
		if up[i].Busy != up[j].Busy {
			return up[i].Busy < up[j].Busy
		}
		return up[i].ID < up[j].ID
	})
	least := up[0].Busy
	// The holder keeps it while it is not much busier; two holders (a
	// handover under way) settle on the quieter, then the lower id.
	for _, c := range up {
		if holds(c, role) && c.Busy-least < handover {
			return c.ID
		}
	}
	return up[0].ID
}

// Lookup resolves a host name to its addresses.
type Lookup func(ctx context.Context, host string) ([]string, error)

// CheckDNS says what is missing for domain to reach the home at publicIP,
// "" when nothing is: the record for the domain and, for the web, one name
// under it (the wildcard *.domain). publicIP "" (not learned yet) only asks
// for records to exist.
func CheckDNS(ctx context.Context, lookup Lookup, domain, publicIP string, wildcard bool) string {
	names := []string{domain}
	if wildcard {
		names = append(names, "maitre-check."+domain)
	}
	shown := []string{domain, "*." + domain}
	for i, n := range names {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		addrs, err := lookup(cctx, n)
		cancel()
		if err != nil || len(addrs) == 0 {
			if publicIP == "" {
				return "waiting for DNS: no record for " + shown[i] + " yet"
			}
			return "waiting for DNS: point " + shown[i] + " at " + publicIP
		}
		if publicIP != "" && !slices.Contains(addrs, publicIP) {
			return shown[i] + " points at " + addrs[0] + ", not at " + publicIP
		}
	}
	return ""
}
