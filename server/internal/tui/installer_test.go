package tui

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

type fakeInstaller struct {
	code    string
	phase   maitrev1.Provisioning_Phase
	folders map[string][]*maitrev1.FolderEntry
}

func (f *fakeInstaller) Status() InstallerStatus {
	return InstallerStatus{Version: "test", Machine: "OptiPlex 7080", Identity: "0123456789abcdef0123", Addresses: []string{"192.168.4.81"}, Image: "ghcr.io/onfire7/maitre-server:stable", Code: f.code, Provisioning: &maitrev1.Provisioning{Phase: f.phase, Percent: 42, Message: "writing the image"}}
}

var testDisks = []*maitrev1.Disk{
	{Path: "/dev/sda", Model: "SanDisk Ultra", SizeBytes: 32 << 30, Removable: true, IsInstallerMedia: true, Partitions: []*maitrev1.Partition{{Device: "/dev/sda1", SizeBytes: 2 << 30, Fstype: "vfat", Label: "MAITRELIVE"}}},
	{Path: "/dev/nvme0n1", Model: "WD Blue SN580", SizeBytes: 1 << 40, Partitions: []*maitrev1.Partition{
		{Device: "/dev/nvme0n1p1", SizeBytes: 100 << 20, Fstype: "vfat", Label: "EFI", Browsable: true},
		{Device: "/dev/nvme0n1p2", SizeBytes: 900 << 30, Fstype: "crypto_LUKS"},
		{Device: "/dev/nvme0n1p3", SizeBytes: 100 << 30, Fstype: "ntfs", Label: "Windows", Browsable: true},
	}},
	{Path: "/dev/sdb", Model: "Media HDD", SizeBytes: 4 << 40, Partitions: []*maitrev1.Partition{{Device: "/dev/sdb1", SizeBytes: 4 << 40, Fstype: "ext4", Label: "Media", Browsable: true}}},
	{Path: "/dev/sdc", Model: "Blank SSD", SizeBytes: 256 << 30},
}

func (f *fakeInstaller) Disks(context.Context) ([]*maitrev1.Disk, error) { return testDisks, nil }
func (f *fakeInstaller) Folder(_ context.Context, device, dir string) (*maitrev1.ListFolderResponse, error) {
	return &maitrev1.ListFolderResponse{Entries: f.folders[device+":"+dir], UsedBytes: 1 << 40, TotalBytes: 4 << 40}, nil
}
func (f *fakeInstaller) Logs(context.Context, int) ([]string, error) {
	return []string{"level=INFO msg=listening"}, nil
}
func (f *fakeInstaller) Reboot() error { return ErrQuit }

