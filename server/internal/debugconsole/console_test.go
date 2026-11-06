package debugconsole

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/services"
)

// fakeSource is a Node in the state this app was written for: a full root
// partition, an image store still on it, a Service in a restart loop and
// images nothing names.
type fakeSource struct {
	debugBuild bool
	// tooOld makes the newer calls answer Unimplemented, as a Node from
	// before them does.
	tooOld bool
	// calls counts what was asked, so a test can prove the Console did
	// not call something it said it would not.
	calls  []string
	pruned bool
	moved  bool
}

func (f *fakeSource) note(s string) { f.calls = append(f.calls, s) }

func (f *fakeSource) Info(context.Context) (*maitrev1.NodeInfo, error) {
	f.note("info")
	return &maitrev1.NodeInfo{
		NodeId: "deadbeefcafe0000", Name: "avalon-1", FleetId: "Maison", Version: "0.9.3",
		UptimeSeconds: 3600, DebugBuild: f.debugBuild,
		Peers: []*maitrev1.PeerStatus{{NodeId: "boulder"}},
	}, nil
}

func (f *fakeSource) UpdateStatus(context.Context) (*maitrev1.UpdateStatus, error) {
	f.note("update")
	return &maitrev1.UpdateStatus{
		Booted:       &maitrev1.ImageDeployment{Image: "ghcr.io/onfire7/maitre-server:main", Version: "0.9.3", Digest: "sha256:d9e853e8aaaaaaaa"},
		Rollback:     &maitrev1.ImageDeployment{Image: "ghcr.io/onfire7/maitre-server:main", Version: "0.9.2"},
		State:        maitrev1.UpdateStatus_STATE_IDLE,
		LastRollback: "greenboot rolled back 0.9.4",
	}, nil
}

func (f *fakeSource) Metrics(context.Context) (*maitrev1.NodeMetrics, error) {
	f.note("metrics")
	return &maitrev1.NodeMetrics{
		CpuPercent: 12.5, Load_1M: 0.42,
		MemoryTotalBytes: 8 << 30, MemoryUsedBytes: 3 << 30,
		PoolTotalBytes: 100 << 30, PoolUsedBytes: 20 << 30,
		// A root with 1 GiB left: below SystemFloor, which is the whole
		// point of the gauge.
		SystemDiskTotalBytes: 24 << 30, SystemDiskUsedBytes: 23 << 30,
		ImageStoreTotalBytes: 24 << 30, ImageStoreUsedBytes: 23 << 30,
		ImageStoreDir:    "/var/lib/containers/storage",
		NetRxBytesPerSec: 1 << 20,
	}, nil
}

func (f *fakeSource) Images(context.Context) (*maitrev1.ListImagesResponse, error) {
	f.note("images")
	if f.tooOld {
		return nil, connect.NewError(connect.CodeUnimplemented, "ListImages")
	}
	return &maitrev1.ListImagesResponse{
		StoreDir: "/var/lib/containers/storage", FreeBytes: 1 << 30, TotalBytes: 24 << 30,
		Images: []*maitrev1.ListImagesResponse_Image{
			{Id: "aaaaaaaaaaaabbbb", Reference: "jellyfin/jellyfin:10.9", SizeBytes: 2 << 30, Service: "jellyfin"},
			{Id: "ccccccccccccdddd", Reference: "jellyfin/jellyfin:10.8", SizeBytes: 2 << 30, Service: "jellyfin", RollbackTarget: true},
			{Id: "eeeeeeeeeeeeffff", Reference: "louislam/uptime-kuma:0", SizeBytes: 3 << 30},
		},
	}, nil
}

func (f *fakeSource) Prune(_ context.Context, keepRollback bool) (*maitrev1.PruneImagesResponse, error) {
	f.note("prune")
	f.pruned = true
	kept := uint32(1)
	if keepRollback {
		kept = 2
	}
	return &maitrev1.PruneImagesResponse{StoreDir: "/var/lib/containers/storage", FreeBeforeBytes: 1 << 30, FreeAfterBytes: 4 << 30, Removed: []string{"louislam/uptime-kuma:0"}, Kept: kept}, nil
}

