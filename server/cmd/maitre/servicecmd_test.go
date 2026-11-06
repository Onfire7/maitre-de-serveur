package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A spec written by hand is how a container definition the Catalog does not
// carry reaches a Node (decision 78), so the file is read strictly: the
// whole spec comes back, and a key that is not a field is an error rather
// than a line that silently does nothing.
func TestSpecFromFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	const body = "[Container]\nImage={{ .Image }}\nExec=sh -c \"df -h /hostvar\"\n"
	spec, err := specFromFile(write("good.json", `{
	  "name": "cleanup",
	  "image": "example.invalid/app@sha256:abc",
	  "quadlet": "[Container]\nImage={{ .Image }}\nExec=sh -c \"df -h /hostvar\"\n"
	}`))
	if err != nil {
		t.Fatalf("specFromFile: %v", err)
	}
	if spec.GetName() != "cleanup" {
		t.Errorf("name = %q", spec.GetName())
	}
	if spec.GetImage() != "example.invalid/app@sha256:abc" {
		t.Errorf("image = %q", spec.GetImage())
	}
	if spec.GetQuadlet() != body {
		t.Errorf("quadlet = %q, want %q", spec.GetQuadlet(), body)
	}

	for _, tc := range []struct{ name, body, want string }{
		{"unknown.json", `{"name": "cleanup", "quadlit": "[Container]\n"}`, "quadlit"},
		{"nameless.json", `{"image": "example.invalid/app@sha256:abc"}`, "no name"},
		{"broken.json", `{"name":`, "EOF"},
	} {
		if _, err := specFromFile(write(tc.name, tc.body)); err == nil {
			t.Errorf("%s: no error", tc.name)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want it to mention %q", tc.name, err, tc.want)
		}
	}

	if _, err := specFromFile(filepath.Join(dir, "absent.json")); err == nil {
		t.Error("a missing file gave no error")
	}
}