// drive feeds a key and runs what it asks for, as the program would.
func drive(t *testing.T, m *installerModel, key string) {
	t.Helper()
	var msg tea.KeyMsg
	switch key {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "backspace":
		msg = tea.KeyMsg{Type: tea.KeyBackspace}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	_, cmd := m.Update(msg)
	if cmd != nil {
		if out := cmd(); out != nil {
			m.Update(out)
		}
	}
}

func show(t *testing.T, name, v string) {
	if os.Getenv("SHOW_SCREENS") != "" {
		t.Logf("%s:\n%s", name, v)
	}
}

func TestInstallerPages(t *testing.T) {
	src := &fakeInstaller{phase: maitrev1.Provisioning_PHASE_IDLE, folders: map[string][]*maitrev1.FolderEntry{
		"/dev/sdb1:":      {{Name: "Films", Dir: true}, {Name: "Photos", Dir: true}, {Name: "notes.txt", SizeBytes: 1200, ModifiedUnixMs: 1_760_000_000_000}},
		"/dev/sdb1:Films": {{Name: "Arrival (2016).mkv", SizeBytes: 8 << 30}},
	}}
	m := newInstallerModel(src)
	m.Update(tea.WindowSizeMsg{Width: 110, Height: 32})

	v := m.View()
	show(t, "waiting", v)
	for i, l := range strings.Split(v, "\n") {
		if w := lipgloss.Width(l); w > 110 {
			t.Errorf("line %d is %d wide", i, w)
		}
	}
	for _, want := range []string{"Installer", "OptiPlex 7080", "192.168.4.81", "0123456789abcdef", "Waiting for Maître Desktop", "1 Install", "2 Disks", "3 Log", "██╔╝╚██╗"} {
		if !strings.Contains(v, want) {
			t.Errorf("install page lacks %q:\n%s", want, v)
		}
	}
	src.code = "482913"
	m.Update(tickMsg{})
	if v := m.View(); !strings.Contains(v, "Pairing code") || !strings.Contains(v, "(482913)") {
		t.Errorf("no pairing code:\n%s", v)
	} else {
		show(t, "pairing", v)
	}
	src.phase = maitrev1.Provisioning_PHASE_INSTALLING
	m.Update(tickMsg{})
	if v := m.View(); !strings.Contains(v, "42%") || !strings.Contains(v, "Do not power off") {
		t.Errorf("no progress:\n%s", v)
	}
	src.phase = maitrev1.Provisioning_PHASE_IDLE

	// Disks: every disk with what it holds.
	drive(t, m, "2")
	v = m.View()
	show(t, "disks", v)
	for i, l := range strings.Split(v, "\n") {
		if w := lipgloss.Width(l); w > 110 {
			t.Errorf("disks line %d is %d wide", i, w)
		}
	}
	for _, want := range []string{"SanDisk Ultra", "Installer", "WD Blue SN580", "encrypted, Windows (ntfs)", "Media (ext4)", "blank", "enter look inside"} {
		if !strings.Contains(v, want) {
			t.Errorf("disks page lacks %q:\n%s", want, v)
		}
	}
	// The NVMe disk has three partitions: its list, with why one is closed.
	drive(t, m, "down")
	drive(t, m, "enter")
	v = m.View()
	show(t, "partitions", v)
	if !strings.Contains(v, "encrypted") || !strings.Contains(v, "/dev/nvme0n1p3") {
		t.Errorf("partitions:\n%s", v)
	}
	drive(t, m, "down")
	drive(t, m, "enter")
	if !strings.Contains(m.View(), "encrypted") || m.level != levelPartitions {
		t.Errorf("opened an encrypted partition")
	}
	drive(t, m, "backspace")
	if m.level != levelDisks || m.table.Cursor() != 1 {
		t.Errorf("back to disks: level %d cursor %d", m.level, m.table.Cursor())
	}

	// The media disk has one partition: straight into its folders.
	drive(t, m, "down")
	// The list is read again every tick; the selection stays.
	m.Update(disksMsg{disks: testDisks})
	drive(t, m, "enter")
	v = m.View()
	show(t, "folder", v)
	if m.level != levelFolder || !strings.Contains(v, "Films/") || !strings.Contains(v, "1.0 TiB of 4.0 TiB used") {
		t.Errorf("folder:\n%s", v)
	}
	drive(t, m, "enter")
	if v := m.View(); !strings.Contains(v, "Arrival (2016).mkv") || !strings.Contains(v, "/Films") {
		t.Errorf("subfolder:\n%s", v)
	}
	drive(t, m, "backspace")
	drive(t, m, "backspace")
	if m.level != levelDisks || m.table.Cursor() != 2 {
		t.Errorf("back out of a one-partition disk: level %d cursor %d", m.level, m.table.Cursor())
	}
	drive(t, m, "down")
	drive(t, m, "enter")
	if m.level != levelDisks || !strings.Contains(m.View(), "blank") {
		t.Errorf("opened a blank disk")
	}

	drive(t, m, "3")
	if v := m.View(); !strings.Contains(v, "maitre-installer") || !strings.Contains(v, "msg=listening") {
		t.Errorf("log page:\n%s", v)
	}
	// Restart is offered on the Install page only.
	drive(t, m, "r")
	if m.confirm {
		t.Error("restart offered off the Install page")
	}
	drive(t, m, "1")
	drive(t, m, "r")
	if !strings.Contains(m.View(), "Restart this machine?") {
		t.Error("no restart confirmation")
	}
}

func TestHolds(t *testing.T) {
	for _, c := range []struct {
		d    *maitrev1.Disk
		want string
	}{
		{testDisks[1], "encrypted, Windows (ntfs)"},
		{testDisks[3], "blank"},
		{&maitrev1.Disk{Partitions: []*maitrev1.Partition{{SizeBytes: 100 << 20, Fstype: "vfat"}}}, "1 small partition(s)"},
		{&maitrev1.Disk{Partitions: []*maitrev1.Partition{{SizeBytes: 2 << 30, Fstype: "xfs"}, {SizeBytes: 2 << 30, Label: "data"}}}, "xfs, data"},
	} {
		if got := Holds(c.d); got != c.want {
			t.Errorf("Holds = %q, want %q", got, c.want)
		}
	}
}