func (f *fakeSource) MoveStore(_ context.Context, dir string) (*maitrev1.MoveImageStoreResponse, error) {
	f.note("move:" + dir)
	f.moved = true
	return &maitrev1.MoveImageStoreResponse{From: "/var/lib/containers/storage", To: "/mnt/pool-1/.maitre/imagestore", MovedBytes: 23 << 30, RootFreeBeforeBytes: 1 << 30, RootFreeAfterBytes: 20 << 30, Services: []string{"jellyfin"}}, nil
}

func (f *fakeSource) Services(context.Context) ([]*maitrev1.ServiceStatus, error) {
	f.note("services")
	return []*maitrev1.ServiceStatus{
		{Spec: &maitrev1.ServiceSpec{Name: "jellyfin", Image: "jellyfin/jellyfin:10.9"}, State: maitrev1.ServiceStatus_STATE_RUNNING, Healthy: true},
		{Spec: &maitrev1.ServiceSpec{Name: "qbittorrent", Image: "lscr.io/qbittorrent:5"}, State: maitrev1.ServiceStatus_STATE_FAILED, Restarts: 412, Message: "pull failed: no space left on device", NetworkSharedWith: "gluetun"},
	}, nil
}

func (f *fakeSource) Logs(_ context.Context, source string, _ int) (*maitrev1.GetNodeLogsResponse, error) {
	f.note("logs:" + source)
	return &maitrev1.GetNodeLogsResponse{
		Lines:   []string{"level=WARN pull failed", "level=INFO retrying in 5s"},
		Sources: []*maitrev1.LogSource{{Id: "agent", Label: "Maître agent"}, {Id: "kernel", Label: "Kernel"}},
	}, nil
}

func (f *fakeSource) ReadFile(_ context.Context, path string) ([]byte, error) {
	f.note("read:" + path)
	if !f.debugBuild {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "this Node was not built with the debug console")
	}
	return []byte("graphroot = \"/mnt/pool-1/.maitre/imagestore\"\n"), nil
}

func (f *fakeSource) Exec(_ context.Context, command string, args []string) (string, error) {
	f.note("exec:" + strings.Join(append([]string{command}, args...), " "))
	if !f.debugBuild {
		return "", connect.NewError(connect.CodeFailedPrecondition, "this Node was not built with the debug console")
	}
	if command == "false" {
		return "it failed, and this is why\n", errors.New("false: exit status 1")
	}
	return "Staged image: none\nBooted image: ghcr.io/onfire7/maitre-server:main\n", nil
}

// ready is a Console that has heard back from every call, as it is a
// moment after it starts.
func ready(t *testing.T, src *fakeSource) *model {
	t.Helper()
	m := newModel(src, "192.168.4.80:7443")
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	ctx := context.Background()
	info, _ := src.Info(ctx)
	upd, _ := src.UpdateStatus(ctx)
	mt, _ := src.Metrics(ctx)
	m.Update(infoMsg{info: info, update: upd, metrics: mt})
	imgs, err := src.Images(ctx)
	m.Update(imagesMsg{imgs, err})
	svcs, _ := src.Services(ctx)
	m.Update(servicesMsg{rows: svcs})
	logs, _ := src.Logs(ctx, "", 500)
	m.Update(logsMsg{res: logs})
	src.calls = nil
	return m
}

func press(m *model, s string) {
	if len(s) == 1 {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
		return
	}
	switch s {
	case "enter":
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	case "esc":
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	default:
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	}
}

// view renders with the escape codes stripped, so a test asserts on text
// rather than on styling.
func view(m *model) string {
	v := m.View()
	for {
		i := strings.Index(v, "\x1b[")
		if i < 0 {
			return v
		}
		j := strings.IndexByte(v[i:], 'm')
		if j < 0 {
			return v
		}
		v = v[:i] + v[i+j+1:]
	}
}

// flat is the view with every run of whitespace collapsed, so that a
// sentence wrapped to the terminal still matches as one.
func flat(m *model) string { return strings.Join(strings.Fields(view(m)), " ") }

