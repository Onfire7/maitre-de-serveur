package catalog_test

import (
	"strings"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
)

func TestSupport(t *testing.T) {
	for option, want := range map[string]string{
		"Network=host":                     "",
		"AddDevice=/dev/dri/renderD128":    "",
		"AddDevice=/dev/dri:/dev/dri:rwm":  "",
		"AddDevice=/dev/ttyUSB0":           "container_use_devices",
		"AddCapability=NET_ADMIN":          "",
		"Volume=/srv/media":                "",
		"Volume=/run/dbus":                 "BlueZ",
		"Volume=/run/podman/podman.sock":   "Podman socket",
		"Network=container:db":             "lifecycle",
		"PodmanArgs=--privileged":          "raw Podman arguments",
		"SecurityLabelDisable=true":        "SELinux",
		"[Service] ExecStartPre=/bin/true": "root on the Node",
		"Frobnicate=yes":                   "classify the Quadlet key Frobnicate",
	} {
		ok, needs := catalog.Support(option)
		if want == "" && (!ok || needs != "") || want != "" && (ok || !strings.Contains(needs, want)) {
			t.Errorf("%s: %v %q, want %q", option, ok, needs, want)
		}
	}
}
