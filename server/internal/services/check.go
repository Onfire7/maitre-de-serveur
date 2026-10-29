package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/proto"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// CheckResult is what a Node would make of a Service: the unit files it
// would write, by name, and the options it would need granted first.
type CheckResult struct {
	Units     map[string]string `json:"units"`
	Protected []ProtectedOption `json:"protected,omitempty"`
}

// Check renders a Service as a Node installs it, in a throwaway layout with
// every volume in one Space, and runs podman's Quadlet generator over the
// units (plan C8's second check, and C10's report). Nothing is installed or
// started. It returns quadlet.ErrNoGenerator where Quadlet is not installed;
// CI runs it inside the maitre-server image, which has it.
func Check(ctx context.Context, in *maitrev1.ServiceSpec) (*CheckResult, error) {
	spec := proto.Clone(in).(*maitrev1.ServiceSpec)
	if !nameRe.MatchString(spec.GetName()) {
		return nil, fmt.Errorf("service name %q: use lowercase letters, digits and dashes", spec.GetName())
	}
	if err := validateContainers(spec); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "maitre-check-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	const space = "general"
	pools := filepath.Join(dir, "pools")
	if err := os.MkdirAll(filepath.Join(pools, space), 0o755); err != nil {
		return nil, err
	}
	st, err := store.Open(filepath.Join(dir, "fleet.db"), "check")
	if err != nil {
		return nil, err
	}
	defer st.Close()
	m := New(Config{UnitsDir: filepath.Join(dir, "units"), PoolsDir: pools}, st, checkRunner{})

	spec.VolumeSpaces = map[string]string{}
	for _, v := range spec.GetVolumes() {
		spec.VolumeSpaces[v.GetName()] = space
	}
	if spec.GetWebPort() != 0 {
		spec.WebHostPort = uint32(m.cfg.HostPortBase)
	}
	vols, extra, err := m.layout(ctx, spec)
	if err != nil {
		return nil, err
	}
	res := &CheckResult{}
	if res.Protected, err = protectedOptions(spec, vols, managedDirs(vols, extra)); err != nil {
		return nil, err
	}
	if res.Units, err = m.renderUnits(spec, vols, extra); err != nil {
		return nil, err
	}
	if err := validateUnits(ctx, spec.GetName(), res.Units); err != nil {
		return nil, err
	}
	return res, nil
}

// checkRunner runs nothing: Check only renders.
type checkRunner struct{}

func (checkRunner) Run(context.Context, string, ...string) (string, error) { return "", nil }