func TestOverviewShowsTheGaugesTheIncidentLacked(t *testing.T) {
	m := ready(t, &fakeSource{debugBuild: true})
	v := view(m)
	for _, want := range []string{
		"Maître Debug Console", "avalon-1", "DEBUG BUILD",
		"Maison", "deadbeefcafe", "0.9.3", "1h0m0s",
		// The two gauges that did not exist on 2026-11-04, and the floor.
		"Root", "Image store", "1.0 GiB free of 24.0 GiB", "low: under 3.0 GiB",
		"/var/lib/containers/storage", "still on root",
		"greenboot rolled back 0.9.4",
		"1 Overview", "6 Run",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("overview lacks %q:\n%s", want, v)
		}
	}
}

func TestSpacePageSaysWhichImagesAPruneWouldTake(t *testing.T) {
	m := ready(t, &fakeSource{debugBuild: true})
	press(m, "2")
	v := view(m)
	for _, want := range []string{
		"jellyfin/jellyfin:10.9", "jellyfin (Rollback)", "louislam/uptime-kuma:0",
		// 3 GiB of the 7 is named by nothing: the space a prune returns.
		"3, of which 3.0 GiB no Service names",
		"p prune", "m move the store off root",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("space page lacks %q:\n%s", want, v)
		}
	}
}

func TestDestructiveActionsAreConfirmedFirst(t *testing.T) {
	src := &fakeSource{debugBuild: true}
	m := ready(t, src)
	press(m, "2")
	press(m, "p")
	if !strings.Contains(view(m), "Delete every image no Service of this Node names?") {
		t.Fatalf("prune not confirmed:\n%s", view(m))
	}
	press(m, "n")
	if src.pruned {
		t.Error("prune ran after the confirmation was declined")
	}
	press(m, "m")
	if !strings.Contains(view(m), "Services stop for the copy") {
		t.Errorf("store move not confirmed:\n%s", view(m))
	}
	press(m, "n")
	if src.moved {
		t.Error("store move ran after the confirmation was declined")
	}
	// Accepting runs it, and the command carries the answer back.
	press(m, "p")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd == nil {
		t.Fatal("no command after confirming the prune")
	}
	msg := cmd()
	if !src.pruned {
		t.Fatal("prune did not run after it was confirmed")
	}
	done, ok := msg.(doneMsg)
	if !ok || !strings.Contains(done.text, "3.0 GiB back") {
		t.Errorf("prune result %#v", msg)
	}
}

func TestServicesPageNamesTheTunnelCarryingTheTraffic(t *testing.T) {
	m := ready(t, &fakeSource{debugBuild: true})
	press(m, "3")
	v := view(m)
	for _, want := range []string{"jellyfin", "healthy", "qbittorrent", "failed", "412", "gluetun"} {
		if !strings.Contains(v, want) {
			t.Errorf("services page lacks %q:\n%s", want, v)
		}
	}
}

func TestLogsPageCyclesTheNodesOwnSources(t *testing.T) {
	src := &fakeSource{debugBuild: true}
	m := ready(t, src)
	press(m, "4")
	if v := view(m); !strings.Contains(v, "Maître agent") || !strings.Contains(v, "pull failed") {
		t.Errorf("logs page:\n%s", v)
	}
	press(m, "right")
	if v := view(m); !strings.Contains(v, "Kernel") {
		t.Errorf("source not cycled:\n%s", v)
	}
	if m.currentSource() != "kernel" {
		t.Errorf("current source %q, want kernel", m.currentSource())
	}
	// "agent" is the Node's name for the default, which the wire calls "".
	press(m, "left")
	if m.currentSource() != "" {
		t.Errorf("agent source should be the empty id, got %q", m.currentSource())
	}
}

func TestADebugBuildAnswersFilesAndRun(t *testing.T) {
	src := &fakeSource{debugBuild: true}
	m := ready(t, src)
	press(m, "5")
	m.path.SetValue("/etc/containers/storage.conf")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on the Files page asked nothing")
	}
	m.Update(cmd())
	if v := view(m); !strings.Contains(v, "graphroot") || !strings.Contains(v, "/etc/containers/storage.conf") {
		t.Errorf("file not shown:\n%s", v)
	}
	press(m, "esc")
	press(m, "6")
	m.cmd.SetValue("bootc status")
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(cmd())
	if v := view(m); !strings.Contains(v, "Booted image") {
		t.Errorf("command output not shown:\n%s", v)
	}
	// A failed command still shows its output, because that is the
	// diagnosis.
	m.cmd.Focus()
	m.cmd.SetValue("false")
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(cmd())
	if v := view(m); !strings.Contains(v, "it failed, and this is why") || !strings.Contains(v, "exit status 1") {
		t.Errorf("failed command hid its output:\n%s", v)
	}
}

