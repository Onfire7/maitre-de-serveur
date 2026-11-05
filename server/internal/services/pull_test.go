package services_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// pullRunner scripts a registry and a store: what a tag resolves to now,
// what podman reports after a pull, and how much room the store has.
type pullRunner struct {
	index    string // the digest the tag itself resolves to (an image index)
	platform string // the digest of this platform's entry inside that index
	pulled   string // what podman image inspect says once something was pulled
	skopeoNo bool   // the registry cannot be reached
	cmds     []string
}

func (r *pullRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	line := name + " " + strings.Join(args, " ")
	r.cmds = append(r.cmds, line)
	switch {
	case name == "skopeo" && r.skopeoNo:
		return "", errUnreachable
	case name == "skopeo" && contains(args, "--format"):
		return r.index + "\n", nil
	case name == "skopeo" && contains(args, "--raw"):
		if r.platform == "" {
			return `{"mediaType":"application/vnd.oci.image.manifest.v1+json"}`, nil
		}
		// Another platform first, so a check that takes the first entry
		// rather than its own fails here.
		return `{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[` +
			`{"digest":"sha256:` + strings.Repeat("9", 64) + `","platform":{"os":"linux","architecture":"ppc64le"}},` +
			`{"digest":"` + r.platform + `","platform":{"os":"linux","architecture":"amd64"}}]}`, nil
	case name == "podman" && len(args) > 1 && args[0] == "image" && args[1] == "inspect":
		return r.pulled + "\n", nil
	case name == "systemctl" && len(args) > 1 && args[0] == "show":
		return "LoadState=loaded\nActiveState=active\nSubState=running\nNRestarts=0\nResult=success\n", nil
	}
	return "", nil
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

type unreachable struct{}

func (unreachable) Error() string { return "dial tcp: no route to host" }

var errUnreachable = unreachable{}

func (r *pullRunner) pulls() int {
	n := 0
	for _, c := range r.cmds {
		if strings.HasPrefix(c, "podman pull") {
			n++
		}
	}
	return n
}

// installed puts a Service of this Node in the store at a pinned digest,
// without going through Install (which would render units).
func installed(t *testing.T, st *store.Store, digest string) *maitrev1.ServiceSpec {
	t.Helper()
	spec := single("grafana", 3000)
	spec.NodeId = "node-a"
	spec.Image = "docker.io/grafana/grafana:11@" + digest
	spec.AutoUpdate = true
	if _, err := st.Put(context.Background(), services.KindService, spec.GetName(), spec, true); err != nil {
		t.Fatal(err)
	}
	return spec
}

// Decision 315: the daily sweep used to download every image to learn its
// digest. Asking the registry costs no layers, and a tag that still names
// the pinned digest moves nothing at all.
func TestAnUpToDateServiceIsNotPulled(t *testing.T) {
	platform := "sha256:" + strings.Repeat("a", 64)
	run := &pullRunner{index: "sha256:" + strings.Repeat("b", 64), platform: platform}
	m, st := managerOver(t, run)
	spec := installed(t, st, platform)
	changed, err := m.Update(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("nothing changed upstream, yet Update reported a change")
	}
	if run.pulls() != 0 {
		t.Fatalf("pulled anyway: %v", run.cmds)
	}
}

// The trap this check exists for: a pinned digest may name the index or
// one platform's manifest, and either one being current means current.
func TestAnIndexDigestCountsAsUpToDateToo(t *testing.T) {
	index := "sha256:" + strings.Repeat("b", 64)
	run := &pullRunner{index: index, platform: "sha256:" + strings.Repeat("a", 64)}
	m, st := managerOver(t, run)
	if changed, err := m.Update(context.Background(), installed(t, st, index)); err != nil || changed {
		t.Fatalf("changed=%v err=%v; the index digest is the tag's own", changed, err)
	}
	if run.pulls() != 0 {
		t.Fatalf("pulled against the index digest: %v", run.cmds)
	}
}

// A single-manifest image has no index: the tag's digest is the manifest's
// own, and that is what gets pinned.
func TestASingleManifestImageIsRecognised(t *testing.T) {
	only := "sha256:" + strings.Repeat("c", 64)
	run := &pullRunner{index: only}
	m, st := managerOver(t, run)
	if changed, err := m.Update(context.Background(), installed(t, st, only)); err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if run.pulls() != 0 {
		t.Fatalf("pulled a single-manifest image that had not moved: %v", run.cmds)
	}
}

func TestAMovedTagIsPulledOnce(t *testing.T) {
	moved := "sha256:" + strings.Repeat("d", 64)
	run := &pullRunner{index: "sha256:" + strings.Repeat("b", 64), platform: moved, pulled: moved}
	m, st := managerOver(t, run)
	spec := installed(t, st, "sha256:"+strings.Repeat("a", 64))
	changed, err := m.Update(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("the tag moved and Update reported no change")
	}
	if run.pulls() != 1 {
		t.Fatalf("want exactly one pull, got %d: %v", run.pulls(), run.cmds)
	}
	if !strings.HasSuffix(spec.GetImage(), moved) {
		t.Fatalf("spec not re-pinned: %s", spec.GetImage())
	}
	if !strings.HasSuffix(spec.GetPreviousImage(), strings.Repeat("a", 64)) {
		t.Fatalf("the old digest was not kept for Rollback: %s", spec.GetPreviousImage())
	}
}

// The check must never cost correctness: a registry that cannot be reached
// falls back to the pull that was the only way before.
func TestAnUnreachableRegistryStillPulls(t *testing.T) {
	moved := "sha256:" + strings.Repeat("d", 64)
	run := &pullRunner{skopeoNo: true, pulled: moved}
	m, st := managerOver(t, run)
	if _, err := m.Update(context.Background(), installed(t, st, "sha256:"+strings.Repeat("a", 64))); err != nil {
		t.Fatal(err)
	}
	if run.pulls() != 1 {
		t.Fatalf("want the fallback pull, got %d: %v", run.pulls(), run.cmds)
	}
}

// Decision 318 before the network, not after it: Boulder asked the registry
// about 16 Services it owned none of, then refused every one.
func TestAnotherNodesServiceIsNotEvenLookedUp(t *testing.T) {
	run := &pullRunner{index: "sha256:" + strings.Repeat("b", 64)}
	m, st := managerOver(t, run)
	spec := installed(t, st, "sha256:"+strings.Repeat("a", 64))
	spec.NodeId = "node-b-aaaaaaaa"
	if _, err := st.Put(context.Background(), services.KindService, spec.GetName(), spec, false); err != nil {
		t.Fatal(err)
	}
	_, err := m.Update(context.Background(), spec)
	if err == nil {
		t.Fatal("Update ran for another Node's Service")
	}
	if !strings.Contains(err.Error(), "belongs to another Node") {
		t.Fatalf("message does not name the owner: %v", err)
	}
	for _, c := range run.cmds {
		if strings.HasPrefix(c, "skopeo") || strings.HasPrefix(c, "podman pull") {
			t.Fatalf("the network was used for it: %v", run.cmds)
		}
	}
}

// managerWithSpace is managerOver with a say in what the image store has
// left, since no test has a store that can actually fill.
func managerWithSpace(t *testing.T, run services.Runner, free func(string) uint64) (*services.Manager, *store.Store) {
	t.Helper()
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	t.Cleanup(func() { st.Close() })
	dir := t.TempDir()
	pools := filepath.Join(dir, "pools")
	_ = os.MkdirAll(filepath.Join(pools, "general"), 0o755)
	m := services.New(services.Config{
		UnitsDir: filepath.Join(dir, "units"), SystemdDir: filepath.Join(dir, "systemd"), PoolsDir: pools,
		DefaultSpace: func(context.Context) string { return "general" }, HostPortBase: 20000,
		FreeBytes: free,
	}, st, run)
	return m, st
}

// Decision 316's trigger: free space crossing a threshold, not a schedule.
// A store below the floor is pruned, and the pull then goes ahead.
func TestALowStoreIsPrunedBeforePulling(t *testing.T) {
	moved := "sha256:" + strings.Repeat("d", 64)
	run := &pullRunner{index: "sha256:" + strings.Repeat("b", 64), platform: moved, pulled: moved}
	calls := 0
	m, st := managerWithSpace(t, run, func(string) uint64 {
		calls++
		if calls == 1 {
			return 1 << 30 // below the floor
		}
		return 40 << 30 // the prune gave the room back
	})
	if _, err := m.Update(context.Background(), installed(t, st, "sha256:"+strings.Repeat("a", 64))); err != nil {
		t.Fatal(err)
	}
	pruned := false
	for _, c := range run.cmds {
		if strings.HasPrefix(c, "podman images --all") {
			pruned = true
		}
	}
	if !pruned {
		t.Fatalf("a store below the floor was not pruned: %v", run.cmds)
	}
	if run.pulls() != 1 {
		t.Fatalf("want the pull to go ahead after pruning, got %d: %v", run.pulls(), run.cmds)
	}
}

// And when pruning cannot help, the pull is refused with the numbers in
// the message, instead of ENOSPC thirty-five seconds into a download.
func TestAPullIsRefusedWhenPruningCannotHelp(t *testing.T) {
	moved := "sha256:" + strings.Repeat("d", 64)
	run := &pullRunner{index: "sha256:" + strings.Repeat("b", 64), platform: moved, pulled: moved}
	m, st := managerWithSpace(t, run, func(string) uint64 { return 1 << 30 })
	_, err := m.Update(context.Background(), installed(t, st, "sha256:"+strings.Repeat("a", 64)))
	if err == nil {
		t.Fatal("pulled into a full store")
	}
	for _, want := range []string{"1.0 GiB", "4.0 GiB", "prune -drop-rollback"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message does not say %q: %v", want, err)
		}
	}
	if run.pulls() != 0 {
		t.Fatalf("pulled anyway: %v", run.cmds)
	}
}

