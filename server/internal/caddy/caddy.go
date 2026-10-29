// Package caddy drives the Node's Caddy through its admin API. The agent
// owns the whole config: one HTTP server on :80 routing <service>.<suffix>
// to the loopback port where that Service's web port is published (plain
// HTTP on the LAN; the Console reaches it over the agent's mTLS tunnel,
// decision 71), and, when any Service is Public through the user's own
// domain (decision 112), an HTTPS server on :443 for those hosts with
// certificates Caddy obtains and renews itself. The :80 server also answers
// the ACME HTTP challenge and redirects those hosts to HTTPS.
//
// Decision 218: every Node routes every Service in the Fleet. A Service on
// another Node is proxied to that Node's Caddy with X-Maitre-Hop naming it;
// a hop is answered only by the Node that runs the Service, never passed
// on, so traffic that reached the wrong Node still arrives and cannot loop.
package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"time"
)

// HopHeader names the Service a request is for when one Node's Caddy hands
// it to the Node that runs it (decision 218).
const HopHeader = "X-Maitre-Hop"

// Route is one Service's web entry.
type Route struct {
	Host     string // e.g. jellyfin.local
	Upstream string // e.g. 127.0.0.1:20481, or a peer's Caddy, 192.168.1.20:80
	// Hop is set when Upstream is another Node's Caddy: the Service's name
	// there, sent as X-Maitre-Hop.
	Hop string
}

// Site is everything one Node's Caddy answers.
type Site struct {
	// Account email for the certificate authority (decision 114); "" for none.
	ACMEEmail string
	// LAN names (<service>.local) of every Service in the Fleet.
	Routes []Route
	// Hosts at the user's domain served over HTTPS here, with certificates.
	Public []Route
	// Hosts at the user's domain answered over plain HTTP here, without a
	// redirect: this Node is not where the router sends the web (decision
	// 218), but a request that reaches it is still served.
	Plain []Route
	// This Node's own Services by name (Host is the name), answering hops
	// from other Nodes.
	Here []Route
	// The other Nodes' addresses: only they may hop, and their forwarded
	// headers (the client's address, scheme and host) are kept.
	Peers []string
}

// lan are the client addresses LAN names (<service>.local) answer. The
// router may forward 80 here from the internet (decision 115, 218); a
// stranger naming a Private Service must not reach it.
var lan = []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fc00::/7", "fe80::/10"}

// hosts are the Fleet's own Nodes as address ranges: loopback and each peer.
func (s Site) hosts() []string {
	out := []string{"127.0.0.0/8", "::1/128"}
	for _, p := range s.Peers {
		switch ip := net.ParseIP(p); {
		case ip == nil:
		case ip.To4() != nil:
			out = append(out, ip.String()+"/32")
		default:
			out = append(out, ip.String()+"/128")
		}
	}
	return out
}

func proxyRoute(r Route) map[string]any {
	return map[string]any{
		"match":    []map[string]any{{"host": []string{r.Host}}},
		"handle":   []map[string]any{proxy(r)},
		"terminal": true,
	}
}

func proxy(r Route) map[string]any {
	h := map[string]any{
		"handler":   "reverse_proxy",
		"upstreams": []map[string]string{{"dial": r.Upstream}},
	}
	if r.Hop != "" {
		h["headers"] = map[string]any{"request": map[string]any{"set": map[string][]string{HopHeader: {r.Hop}}}}
	} else {
		// The app never sees a hop.
		h["headers"] = map[string]any{"request": map[string]any{"delete": []string{HopHeader}}}
	}
	return h
}

// Config builds the Caddy JSON for the LAN routes and the public (own
// domain) routes.
func Config(routes []Route, public ...Route) []byte {
	return ConfigWithEmail("", routes, public...)
}

// ConfigWithEmail is Config with an optional account email for the
// certificate authority (decision 114): Let's Encrypt sends expiry and
// policy notices there. Empty means no email.
func ConfigWithEmail(acmeEmail string, routes []Route, public ...Route) []byte {
	return Build(Site{ACMEEmail: acmeEmail, Routes: routes, Public: public})
}

// Build is the Caddy JSON for a Site.
func Build(site Site) []byte {
	acmeEmail, routes, public := site.ACMEEmail, site.Routes, site.Public
	byHost := func(rs []Route) {
		sort.Slice(rs, func(i, j int) bool { return rs[i].Host < rs[j].Host })
	}
	byHost(routes)
	byHost(public)
	byHost(site.Plain)
	byHost(site.Here)
	var rs []map[string]any
	// Hops first: a peer handing over a request for a Service here. Any
	// other hop is for a Service this Node does not run (it moved, or the
	// peer's view is behind): answered at once, never passed on.
	nodes := site.hosts()
	for _, r := range site.Here {
		rs = append(rs, map[string]any{
			"match":    []map[string]any{{"header": map[string][]string{HopHeader: {r.Host}}, "remote_ip": map[string]any{"ranges": nodes}}},
			"handle":   []map[string]any{proxy(Route{Upstream: r.Upstream})},
			"terminal": true,
		})
	}
	rs = append(rs, map[string]any{
		"match": []map[string]any{{"header_regexp": map[string]any{HopHeader: map[string]string{"pattern": "."}}}},
		"handle": []map[string]any{{
			"handler":     "static_response",
			"status_code": 502,
			"body":        "Maître Server: that Service is not on this Node.\n",
		}},
		"terminal": true,
	})
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
	for _, r := range site.Plain {
		rs = append(rs, proxyRoute(r))
	}
	for _, r := range routes {
		lr := proxyRoute(r)
		lr["match"] = []map[string]any{{"host": []string{r.Host}, "remote_ip": map[string]any{"ranges": lan}}}
		rs = append(rs, lr)
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
			"trusted_proxies": map[string]any{"source": "static", "ranges": nodes},
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
					"issuers":  []map[string]any{issuer(acmeEmail)},
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

func issuer(email string) map[string]any {
	is := map[string]any{"module": "acme"}
	if email != "" {
		is["email"] = email
	}
	return is
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
