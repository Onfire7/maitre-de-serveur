package direct

import "testing"

func TestValidHost(t *testing.T) {
	for _, ok := range []string{"photos.example.com", "a.bc", "x-y.example.co.uk"} {
		if !ValidHost(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "localhost", "Photos.Example.com", "photos.example.com.", "-a.example.com", "a..b", "a.b/c", "jellyfin.local"} {
		if ValidHost(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
