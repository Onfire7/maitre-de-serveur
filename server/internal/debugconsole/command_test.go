package debugconsole

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func run(t *testing.T, src Source, opt Options, args ...string) (string, error) {
	t.Helper()
	var b bytes.Buffer
	err := Command(context.Background(), src, &b, args, opt)
	return b.String(), err
}

func TestStatusPrintsTheFactsAndTheGauges(t *testing.T) {
	out, err := run(t, &fakeSource{debugBuild: true}, Options{}, "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"name               avalon-1",
		"fleet              Maison",
		"debug_build        yes",
		"root               96% used, 1.0 GiB free of 24.0 GiB",
		"image_store        96% used",
		"image_store_dir    /var/lib/containers/storage",
		"booted             ghcr.io/onfire7/maitre-server:main 0.9.3 d9e853e8aaaa",
		"last_rollback      greenboot rolled back 0.9.4",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
}

func TestAShippedImageSaysSoInStatusToo(t *testing.T) {
	out, err := run(t, &fakeSource{}, Options{}, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `debug_build        no — read and run answer "not a debug build"`) {
		t.Errorf("status does not say the image is a shipped one:\n%s", out)
	}
}

func TestEveryCommandHasAJSONFormAScriptCanRead(t *testing.T) {
	src := &fakeSource{debugBuild: true}
	opt := Options{JSON: true}

	out, err := run(t, src, opt, "status")
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		Node    map[string]any `json:"node"`
		Update  map[string]any `json:"update"`
		Metrics map[string]any `json:"metrics"`
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("status --json is not JSON: %v\n%s", err, out)
	}
	if status.Node["name"] != "avalon-1" {
		t.Errorf("status --json node.name = %v", status.Node["name"])
	}
	// The field the Console exists for, readable without parsing a table.
	if status.Metrics["systemDiskUsedBytes"] == nil {
		t.Errorf("status --json has no systemDiskUsedBytes:\n%s", out)
	}
	if status.Update["lastRollback"] != "greenboot rolled back 0.9.4" {
		t.Errorf("status --json update.lastRollback = %v", status.Update["lastRollback"])
	}

	for _, cmd := range []string{"space", "services", "logs", "prune", "move-store"} {
		out, err := run(t, src, opt, cmd)
		if err != nil {
			t.Fatalf("%s --json: %v", cmd, err)
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(out), &obj); err != nil {
			t.Errorf("%s --json is not JSON: %v\n%s", cmd, err, out)
		}
	}
}

func TestSpaceCommandWarnsBelowTheFloorTheNodeInsistsOn(t *testing.T) {
	out, err := run(t, &fakeSource{debugBuild: true}, Options{}, "space")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"store      /var/lib/containers/storage",
		"warning    below the 4.0 GiB this Node needs before it will pull an image",
		"images     3, of which 3.0 GiB no Service names",
		"jellyfin (Rollback)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("space lacks %q:\n%s", want, out)
		}
	}
	// `images` is the same page under the name the service CLI uses.
	same, err := run(t, &fakeSource{debugBuild: true}, Options{}, "images")
	if err != nil || same != out {
		t.Errorf("images and space differ:\n%s\n%s", out, same)
	}
}

func TestServicesCommandShowsTheFailureAndItsReason(t *testing.T) {
	out, err := run(t, &fakeSource{debugBuild: true}, Options{}, "services")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"jellyfin", "healthy", "qbittorrent", "failed", "restarts=412", "via gluetun", "no space left on device"} {
		if !strings.Contains(out, want) {
			t.Errorf("services lacks %q:\n%s", want, out)
		}
	}
}

func TestLogsCommandNamesTheOtherSources(t *testing.T) {
	src := &fakeSource{debugBuild: true}
	out, err := run(t, src, Options{Lines: 50}, "logs")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pull failed") || !strings.Contains(out, "sources: agent kernel") {
		t.Errorf("logs:\n%s", out)
	}
	// "agent" is the Node's name for the default, and the wire's name for
	// it is the empty string.
	if _, err := run(t, src, Options{}, "logs", "agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, src, Options{}, "logs", "kernel"); err != nil {
		t.Fatal(err)
	}
	want := []string{"logs:", "logs:", "logs:kernel"}
	for i, c := range src.calls {
		if c != want[i] {
			t.Errorf("call %d was %q, want %q", i, c, want[i])
		}
	}
}

func TestReadWritesTheFileItself(t *testing.T) {
	out, err := run(t, &fakeSource{debugBuild: true}, Options{}, "read", "/etc/containers/storage.conf")
	if err != nil {
		t.Fatal(err)
	}
	// Bytes through, nothing added: this is what a script diffs.
	if out != "graphroot = \"/mnt/pool-1/.maitre/imagestore\"\n" {
		t.Errorf("read wrote %q", out)
	}
}

