package services_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/quadlet"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
	"github.com/Onfire7/maitre-de-serveur/server/internal/store"
)

// A two-component Service (a web app and its database) renders one unit per
// component, a shared network and a grouping target, all of which pass
// podman's own Quadlet generator; its lifecycle acts on the target.
func multiSpec() *maitrev1.ServiceSpec {
	return &maitrev1.ServiceSpec{
		Name: "notes",
		Components: []*maitrev1.ServiceComponent{
			{
				Name:    "app",
				Image:   "docker.io/example/notes:1@sha256:" + strings.Repeat("a", 64),
				Quadlet: "[Container]\nImage={{ .Image }}\nEnvironment=DB_HOST=db\nVolume={{ .Volumes.data }}:/data:Z\n",
				After:   []string{"db"},
			},
			{
				Name:        "db",
				Image:       "docker.io/example/postgres:16@sha256:" + strings.Repeat("b", 64),
				Quadlet:     "[Container]\nImage={{ .Image }}\nVolume={{ .Volumes.db }}:/var/lib/postgresql/data:Z\n",
				WaitHealthy: true,
			},
		},
		WebComponent: "app",
		WebPort:      3000,
		WebPath:      "/",
		Volumes: []*maitrev1.ServiceVolume{
			{Name: "data", Path: "/data"},
			{Name: "db", Path: "/var/lib/postgresql/data"},
		},
	}
}

