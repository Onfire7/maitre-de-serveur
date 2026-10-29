package nodeservice

import (
	"context"
	"os/exec"
	"strconv"
	"strings"

	"connectrpc.com/connect/v2"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
)

// logSources are the journals the Console may read (decision 187): only
// these, never an arbitrary unit. Services are added per Node; a Service of
// several components is listed whole (every component's journal, interleaved)
// and once per component, as "service:<name>/<component>".
var logSources = []struct {
	id, label string
	args      []string
}{
	{"agent", "Maître agent", []string{"-u", "maitre-agent.service"}},
	{"pool", "Storage pool", []string{"-u", "maitre-pool.service"}},
	{"caddy", "Web routes (Caddy)", []string{"-u", "caddy.service"}},
	{"health", "Boot health checks", []string{"-u", "greenboot-healthcheck.service"}},
	{"kernel", "Kernel", []string{"-k", "-b"}},
	{"system", "Everything since boot", []string{"-b"}},
}

// Journal runs journalctl; swapped in tests.
var Journal = func(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "journalctl", args...).CombinedOutput()
	return string(out), err
}

// GetNodeLogs implements NodeService (decision 187).
func (s *Service) GetNodeLogs(ctx context.Context, req *maitrev1.GetNodeLogsRequest) (*maitrev1.GetNodeLogsResponse, error) {
	res := &maitrev1.GetNodeLogsResponse{}
	args := map[string][]string{}
	for _, l := range logSources {
		res.Sources = append(res.Sources, &maitrev1.LogSource{Id: l.id, Label: l.label})
		args[l.id] = l.args
	}
	if s.Services != nil {
		for _, st := range s.Services(ctx) {
			name := st.GetSpec().GetName()
			id := "service:" + name
			res.Sources = append(res.Sources, &maitrev1.LogSource{Id: id, Label: name})
			args[id] = unitArgs(st.GetSpec(), "")
			for _, c := range st.GetSpec().GetComponents() {
				cid := id + "/" + c.GetName()
				res.Sources = append(res.Sources, &maitrev1.LogSource{Id: cid, Label: name + ": " + c.GetName()})
				args[cid] = unitArgs(st.GetSpec(), c.GetName())
			}
		}
	}
	src := req.GetSource()
	if src == "" {
		src = "agent"
	}
	a, ok := args[src]
	if !ok {
		return nil, connect.Errorf(connect.CodeNotFound, "no log source %q on this Node", src)
	}
	n := req.GetLines()
	if n == 0 {
		n = 300
	}
	if n > 5000 {
		n = 5000
	}
	out, err := Journal(ctx, append(append([]string{}, a...), "-n", strconv.Itoa(int(n)), "--no-pager", "-o", "short-iso")...)
	if err != nil {
		return nil, connect.Errorf(connect.CodeUnavailable, "journalctl: %v: %s", err, strings.TrimSpace(out))
	}
	out = strings.TrimRight(out, "\n")
	if out != "" && out != "-- No entries --" {
		res.Lines = strings.Split(out, "\n")
	}
	return res, nil
}

// unitArgs is journalctl's -u flags for a Service, or one of its components.
func unitArgs(spec *maitrev1.ServiceSpec, component string) []string {
	units, _ := services.LogUnits(spec, component)
	var a []string
	for _, u := range units {
		a = append(a, "-u", u)
	}
	return a
}
