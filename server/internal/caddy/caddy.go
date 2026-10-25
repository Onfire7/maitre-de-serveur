// Package caddy drives the Node's Caddy through its admin API. The agent
// owns the whole config: one HTTP server on :80 routing <service>.<suffix>
// to the loopback port where that Service's web port is published. Private
// exposure only in Milestone 2 (plain HTTP on the LAN; the Console reaches it
// over the agent's mTLS tunnel, decision 71).
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

// Config builds the Caddy JSON for a set of routes.
func Config(routes []Route) []byte {
	sort.Slice(routes, func(i, j int) bool { return routes[i].Host < routes[j].Host })
	var rs []map[string]any
	for _, r := range routes {
		rs = append(rs, map[string]any{
			"match": []map[string]any{{"host": []string{r.Host}}},
			"handle": []map[string]any{{
				"handler":   "reverse_proxy",
				"upstreams": []map[string]string{{"dial": r.Upstream}},
			}},
			"terminal": true,
		})
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
	cfg := map[string]any{
		"admin": map[string]any{"listen": "127.0.0.1:2019"},
		"apps": map[string]any{
			"http": map[string]any{
				"servers": map[string]any{
					"private": map[string]any{
						"listen":          []string{":80"},
						"routes":          rs,
						"automatic_https": map[string]any{"disable": true},
					},
				},
			},
		},
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
