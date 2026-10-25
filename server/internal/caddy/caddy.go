// Package caddy drives the Node's Caddy through its admin API. The agent
// owns the whole config: one HTTP server on :80 routing <service>.<suffix>
// to the loopback port where that Service's web port is published (plain
// HTTP on the LAN; the Console reaches it over the agent's mTLS tunnel,
// decision 71), and, when any Service is Public through the user's own
// domain (decision 112), an HTTPS server on :443 for those hosts with
// certificates Caddy obtains and renews itself. The :80 server also answers
// the ACME HTTP challenge and redirects those hosts to HTTPS.
package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

// Route is one Service's web entry.
type Route struct {
	Host     string // e.g. jellyfin.local
	Upstream string // e.g. 127.0.0.1:20481
}

func proxyRoute(r Route) map[string]any {
	return map[string]any{
		"match": []map[string]any{{"host": []string{r.Host}}},
		"handle": []map[string]any{{
			"handler":   "reverse_proxy",
			"upstreams": []map[string]string{{"dial": r.Upstream}},
		}},
		"terminal": true,
	}
}

// Config builds the Caddy JSON for the LAN routes and the public (own
// domain) routes.
func Config(routes []Route, public ...Route) []byte {
	sort.Slice(routes, func(i, j int) bool { return routes[i].Host < routes[j].Host })
	sort.Slice(public, func(i, j int) bool { return public[i].Host < public[j].Host })
	var rs []map[string]any
	for _, r := range public {
		// Plain HTTP for a public host goes to HTTPS; the ACME HTTP challenge
		// is answered before routing, so this does not get in its way.
		rs = append(rs, map[string]any{
			"match": []map[string]any{{"host": []string{r.Host}}},
			"handle": []map[string]any{{
				"handler":     "static_response",
				"status_code": 308,
				"headers":     map[string][]string{"Location": {"https://{http.request.host}{http.request.uri}"}},
			}},
			"terminal": true,
		})
	}
	for _, r := range routes {
		rs = append(rs, proxyRoute(r))
	}
	// Anything else gets a short plain-text answer so a stray browser sees
	// something meaningful instead of a blank page.
	rs = append(rs, map[string]any{
		"handle": []map[string]any{{
			"handler":     "static_response",
			"status_code": 404,
			"body":        "Maître Server: no Service at this name.\n",
		}},
	})
	servers := map[string]any{
		"private": map[string]any{
			"listen":          []string{":80"},
			"routes":          rs,
			"automatic_https": map[string]any{"disable": true},
		},
	}
	apps := map[string]any{"http": map[string]any{"servers": servers}}
	if len(public) > 0 {
		var ps []map[string]any
		var hosts []string
		for _, r := range public {
			ps = append(ps, proxyRoute(r))
			hosts = append(hosts, r.Host)
		}
		// Automatic HTTPS stays on for this server: Caddy manages a
		// certificate for every host matched here. Redirects are disabled
		// because the :80 server already does them.
		servers["public"] = map[string]any{
			"listen":          []string{":443"},
			"routes":          ps,
			"automatic_https": map[string]any{"disable_redirects": true},
		}
		apps["tls"] = map[string]any{
			"automation": map[string]any{
				"policies": []map[string]any{{
					"subjects": hosts,
					"issuers":  []map[string]any{{"module": "acme"}},
				}},
			},
		}
	}
	cfg := map[string]any{
		"admin": map[string]any{"listen": "127.0.0.1:2019"},
		"apps":  apps,
	}
	b, _ := json.Marshal(cfg)
	return b
}

// Load pushes a config to the admin API, replacing the running one.
func Load(ctx context.Context, adminAddr string, cfg []byte) error {
	if adminAddr == "" {
		adminAddr = "127.0.0.1:2019"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+adminAddr+"/load", bytes.NewReader(cfg))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("caddy admin: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("caddy admin: %s: %s", res.Status, body)
	}
	return nil
}
