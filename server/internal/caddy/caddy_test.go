package caddy_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/caddy"
)

func TestConfigIsValidJSONWithRoutes(t *testing.T) {
	cfg := caddy.Config([]caddy.Route{{Host: "uptime.local", Upstream: "127.0.0.1:20001"}, {Host: "jellyfin.local", Upstream: "127.0.0.1:20002"}})
	var v map[string]any
	if err := json.Unmarshal(cfg, &v); err != nil {
		t.Fatal(err)
	}
	s := string(cfg)
	if strings.Index(s, "jellyfin") > strings.Index(s, "uptime") || !strings.Contains(s, `"dial":"127.0.0.1:20002"`) || !strings.Contains(s, `"listen":[":80"]`) {
		t.Fatalf("unexpected config: %s", s)
	}
}

func TestConfigPublicHostsGetHTTPSAndRedirect(t *testing.T) {
	cfg := caddy.Config([]caddy.Route{{Host: "photos.local", Upstream: "127.0.0.1:20001"}}, caddy.Route{Host: "photos.example.com", Upstream: "127.0.0.1:20001"})
	var v struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Listen []string         `json:"listen"`
					Routes []map[string]any `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
			TLS struct {
				Automation struct {
					Policies []struct {
						Subjects []string `json:"subjects"`
					} `json:"policies"`
				} `json:"automation"`
			} `json:"tls"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(cfg, &v); err != nil {
		t.Fatal(err)
	}
	pub, ok := v.Apps.HTTP.Servers["public"]
	if !ok || len(pub.Listen) != 1 || pub.Listen[0] != ":443" || len(pub.Routes) != 1 {
		t.Fatalf("public server: %+v", pub)
	}
	if len(v.Apps.TLS.Automation.Policies) != 1 || v.Apps.TLS.Automation.Policies[0].Subjects[0] != "photos.example.com" {
		t.Fatalf("tls policy: %+v", v.Apps.TLS.Automation.Policies)
	}
	// The :80 server turns away stray hops, redirects the public host and
	// still routes the LAN name.
	priv := v.Apps.HTTP.Servers["private"]
	if len(priv.Routes) != 4 {
		t.Fatalf("private routes: %d", len(priv.Routes))
	}
	if !strings.Contains(string(cfg), `"status_code":308`) {
		t.Fatalf("no redirect: %s", cfg)
	}
	// No public host: no tls app at all.
	if strings.Contains(string(caddy.Config([]caddy.Route{{Host: "a.local", Upstream: "127.0.0.1:1"}})), `"tls"`) {
		t.Fatal("tls app present without public hosts")
	}
}

func TestConfigWithEmailGivesItToTheIssuer(t *testing.T) {
	cfg := caddy.ConfigWithEmail("ops@example.com", []caddy.Route{{Host: "photos.local", Upstream: "127.0.0.1:20001"}}, caddy.Route{Host: "photos.example.com", Upstream: "127.0.0.1:20001"})
	if !strings.Contains(string(cfg), `"issuers":[{"email":"ops@example.com","module":"acme"}]`) {
		t.Fatalf("issuer without the email: %s", cfg)
	}
	if strings.Contains(string(caddy.Config([]caddy.Route{{Host: "photos.local", Upstream: "127.0.0.1:20001"}}, caddy.Route{Host: "photos.example.com", Upstream: "127.0.0.1:20001"})), `"email"`) {
		t.Fatal("email present when none was given")
	}
}

func TestHopsReachTheServiceWhereverItRuns(t *testing.T) {
	cfg := string(caddy.Build(caddy.Site{
		Routes: []caddy.Route{
			{Host: "jellyfin.local", Upstream: "127.0.0.1:20001"},
			{Host: "immich.local", Upstream: "192.168.1.20:80", Hop: "immich"},
		},
		Plain: []caddy.Route{{Host: "photos.example.com", Upstream: "192.168.1.20:80", Hop: "immich"}},
		Here:  []caddy.Route{{Host: "jellyfin", Upstream: "127.0.0.1:20001"}},
		Peers: []string{"192.168.1.20", "fd00::20"},
	}))
	for _, want := range []string{
		// A Service on a peer goes to the peer's Caddy, naming it.
		`"headers":{"request":{"set":{"X-Maitre-Hop":["immich"]}}}`,
		`"dial":"192.168.1.20:80"`,
		// A peer's hop for this Node's own Service is answered here, from
		// the Fleet's Nodes only, and the app never sees the header...
		`"match":[{"header":{"X-Maitre-Hop":["jellyfin"]},"remote_ip":{"ranges":["127.0.0.0/8","::1/128","192.168.1.20/32","fd00::20/128"]}}]`,
		`"headers":{"request":{"delete":["X-Maitre-Hop"]}}`,
		// ...and any other hop stops here rather than looping.
		`"header_regexp":{"X-Maitre-Hop":{"pattern":"."}}`,
		// Forwarded headers are kept from another Node only.
		`"trusted_proxies":{"ranges":["127.0.0.0/8","::1/128","192.168.1.20/32","fd00::20/128"],"source":"static"}`,
		// LAN names answer the LAN only: the router may forward 80 here.
		`"match":[{"host":["jellyfin.local"],"remote_ip":{"ranges":["127.0.0.0/8"`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("missing %s in %s", want, cfg)
		}
	}
	// A public host served in plain HTTP here is not redirected, and gets no certificate.
	if strings.Contains(cfg, `"status_code":308`) || strings.Contains(cfg, `"tls"`) {
		t.Errorf("plain host redirected or certified: %s", cfg)
	}
	// Hops come before the host routes, so a hop is never passed on.
	if strings.Index(cfg, `"pattern":"."`) > strings.Index(cfg, `"host":["immich.local"]`) {
		t.Errorf("hop guard after the host routes: %s", cfg)
	}
}
