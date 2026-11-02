package convert

import "testing"

// A tag's leading v goes only before a digit: LinuxServer's DokuWiki is
// tagged version-2026-07-14c, which is not v plus ersion-2026-07-14c.
func TestImageTag(t *testing.T) {
	for in, want := range map[string]string{
		"codeberg.org/forgejo/forgejo:17.0.1@sha256:abc":                "17.0.1",
		"ghcr.io/flaresolverr/flaresolverr:v3.4.6":                      "3.4.6",
		"docker.io/linuxserver/dokuwiki:version-2026-07-14c@sha256:abc": "version-2026-07-14c",
		"docker.io/library/postgres:16-alpine":                          "16-alpine",
		"docker.io/library/postgres":                                    "",
	} {
		if got := imageTag(in); got != want {
			t.Errorf("imageTag(%q) = %q, want %q", in, got, want)
		}
	}
}