func newMultiManager(t *testing.T, run services.Runner) (*services.Manager, *store.Store, string) {
	t.Helper()
	st, _ := store.Open(filepath.Join(t.TempDir(), "fleet.db"), "node-a")
	t.Cleanup(func() { st.Close() })
	dir := t.TempDir()
	pools := filepath.Join(dir, "pools")
	if err := os.MkdirAll(filepath.Join(pools, "general"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := services.New(services.Config{
		UnitsDir: filepath.Join(dir, "units"), PoolsDir: pools,
		DefaultSpace: func(context.Context) string { return "general" }, HostPortBase: 20000,
		ValidateUnits: quadlet.Generator() != "",
	}, st, run)
	return m, st, dir
}

func TestMultiContainerInstall(t *testing.T) {
	ctx := context.Background()
	run := &fakeRunner{state: "active"}
	m, _, dir := newMultiManager(t, run)

	st, err := m.Install(ctx, multiSpec())
	if err != nil {
		t.Fatal(err)
	}
	if st.GetState() != maitrev1.ServiceStatus_STATE_RUNNING {
		t.Fatalf("state %v", st.GetState())
	}
	if len(st.GetComponents()) != 2 {
		t.Fatalf("components %d", len(st.GetComponents()))
	}

	units := filepath.Join(dir, "units")
	app, err := os.ReadFile(filepath.Join(units, "notes-app.container"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"HostName=app",
		"Network=maitre-notes.network:alias=app",
		"Network=maitre.network:alias=notes",
		"Requires=notes-db.service",
		"After=notes-db.service",
		"PublishPort=127.0.0.1:" + itoa(int(st.GetSpec().GetWebHostPort())) + ":3000",
		"Label=dev.maitre.component=app",
		"PartOf=notes.target",
		"WantedBy=notes.target",
		"NoNewPrivileges=true",
	} {
		if !strings.Contains(string(app), want) {
			t.Errorf("app unit missing %q:\n%s", want, app)
		}
	}
	// The database waits for its own health and does not publish a web port.
	db, _ := os.ReadFile(filepath.Join(units, "notes-db.container"))
	if !strings.Contains(string(db), "Notify=healthy") {
		t.Errorf("db unit missing Notify=healthy:\n%s", db)
	}
	if strings.Contains(string(db), "maitre.network") {
		t.Errorf("non-web component joined maitre.network:\n%s", db)
	}
	// The shared network and the grouping target were written.
	if _, err := os.Stat(filepath.Join(units, "maitre-notes.network")); err != nil {
		t.Error("service network not written")
	}
	target, err := os.ReadFile(filepath.Join(units, "notes.target"))
	if err != nil || !strings.Contains(string(target), "[Install]") {
		t.Errorf("target: %v\n%s", err, target)
	}
	// Both volume directories were created under the Service's folder.
	for _, v := range []string{"data", "db"} {
		if _, err := os.Stat(filepath.Join(dir, "pools", "general", ".Services", "notes", v)); err != nil {
			t.Errorf("volume %s dir not created", v)
		}
	}
	// Lifecycle acts on the target, not a single .service.
	cmds := strings.Join(run.cmds, "\n")
	if !strings.Contains(cmds, "systemctl start --no-block notes.target") {
		t.Fatalf("did not start the target:\n%s", cmds)
	}
}

func TestMultiContainerRemove(t *testing.T) {
	ctx := context.Background()
	run := &fakeRunner{state: "active"}
	m, _, dir := newMultiManager(t, run)
	if _, err := m.Install(ctx, multiSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Control(ctx, &maitrev1.ControlServiceRequest{Name: "notes", Action: maitrev1.ControlServiceRequest_ACTION_REMOVE}); err != nil {
		t.Fatal(err)
	}
	units := filepath.Join(dir, "units")
	for _, f := range []string{"notes-app.container", "notes-db.container", "maitre-notes.network", "notes.target"} {
		if _, err := os.Stat(filepath.Join(units, f)); !os.IsNotExist(err) {
			t.Errorf("%s not removed", f)
		}
	}
}

// A failed component fails the whole Service and names the component.
func TestMultiContainerStatusRollup(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newMultiManager(t, &perUnitRunner{states: map[string]string{
		"notes-app.service": "active",
		"notes-db.service":  "failed",
	}})
	if _, err := m.Install(ctx, multiSpec()); err != nil {
		t.Fatal(err)
	}
	st, err := m.Status(ctx, multiSpec())
	if err != nil {
		t.Fatal(err)
	}
	if st.GetState() != maitrev1.ServiceStatus_STATE_FAILED {
		t.Fatalf("state %v", st.GetState())
	}
	if !strings.Contains(st.GetMessage(), "db") {
		t.Fatalf("message does not name the failed component: %q", st.GetMessage())
	}
}

// perUnitRunner answers systemctl show per unit from a scripted state map, so
// a test can fail one component and see the rollup.
type perUnitRunner struct {
	states map[string]string
	cmds   []string
}

func (r *perUnitRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	r.cmds = append(r.cmds, name+" "+strings.Join(args, " "))
	if name == "systemctl" && len(args) > 1 && args[0] == "show" {
		state := "active"
		if s, ok := r.states[args[1]]; ok {
			state = s
		}
		return "ActiveState=" + state + "\nSubState=x\nNRestarts=0\nResult=exit-code\nExecMainStartTimestamp=\n", nil
	}
	return "", nil
}

// A bundle's own .volume and .network units are written under the Service's
// name, and the containers' references to them follow (plan C1), so Quadlet
// resolves them; everything else in the template is left alone.
func TestBundleUnitReferences(t *testing.T) {
	spec := multiSpec()
	spec.Units = []*maitrev1.ServiceUnit{{Name: "cache.volume", Body: "[Volume]\n"}, {Name: "backend.network", Body: "[Network]\n"}}
	spec.Components[1].Quadlet = "[Container]\nImage={{ .Image }}\nVolume={{ .Volumes.db }}:/var/lib/postgresql/data:Z\nVolume=cache.volume:/cache:z\nMount=type=volume,source=cache.volume,destination=/more\nNetwork=backend.network\nEnvironment=NOTE=cache.volume\n"
	res, err := services.Check(context.Background(), spec)
	if errors.Is(err, quadlet.ErrNoGenerator) {
		t.Skip("no Quadlet generator installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	db := res.Units["notes-db.container"]
	for _, want := range []string{"Volume=notes-cache.volume:/cache:z", "Mount=type=volume,source=notes-cache.volume,destination=/more", "Network=notes-backend.network", "Environment=NOTE=cache.volume"} {
		if !strings.Contains(db, want) {
			t.Errorf("db unit missing %q:\n%s", want, db)
		}
	}
	if res.Units["notes-cache.volume"] == "" || res.Units["notes-backend.network"] == "" {
		t.Fatalf("bundle units not written: %v", res.Units)
	}

	// Only .volume and .network units, and only with components.
	bad := multiSpec()
	bad.Units = []*maitrev1.ServiceUnit{{Name: "evil.service", Body: "[Service]\n"}}
	if _, err := services.Check(context.Background(), bad); err == nil {
		t.Fatal("a .service bundle unit was accepted")
	}
}
