package installer

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestTailWriterExplainsAFailure(t *testing.T) {
	tw := &tailWriter{}
	fmt.Fprint(tw, "Checking that no-one is using this disk right now ... OK\n\nsfdisk: All space for primary partitions is in use.\r\nsfdisk: Failed to add #4 partition: Numerical result out of range\n")
	err := tw.explain(errors.New("exit status 1"))
	want := "exit status 1: Checking that no-one is using this disk right now ... OK / sfdisk: All space for primary partitions is in use. / sfdisk: Failed to add #4 partition: Numerical result out of range"
	if err.Error() != want {
		t.Errorf("got %q", err)
	}
	if (&tailWriter{}).explain(nil) != nil {
		t.Error("success must stay nil")
	}
	if err := (&tailWriter{}).explain(errors.New("exit status 2")); err.Error() != "exit status 2" {
		t.Errorf("no output: %q", err)
	}
}

// bootc follows its error with a pretty-printed dump of the command it ran;
// the failure names the error, and keeps every line for the Installer.
func TestTailWriterPicksTheErrorLineOverADump(t *testing.T) {
	tw := &tailWriter{}
	fmt.Fprint(tw, "Running bootupctl backend install\nERROR Installing to disk: Task bootupctl backend install failed: exit status 1\nCommand {\n    program: \"bootupctl\",\n    args: [\n        \"backend\",\n    ],\n    create_pidfd: false,\n}\n")
	err := tw.explain(errors.New("exit status 1"))
	want := "exit status 1: Running bootupctl backend install / ERROR Installing to disk: Task bootupctl backend install failed: exit status 1"
	if err.Error() != want {
		t.Errorf("got %q", err)
	}
	if out := Output(err); len(out) != 9 || out[8] != "}" {
		t.Errorf("output %q", out)
	}
	if Output(errors.New("plain")) != nil {
		t.Error("a plain error has no output")
	}
}

// A small disk gets a smaller root so the config (and data) partition still
// fits after it; a disk with no room for even MinRoot is refused (decision 269).
func TestRootSizeFor(t *testing.T) {
	for _, c := range []struct {
		disk int64
		data bool
		want string
	}{
		{1 << 40, true, "32G"},
		{40 << 30, false, "32G"},
		{31042043904, false, "28068M"}, // the 28.9 GiB stick, less 1.5 GiB
		{31042043904, true, "26020M"},
		{16 << 30, false, "14848M"},
		{8 << 30, false, ""},
		{10 << 30, true, ""},
	} {
		got, err := rootSizeFor(c.disk, c.data)
		if c.want == "" {
			if err == nil || !strings.Contains(err.Error(), "too small") {
				t.Errorf("%d data=%v: want too small, got %q %v", c.disk, c.data, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%d data=%v: got %q %v, want %q", c.disk, c.data, got, err, c.want)
		}
	}
}
