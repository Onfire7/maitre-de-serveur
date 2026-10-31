package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// Before bootc wipes a disk, what the live system holds on it is let go of,
// innermost first (decision 271).
func TestReleaseDisk(t *testing.T) {
	const tree = `{"blockdevices":[{"path":"/dev/sda","type":"disk","fstype":null,"mountpoints":[null],"children":[
	 {"path":"/dev/sda1","type":"part","fstype":"vfat","mountpoints":["/run/maitre-browse/sda1"]},
	 {"path":"/dev/sda2","type":"part","fstype":"swap","mountpoints":["[SWAP]"]},
	 {"path":"/dev/sda3","type":"part","fstype":"LVM2_member","mountpoints":[null],"children":[
	  {"path":"/dev/mapper/fedora-root","type":"lvm","fstype":"xfs","mountpoints":[null]},
	  {"path":"/dev/mapper/fedora-home","type":"lvm","fstype":"crypto_LUKS","mountpoints":[null],"children":[
	   {"path":"/dev/mapper/luks-home","type":"crypt","fstype":"ext4","mountpoints":["/mnt/home"]}]}]},
	 {"path":"/dev/sda4","type":"part","fstype":"linux_raid_member","mountpoints":[null],"children":[
	  {"path":"/dev/md127","type":"raid1","fstype":"ext4","mountpoints":[null]}]}]}]}`
	var ran []string
	r := &BootcRunner{Exec: func(_ context.Context, stdout io.Writer, name string, args ...string) error {
		ran = append(ran, name+" "+strings.Join(args, " "))
		switch name {
		case "lsblk":
			io.WriteString(stdout, tree)
		case "pvs":
			io.WriteString(stdout, "  fedora\n")
		}
		return nil
	}}
	r.releaseDisk(context.Background(), "/dev/sda")
	want := []string{
		"lsblk -J -b -o PATH,TYPE,FSTYPE,MOUNTPOINTS /dev/sda",
		"umount /run/maitre-browse/sda1",
		"swapoff /dev/sda2",
		"lvchange -an /dev/mapper/fedora-root",
		"umount /mnt/home",
		"cryptsetup close /dev/mapper/luks-home",
		"lvchange -an /dev/mapper/fedora-home",
		"pvs --noheadings -o vg_name /dev/sda3",
		"vgchange -an fedora",
		"mdadm --stop /dev/md127",
		"udevadm settle",
	}
	if got := strings.Join(ran, "\n"); got != strings.Join(want, "\n") {
		t.Errorf("ran:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

func TestParseRxBytes(t *testing.T) {
	dev := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 5830085706  315764    0    0    0     0          0         0 5830085706  315764    0    0    0     0       0          0
enp3s0: 2369056660 2807176    0 1089    0     0          0     66114 13923130782 1321041    0    0    0     0       0          0
 wlan0:       1000       7    0    0    0     0          0         0        500       3    0    0    0     0       0          0
`
	if got := parseRxBytes(dev); got != 2369056660+1000 {
		t.Errorf("rx %d", got)
	}
}
