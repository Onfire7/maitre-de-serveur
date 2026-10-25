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