func TestRunPassesArgumentsThroughAndPrintsAFailuresOutput(t *testing.T) {
	src := &fakeSource{debugBuild: true}
	out, err := run(t, src, Options{}, "run", "systemctl", "status", "jellyfin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Booted image") {
		t.Errorf("run:\n%s", out)
	}
	if src.calls[0] != "exec:systemctl status jellyfin" {
		t.Errorf("arguments not passed through: %q", src.calls[0])
	}
	// The output is the diagnosis even when the command failed, so it is
	// printed and the failure is still the exit status.
	out, err = run(t, src, Options{}, "run", "false")
	if err == nil {
		t.Error("a failed command returned no error")
	}
	if !strings.Contains(out, "it failed, and this is why") {
		t.Errorf("a failed command printed nothing:\n%s", out)
	}
}

func TestAShippedImageRefusesTheDebugTier(t *testing.T) {
	src := &fakeSource{}
	if _, err := run(t, src, Options{}, "read", "/proc/mounts"); err == nil {
		t.Error("read succeeded against a shipped image")
	} else if !strings.Contains(err.Error(), "not built with the debug console") {
		t.Errorf("read error %v", err)
	}
	if _, err := run(t, src, Options{}, "run", "ls"); err == nil {
		t.Error("run succeeded against a shipped image")
	}
	// The basic tier still works on the same Node, which is the point of
	// the split.
	if _, err := run(t, src, Options{}, "space"); err != nil {
		t.Errorf("space failed against a shipped image: %v", err)
	}
}

func TestPruneKeepsRollbackUnlessTold(t *testing.T) {
	out, err := run(t, &fakeSource{debugBuild: true}, Options{}, "prune")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1.0 GiB before, 4.0 GiB after: 3.0 GiB back", "kept       2 image(s)", "louislam/uptime-kuma:0"} {
		if !strings.Contains(out, want) {
			t.Errorf("prune lacks %q:\n%s", want, out)
		}
	}
	// Decision 320: dropping the Rollback target is asked for explicitly.
	out, err = run(t, &fakeSource{debugBuild: true}, Options{DropRollback: true}, "prune")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "kept       1 image(s)") {
		t.Errorf("-drop-rollback kept the Rollback target:\n%s", out)
	}
}

func TestMoveStorePassesTheChosenDirectory(t *testing.T) {
	src := &fakeSource{debugBuild: true}
	out, err := run(t, src, Options{StoreDir: "/mnt/pool-1/.maitre/imagestore"}, "move-store")
	if err != nil {
		t.Fatal(err)
	}
	if src.calls[0] != "move:/mnt/pool-1/.maitre/imagestore" {
		t.Errorf("-store-dir not passed: %q", src.calls[0])
	}
	for _, want := range []string{"/var/lib/containers/storage -> /mnt/pool-1/.maitre/imagestore", "moved      23.0 GiB", "19.0 GiB back", "restarted  1: jellyfin"} {
		if !strings.Contains(out, want) {
			t.Errorf("move-store lacks %q:\n%s", want, out)
		}
	}
	// Empty lets the Node choose, which is what the screen asks for.
	src2 := &fakeSource{debugBuild: true}
	if _, err := run(t, src2, Options{}, "move-store"); err != nil {
		t.Fatal(err)
	}
	if src2.calls[0] != "move:" {
		t.Errorf("an empty -store-dir became %q", src2.calls[0])
	}
}

func TestAnOlderNodeIsToldApartFromABrokenOne(t *testing.T) {
	src := &fakeSource{tooOld: true}
	_, err := run(t, src, Options{}, "space")
	if err == nil || !strings.Contains(err.Error(), "does not answer the image list") {
		t.Errorf("space against an older Node: %v", err)
	}
}

func TestNoCommandAndAnUnknownOneBothPrintTheUsage(t *testing.T) {
	src := &fakeSource{}
	_, err := run(t, src, Options{})
	if err == nil || !strings.Contains(err.Error(), "maitre debug") {
		t.Errorf("no command: %v", err)
	}
	_, err = run(t, src, Options{}, "shell")
	if err == nil || !strings.Contains(err.Error(), `unknown debug command "shell"`) {
		t.Errorf("unknown command: %v", err)
	}
	// Every page of the screen is named in the usage, because a page that
	// cannot be scripted is the thing this was built to avoid.
	for _, want := range []string{"status", "space", "services", "logs", "read", "run", "prune", "move-store"} {
		if !strings.Contains(Usage, want) {
			t.Errorf("the usage does not mention %q", want)
		}
	}
}

// Go's flag package stops at the first non-flag argument, so a flag typed
// after the command is never seen. Said rather than silently ignored.
func TestAFlagAfterTheCommandIsRefusedRatherThanIgnored(t *testing.T) {
	src := &fakeSource{debugBuild: true}
	_, err := run(t, src, Options{}, "space", "--json")
	if err == nil || !strings.Contains(err.Error(), "flags go before the command") {
		t.Errorf("space --json: %v", err)
	}
	// `run` is the exception: the dashes belong to the Node's command.
	if _, err := run(t, src, Options{}, "run", "journalctl", "-n", "50"); err != nil {
		t.Fatal(err)
	}
	if src.calls[0] != "exec:journalctl -n 50" {
		t.Errorf("run swallowed a dash: %q", src.calls[0])
	}
}