func TestAShippedImageSaysSoBeforeAnythingIsTyped(t *testing.T) {
	src := &fakeSource{debugBuild: false}
	m := ready(t, src)
	if v := view(m); !strings.Contains(v, "shipped image — Files and Run are not in it") {
		t.Errorf("header does not say the image is a shipped one:\n%s", v)
	}
	press(m, "5")
	if v := flat(m); !strings.Contains(v, "it has no file-reading procedure to answer with") {
		t.Errorf("Files page does not explain a shipped image:\n%s", view(m))
	}
	press(m, "esc")
	press(m, "6")
	if v := flat(m); !strings.Contains(v, "absent from the binary rather than guarded inside it") {
		t.Errorf("Run page does not explain a shipped image:\n%s", view(m))
	}
	// And nothing was asked of the Node: the Console knows from
	// NodeInfo.debug_build, so it does not make a Node refuse to find out.
	for _, c := range src.calls {
		if strings.HasPrefix(c, "read:") || strings.HasPrefix(c, "exec:") {
			t.Errorf("the Console called %q on a shipped image", c)
		}
	}
}

func TestAnOlderNodeIsSaidToBeOlderRatherThanBroken(t *testing.T) {
	m := ready(t, &fakeSource{debugBuild: false, tooOld: true})
	press(m, "2")
	if v := flat(m); !strings.Contains(v, "does not answer the image list") {
		t.Errorf("space page on an older Node:\n%s", v)
	}
}

func TestATypedPathKeepsItsDigits(t *testing.T) {
	m := ready(t, &fakeSource{debugBuild: true})
	press(m, "5")
	for _, r := range "/proc/2" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.page != pageFiles {
		t.Fatalf("a digit in a path changed the page to %d", m.page)
	}
	if got := m.path.Value(); got != "/proc/2" {
		t.Errorf("path %q, want /proc/2", got)
	}
	// esc hands the keyboard back, and then the digits are pages again.
	press(m, "esc")
	press(m, "2")
	if m.page != pageSpace {
		t.Errorf("page %d after esc then 2, want Space", m.page)
	}
}

// The gauge turns red at the number the agent itself insists on, so the
// Console and the Node cannot disagree about what "full" means.
func TestPullFloorMatchesTheAgent(t *testing.T) {
	if PullFloor != services.PullFloor {
		t.Errorf("PullFloor = %d, services.PullFloor = %d", PullFloor, services.PullFloor)
	}
}

// TestEveryPageFitsTheTerminal is the invariant behind the column
// arithmetic: a table wider than the window is cut off, and a panel cut
// off loses the border that made it a panel. Checked before the last
// resort in View, so this fails rather than being hidden by it.
func TestEveryPageFitsTheTerminal(t *testing.T) {
	// A hundred columns is what this app lays out for; it is a developer's
	// terminal, not the Node's console, and below that the last resort in
	// View truncates rather than wrapping a table into nonsense.
	src := &fakeSource{debugBuild: true}
	for _, width := range []int{100, 120, 150, 240} {
		m := ready(t, src)
		m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
		for p := pageOverview; p <= pageRun; p++ {
			m.page = p
			if got := lipgloss.Width(m.header() + "\n" + m.body()); got > width {
				t.Errorf("page %d at width %d renders %d columns wide:\n%s", p, width, got, m.body())
			}
			// The bar names every page and this one's keys, so it has a
			// floor of its own; a roomy terminal must still be above it.
			if got := lipgloss.Width(bottomBar(width, pageNames, int(p), m.hint())); got > width && width >= 150 {
				t.Errorf("the bottom bar for page %d is %d columns wide at width %d", p, got, width)
			}
		}
	}
}
