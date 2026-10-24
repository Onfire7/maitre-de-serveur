package catalog_test

import (
	"encoding/json"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
)

func TestRepoCatalogLoadsAndRoundTrips(t *testing.T) {
	idx, err := catalog.LoadDir("../../../catalog/entries")
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Entries) != 3 {
		t.Fatalf("got %d entries", len(idx.Entries))
	}
	raw, _ := json.Marshal(idx)
	back, err := catalog.ParseIndex(raw)
	if err != nil {
		t.Fatal(err)
	}
	ag := back.Find("adguard-home")
	if ag == nil || !ag.Infrastructure || len(ag.HostPorts) != 2 || ag.Web.Port != 3000 {
		t.Fatalf("adguard entry wrong: %+v", ag)
	}
	if jf := back.Find("jellyfin"); jf == nil || jf.Settings[0].Type != "timezone" || jf.Volumes[2].Size != "large" {
		t.Fatalf("jellyfin entry wrong: %+v", jf)
	}
}

func TestValidateRejects(t *testing.T) {
	good := catalog.Manifest{ID: "xy", Name: "X", Summary: "s", Image: "docker.io/x/x:1@sha256:" + repeat("a", 64), Exposure: "remote", Quadlet: "[Container]\n"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := good
	bad.Image = "docker.io/x/x:latest"
	if err := bad.Validate(); err == nil {
		t.Fatal("unpinned image accepted")
	}
	bad = good
	bad.Infrastructure = true
	if err := bad.Validate(); err == nil {
		t.Fatal("infrastructure without host ports accepted")
	}
	bad = good
	bad.Volumes = []catalog.Volume{{Name: "data", Path: "/data"}}
	if err := bad.Validate(); err == nil {
		t.Fatal("unused volume accepted")
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
