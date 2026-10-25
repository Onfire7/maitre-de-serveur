package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

const sampleStatus = `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","metadata":{"name":"host"},
"spec":{"bootOrder":"default","image":{"image":"ghcr.io/onfire7/maitre-server:main","transport":"registry"}},
"status":{"booted":{"image":{"image":{"image":"ghcr.io/onfire7/maitre-server:main","transport":"registry"},"version":"42.20261001.0","timestamp":"2026-10-01T10:00:00Z","imageDigest":"sha256:aaa"},"cachedUpdate":null,"incompatible":false,"pinned":false,"ostree":{"checksum":"c","deploySerial":0,"stateroot":"default"}},
"rollback":{"image":{"image":{"image":"ghcr.io/onfire7/maitre-server:main","transport":"registry"},"version":"42.20260901.0","timestamp":"2026-09-01T10:00:00Z","imageDigest":"sha256:bbb"}},
"staged":null,"rollbackQueued":false,"type":"bootcHost"}}`

func TestParseStatus(t *testing.T) {
	st := &maitrev1.UpdateStatus{}
	if err := ParseStatus(strings.NewReader(sampleStatus), st); err != nil {
		t.Fatal(err)
	}
	if st.GetBooted().GetImage() != "ghcr.io/onfire7/maitre-server:main" || st.GetBooted().GetDigest() != "sha256:aaa" || st.GetBooted().GetVersion() != "42.20261001.0" {
		t.Errorf("booted = %v", st.GetBooted())
	}
	if st.GetStaged() != nil {
		t.Errorf("staged should be nil")
	}
	if st.GetRollback().GetDigest() != "sha256:bbb" {
		t.Errorf("rollback = %v", st.GetRollback())
	}
}

func TestRegistryHost(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/onfire7/maitre-server:main":          "ghcr.io",
		"docker://192.168.1.5:8080/maitre-server:dev": "192.168.1.5:8080",
		"maitre-depot.local:8080/maitre-server:dev":   "maitre-depot.local:8080",
		"localhost/maitre-server:dev":                 "localhost",
		"library/fedora:42":                           "",
		"fedora":                                      "",
	} {
		if got := RegistryHost(in); got != want {
			t.Errorf("RegistryHost(%q) = %q, want %q", in, got, want)
		}
	}
}

type fakeRunner struct {
	calls    []string
	switched string
	fail     bool
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name == "bootc" && args[0] == "status" {
		return sampleStatus, nil
	}
	return "", nil
}

func (f *fakeRunner) Switch(_ context.Context, image string, progress func(Progress)) error {
	f.switched = image
	progress(Progress{Type: "ProgressBytes", Description: "Pulling", Bytes: 50, BytesTotal: 100})
	if f.fail {
		return errors.New("boom")
	}
	return nil
}

func wait(t *testing.T, m *Manager, want maitrev1.UpdateStatus_State) *maitrev1.UpdateStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, err := m.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if st.GetState() == want {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("state never became %v", want)
	return nil
}

func TestStartStagesAndReboots(t *testing.T) {
	f := &fakeRunner{}
	m := New(f, nil)
	m.RegistriesDir = t.TempDir()
	if err := m.Start("192.168.1.5:8080/maitre-server:dev", true, true); err != nil {
		t.Fatal(err)
	}
	st := wait(t, m, maitrev1.UpdateStatus_STATE_REBOOTING)
	if f.switched != "192.168.1.5:8080/maitre-server:dev" || st.GetTarget() != f.switched {
		t.Errorf("switched %q target %q", f.switched, st.GetTarget())
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "systemctl reboot") {
		t.Errorf("no reboot: %v", f.calls)
	}
	b, err := os.ReadFile(filepath.Join(m.RegistriesDir, "maitre-192.168.1.5-8080.conf"))
	if err != nil || !strings.Contains(string(b), `location = "192.168.1.5:8080"`) || !strings.Contains(string(b), "insecure = true") {
		t.Errorf("registries drop-in: %v %s", err, b)
	}
	if err := m.Start("x/y:z", false, false); !errors.Is(err, ErrBusy) {
		t.Errorf("second start: %v", err)
	}
}

func TestStartWithoutReboot(t *testing.T) {
	f := &fakeRunner{}
	m := New(f, nil)
	if err := m.Start("ghcr.io/onfire7/maitre-server:main", false, false); err != nil {
		t.Fatal(err)
	}
	st := wait(t, m, maitrev1.UpdateStatus_STATE_IDLE)
	if !strings.Contains(st.GetMessage(), "staged") || st.GetProgress() != 1 {
		t.Errorf("status = %v", st)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "reboot") {
			t.Errorf("rebooted without being asked: %v", f.calls)
		}
	}
}

func TestStartFailure(t *testing.T) {
	f := &fakeRunner{fail: true}
	m := New(f, nil)
	if err := m.Start("ghcr.io/onfire7/maitre-server:main", true, false); err != nil {
		t.Fatal(err)
	}
	st := wait(t, m, maitrev1.UpdateStatus_STATE_FAILED)
	if !strings.Contains(st.GetMessage(), "boom") {
		t.Errorf("message = %q", st.GetMessage())
	}
	// A failed update does not block the next one.
	if err := m.Start("ghcr.io/onfire7/maitre-server:main", false, false); err != nil {
		t.Fatal(err)
	}
}

func TestRollback(t *testing.T) {
	f := &fakeRunner{}
	m := New(f, nil)
	if err := m.Rollback(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.calls, "\n")
	if !strings.Contains(joined, "bootc rollback") || !strings.Contains(joined, "systemctl reboot") {
		t.Errorf("calls = %v", f.calls)
	}
}

func TestReadProgress(t *testing.T) {
	var got []Progress
	ReadProgress(strings.NewReader(`{"type":"ProgressBytes","task":"pulling","description":"Pulling layers","bytes":10,"bytes_total":40}
not json
{"type":"ProgressSteps","task":"deploying","steps":1,"steps_total":3}
`), func(p Progress) { got = append(got, p) })
	if len(got) != 2 || got[0].BytesTotal != 40 || got[1].StepsTotal != 3 {
		t.Errorf("got %+v", got)
	}
}
