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
	// The :80 server redirects the public host first and still routes the LAN name.
	priv := v.Apps.HTTP.Servers["private"]
	if len(priv.Routes) != 3 {
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