// Nothing is refused on a measurement that could not be taken: statfs
// answering 0 means unknown, not full.
func TestAnUnmeasurableStoreDoesNotRefuseAPull(t *testing.T) {
	moved := "sha256:" + strings.Repeat("d", 64)
	run := &pullRunner{index: "sha256:" + strings.Repeat("b", 64), platform: moved, pulled: moved}
	m, st := managerWithSpace(t, run, func(string) uint64 { return 0 })
	if _, err := m.Update(context.Background(), installed(t, st, "sha256:"+strings.Repeat("a", 64))); err != nil {
		t.Fatal(err)
	}
	if run.pulls() != 1 {
		t.Fatalf("want one pull, got %d: %v", run.pulls(), run.cmds)
	}
}

// Decision 317: a Service on a Tunnel says whose network it is on, so the
// Console can mark the figure shared instead of splitting or hiding it.
func TestAServiceOnATunnelSaysWhoseNetworkItShares(t *testing.T) {
	ctx := context.Background()
	run := &pullRunner{}
	m, st := managerOver(t, run)
	member := single("qbittorrent", 8080)
	member.NodeId = "node-a"
	member.NetworkKind, member.NetworkTarget = services.NetworkTunnel, "gluetun"
	if _, err := st.Put(ctx, services.KindService, member.GetName(), member, true); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status(ctx, member)
	if err != nil {
		t.Fatal(err)
	}
	if status.GetNetworkSharedWith() != "gluetun" {
		t.Fatalf("want gluetun, got %q", status.GetNetworkSharedWith())
	}
	// A Service with a network of its own shares nobody's.
	own := single("memos", 5230)
	own.NodeId = "node-a"
	if _, err := st.Put(ctx, services.KindService, own.GetName(), own, true); err != nil {
		t.Fatal(err)
	}
	if status, err = m.Status(ctx, own); err != nil {
		t.Fatal(err)
	}
	if status.GetNetworkSharedWith() != "" {
		t.Fatalf("want no sharing, got %q", status.GetNetworkSharedWith())
	}
}
