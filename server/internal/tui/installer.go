package tui

// The Installer's screen (decision 261): the Node's look on installer
// media. Pages: Install (the machine, the pairing code, progress), Disks
// (every disk, its partitions and their folders, decision 262), Log (the
// Installer's journal). Keys: 1 2 3 pages, r restart.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/ascii"
)

// InstallerStatus is what the Install page shows.
type InstallerStatus struct {
	Version   string
	Machine   string
	Identity  string // key fingerprint
	Addresses []string
	Image     string // what it installs
	Source    string // where from, in words
	// Code is the pairing code of the device that asked last; empty until
	// one does.
	Code         string
	Provisioning *maitrev1.Provisioning
}

// InstallerSource feeds the Installer's screen.
type InstallerSource interface {
	Status() InstallerStatus
	Disks(ctx context.Context) ([]*maitrev1.Disk, error)
	Folder(ctx context.Context, device, dir string) (*maitrev1.ListFolderResponse, error)
	Logs(ctx context.Context, lines int) ([]string, error)
	Reboot() error
}

// RunInstaller drives the Installer's screen on stdin/stdout until ctx ends.
func RunInstaller(ctx context.Context, src InstallerSource) error {
	setupConsole()
	return run(ctx, newInstallerModel(src))
}

const (
	pageInstall page = iota
	pageDisks
	pageLog
)

// Levels of the Disks page.
const (
	levelDisks = iota
	levelPartitions
	levelFolder
)

type disksMsg struct {
	disks []*maitrev1.Disk
	err   error
}

type folderMsg struct {
	device, dir string
	resp        *maitrev1.ListFolderResponse
	err         error
}

type installerLogMsg struct {
	lines []string
	err   error
}

type installerModel struct {
	src           InstallerSource
	page          page
	width, height int
	status        InstallerStatus
	confirm       bool
	message       string

	disks []*maitrev1.Disk
	dErr  error
	level int
	disk  *maitrev1.Disk
	part  *maitrev1.Partition
	dir   string // within part, "" for its top
	// cursors remembers the selected row of each level above, to return to.
	cursors []int
	folder  *maitrev1.ListFolderResponse
	fErr    error
	table   table.Model

	logs   []string
	lErr   error
	vp     viewport.Model
	follow bool
}

func newInstallerModel(src InstallerSource) *installerModel {
	t := table.New(table.WithFocused(true), table.WithHeight(10))
	s := table.DefaultStyles()
	s.Header = s.Header.Bold(true).BorderBottom(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("6"))
	s.Selected = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("6"))
	t.SetStyles(s)
	m := &installerModel{src: src, status: src.Status(), table: t, follow: true, width: 80, height: 24}
	m.vp = viewport.New(80, 10)
	return m
}

func (m *installerModel) Init() tea.Cmd {
	return tea.Batch(tick(), m.fetchDisks())
}

func (m *installerModel) fetchDisks() tea.Cmd {
	src := m.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		d, err := src.Disks(ctx)
		return disksMsg{d, err}
	}
}

func (m *installerModel) fetchFolder() tea.Cmd {
	src, device, dir := m.src, m.part.GetDevice(), m.dir
	return func() tea.Msg {
		// Opening a partition mounts it, which can take a while.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		r, err := src.Folder(ctx, device, dir)
		return folderMsg{device, dir, r, err}
	}
}

func (m *installerModel) fetchLog() tea.Cmd {
	src := m.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		lines, err := src.Logs(ctx, 300)
		return installerLogMsg{lines, err}
	}
}

func (m *installerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layoutTable()
		m.vp.Width = max(20, m.width)
		m.vp.Height = max(3, m.height-4)
		setLog(&m.vp, m.logs, m.follow)
	case tickMsg:
		m.status = m.src.Status()
		cmds := []tea.Cmd{tick()}
		// As on the Node's screen: the console's size changes after boot
		// and a repaint rubs out anything printed over the screen.
		if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 && h > 0 {
			cmds = append(cmds, func() tea.Msg { return tea.WindowSizeMsg{Width: w, Height: h} })
		}
		// A disk plugged in shows up; folders are read when opened.
		if m.page == pageDisks && m.level < levelFolder {
			cmds = append(cmds, m.fetchDisks())
		}
		if m.page == pageLog {
			cmds = append(cmds, m.fetchLog())
		}
		return m, tea.Batch(cmds...)
	case disksMsg:
		m.disks, m.dErr = msg.disks, msg.err
		m.refreshChosen()
		m.layoutTable()
	case folderMsg:
		if m.part != nil && msg.device == m.part.GetDevice() && msg.dir == m.dir {
			m.folder, m.fErr = msg.resp, msg.err
			m.layoutTable()
		}
	case installerLogMsg:
		m.logs, m.lErr = msg.lines, msg.err
		setLog(&m.vp, m.logs, m.follow)
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *installerModel) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirm {
		m.confirm = false
		m.message = ""
		if k := msg.String(); k == "y" || k == "Y" {
			m.message = "restarting..."
			if err := m.src.Reboot(); errors.Is(err, ErrQuit) {
				return m, tea.Quit
			} else if err != nil {
				m.message = "restart failed: " + err.Error()
			}
		}
		return m, nil
	}
	m.message = ""
	switch msg.String() {
	case "1":
		m.page = pageInstall
		return m, nil
	case "2":
		m.page = pageDisks
		return m, m.fetchDisks()
	case "3":
		m.page = pageLog
		return m, m.fetchLog()
	case "r":
		if m.page == pageInstall {
			m.confirm = true
		}
		return m, nil
	}
	switch m.page {
	case pageDisks:
		switch msg.String() {
		case "enter", "right", "l":
			return m, m.open()
		case "backspace", "esc", "left", "h":
			return m, m.up()
		}
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		return m, cmd
	case pageLog:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		m.follow = m.vp.AtBottom()
		return m, cmd
	}
	return m, nil
}

// open goes into the selected disk, partition or folder.
func (m *installerModel) open() tea.Cmd {
	i := m.table.Cursor()
	switch m.level {
	case levelDisks:
		if i < 0 || i >= len(m.disks) {
			return nil
		}
		d := m.disks[i]
		if len(d.GetPartitions()) == 0 {
			m.message = "This disk is blank: nothing to look at."
			return nil
		}
		m.cursors = append(m.cursors, i)
		m.disk, m.level = d, levelPartitions
		// One partition that opens: straight into it.
		if ps := d.GetPartitions(); len(ps) == 1 && ps[0].GetBrowsable() {
			m.cursors = append(m.cursors, 0)
			return m.enterPartition(ps[0])
		}
		m.layoutTable()
		m.table.SetCursor(0)
	case levelPartitions:
		ps := m.disk.GetPartitions()
		if i < 0 || i >= len(ps) {
			return nil
		}
		if !ps[i].GetBrowsable() {
			m.message = closedReason(m.disk, ps[i])
			return nil
		}
		m.cursors = append(m.cursors, i)
		return m.enterPartition(ps[i])
	case levelFolder:
		es := m.folder.GetEntries()
		if m.fErr != nil || i < 0 || i >= len(es) {
			return nil
		}
		if !es[i].GetDir() || es[i].GetLink() {
			return nil
		}
		m.cursors = append(m.cursors, i)
		m.dir = strings.TrimPrefix(m.dir+"/"+es[i].GetName(), "/")
		m.folder, m.fErr = nil, nil
		m.layoutTable()
		m.table.SetCursor(0)
		return m.fetchFolder()
	}
	return nil
}

func (m *installerModel) enterPartition(p *maitrev1.Partition) tea.Cmd {
	m.part, m.dir, m.level = p, "", levelFolder
	m.folder, m.fErr = nil, nil
	m.layoutTable()
	m.table.SetCursor(0)
	return m.fetchFolder()
}

// up goes back one folder, or out of the partition or disk.
func (m *installerModel) up() tea.Cmd {
	if m.level == levelDisks {
		return nil
	}
	var cmd tea.Cmd
	switch {
	case m.level == levelFolder && m.dir != "":
		if i := strings.LastIndex(m.dir, "/"); i >= 0 {
			m.dir = m.dir[:i]
		} else {
			m.dir = ""
		}
		m.folder, m.fErr = nil, nil
		cmd = m.fetchFolder()
	case m.level == levelFolder:
		m.part, m.level = nil, levelPartitions
		// Came straight in from a disk with one partition: leave it too.
		if len(m.disk.GetPartitions()) == 1 {
			m.cursors = m.cursors[:max(0, len(m.cursors)-1)]
			m.disk, m.level = nil, levelDisks
		}
	default:
		m.disk, m.level = nil, levelDisks
	}
	m.layoutTable()
	if n := len(m.cursors); n > 0 {
		m.table.SetCursor(m.cursors[n-1])
		m.cursors = m.cursors[:n-1]
	}
	return cmd
}

// refreshChosen swaps the chosen disk for its new listing, or goes back to
// the list when it is gone.
func (m *installerModel) refreshChosen() {
	if m.disk == nil {
		return
	}
	for _, d := range m.disks {
		if d.GetPath() == m.disk.GetPath() {
			m.disk = d
			return
		}
	}
	m.disk, m.part, m.level, m.cursors = nil, nil, levelDisks, nil
	m.message = "That disk is gone."
}

func (m *installerModel) layoutTable() {
	// The panel's border and padding take 4, each cell's padding 2.
	w := max(60, m.width-4)
	var cols []table.Column
	var rows []table.Row
	switch m.level {
	case levelDisks:
		w -= 10
		path := 16
		size, kind := 9, 10
		name := max(14, (w-path-size-kind)/3)
		holds := max(14, w-path-size-kind-name)
		cols = []table.Column{{Title: "Disk", Width: name}, {Title: "Size", Width: size}, {Title: "Kind", Width: kind}, {Title: "Holds", Width: holds}, {Title: "Device", Width: path}}
		for _, d := range m.disks {
			kind := "internal"
			switch {
			case d.GetIsInstallerMedia():
				kind = "Installer"
			case d.GetRemovable():
				kind = "USB"
			}
			rows = append(rows, table.Row{diskName(d), wholeBytes(d.GetSizeBytes()), kind, Holds(d), d.GetPath()})
		}
	case levelPartitions:
		w -= 10
		size, fs, dev := 9, 12, 18
		name := max(14, (w-size-fs-dev)/2)
		note := max(14, w-size-fs-dev-name)
		cols = []table.Column{{Title: "Partition", Width: name}, {Title: "Size", Width: size}, {Title: "Filesystem", Width: fs}, {Title: "Device", Width: dev}, {Title: "", Width: note}}
		for _, p := range m.disk.GetPartitions() {
			note := ""
			if !p.GetBrowsable() {
				note = closedReason(m.disk, p)
			}
			rows = append(rows, table.Row{orDash(p.GetLabel()), wholeBytes(p.GetSizeBytes()), orDash(p.GetFstype()), p.GetDevice(), note})
		}
	case levelFolder:
		w -= 6
		size, when := 10, 17
		name := max(20, w-size-when)
		cols = []table.Column{{Title: "Name", Width: name}, {Title: "Size", Width: size}, {Title: "Modified", Width: when}}
		for _, e := range m.folder.GetEntries() {
			n, sz := e.GetName(), humanBytes(e.GetSizeBytes())
			switch {
			case e.GetLink():
				n, sz = n+" →", ""
			case e.GetDir():
				n, sz = n+"/", ""
			}
			mod := ""
			if ms := e.GetModifiedUnixMs(); ms > 0 {
				mod = time.UnixMilli(ms).Format("2006-01-02 15:04")
			}
			rows = append(rows, table.Row{n, sz, mod})
		}
	}
	// Columns and rows must agree in count: clear rows before columns,
	// which moves the cursor to the top, so it is put back.
	cursor := m.table.Cursor()
	m.table.SetRows(nil)
	m.table.SetColumns(cols)
	m.table.SetRows(rows)
	m.table.SetCursor(cursor)
	// As tall as its rows and header, up to the screen less the heading
	// and the bar.
	m.table.SetHeight(max(3, min(len(rows)+2, m.height-7)))
	if m.table.Cursor() >= len(rows) {
		m.table.SetCursor(max(0, len(rows)-1))
	}
	if m.table.Cursor() < 0 && len(rows) > 0 {
		m.table.SetCursor(0)
	}
}

func diskName(d *maitrev1.Disk) string {
	if d.GetModel() != "" {
		return d.GetModel()
	}
	return d.GetPath()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// Holds says in a few words what a disk has on it: its partitions of a
// gigabyte or more by label and filesystem, or "blank".
func Holds(d *maitrev1.Disk) string {
	ps := d.GetPartitions()
	if len(ps) == 0 {
		return "blank"
	}
	var big []string
	for _, p := range ps {
		if p.GetSizeBytes() < 1<<30 {
			continue
		}
		fs := fsName(p.GetFstype())
		switch {
		case p.GetLabel() != "" && fs != "":
			big = append(big, p.GetLabel()+" ("+fs+")")
		case p.GetLabel() != "":
			big = append(big, p.GetLabel())
		case fs != "":
			big = append(big, fs)
		}
	}
	if len(big) == 0 {
		return fmt.Sprintf("%d small partition(s)", len(ps))
	}
	return strings.Join(big, ", ")
}

// fsName is a filesystem type in words where blkid's name says little.
func fsName(fstype string) string {
	switch fstype {
	case "crypto_LUKS", "BitLocker":
		return "encrypted"
	case "LVM2_member":
		return "LVM"
	case "linux_raid_member":
		return "RAID"
	}
	return fstype
}

// closedReason says why a partition cannot be looked inside.
func closedReason(d *maitrev1.Disk, p *maitrev1.Partition) string {
	if d.GetIsInstallerMedia() {
		return "the Installer's own media"
	}
	switch p.GetFstype() {
	case "":
		return "no filesystem"
	case "crypto_LUKS", "BitLocker":
		return "encrypted"
	case "LVM2_member":
		return "LVM: its volumes are listed separately"
	case "linux_raid_member", "zfs_member", "ddf_raid_member", "isw_raid_member":
		return "part of a RAID set"
	case "swap":
		return "swap space"
	}
	return "cannot open " + p.GetFstype()
}

func (m *installerModel) View() string {
	var body, hint string
	switch m.page {
	case pageInstall:
		body = m.viewInstall()
		hint = keyHint.Render("r restart")
	case pageDisks:
		body = m.viewDisks()
		if m.level == levelDisks {
			hint = keyHint.Render("↑↓ select   enter look inside")
		} else {
			hint = keyHint.Render("↑↓ select   enter open   backspace back")
		}
	case pageLog:
		body = pager("maitre-installer", m.vp, m.follow, m.lErr)
		hint = keyHint.Render("↑↓ PgUp PgDn scroll")
	}
	if m.confirm {
		hint = warn.Bold(true).Render("Restart this machine?   [y] restart   [other] no")
	} else if m.message != "" {
		hint += "   " + warn.Render(m.message)
	}
	return screen(body, bottomBar(m.width, []string{"1 Install", "2 Disks", "3 Log"}, int(m.page), hint), m.height)
}

func (m *installerModel) viewInstall() string {
	s := m.status
	var id strings.Builder
	id.WriteString(bold.Render("Installer") + "\n")
	id.WriteString(row("Machine", bold.Render(s.Machine)))
	id.WriteString(row("Version", s.Version))
	if len(s.Addresses) == 0 {
		id.WriteString(row("Network", warn.Render("waiting for a cable and an address...")))
	} else {
		id.WriteString(row("Network", strings.Join(s.Addresses, ", ")))
	}
	if len(s.Identity) >= 16 {
		id.WriteString(row("Identity", s.Identity[:16]))
	}
	if s.Image != "" {
		id.WriteString(row("Installs", s.Image))
	}
	if s.Source != "" {
		id.WriteString(row("From", dim.Render(s.Source)))
	}
	box := panel.Render(strings.TrimRight(id.String(), "\n"))

	banner := accent.Render(ascii.Banner())
	bw, nw := lipgloss.Width(banner), lipgloss.Width(box)
	var top string
	if m.width >= bw+nw+2 {
		top = lipgloss.JoinHorizontal(lipgloss.Top, banner, strings.Repeat(" ", m.width-bw-nw), box)
	} else {
		top = lipgloss.JoinVertical(lipgloss.Left, banner, box)
	}

	var st strings.Builder
	p := s.Provisioning
	switch p.GetPhase() {
	case maitrev1.Provisioning_PHASE_INSTALLING, maitrev1.Provisioning_PHASE_CONFIGURING:
		st.WriteString(bold.Render("Installing") + "\n\n")
		st.WriteString(fmt.Sprintf("%s %3d%%\n", gauge(float64(p.GetPercent())), p.GetPercent()))
		st.WriteString(p.GetMessage() + "\n\n")
		st.WriteString(warn.Render("Do not power off.") + " " + dim.Render("The machine restarts by itself when done."))
	case maitrev1.Provisioning_PHASE_REBOOTING:
		st.WriteString(good.Render("Installed.") + " Restarting as a Node...")
	case maitrev1.Provisioning_PHASE_FAILED:
		st.WriteString(chartRed.Render("Install failed") + "\n\n")
		st.WriteString(p.GetMessage() + "\n\n")
		st.WriteString(dim.Render("Fix the problem in Maître Desktop and try again. Press 3 for the log."))
	default:
		if s.Code == "" {
			st.WriteString(bold.Render("Waiting for Maître Desktop") + "\n\n")
			st.WriteString("Open Maître Desktop on a computer on this network.\n")
			st.WriteString("It finds this machine by itself and shows a code to compare with this screen.\n\n")
			st.WriteString(dim.Render("Press 2 to see what is on this machine's disks."))
		} else {
			st.WriteString(bold.Render("Pairing code") + "\n\n")
			st.WriteString(accent.Render(ascii.Digits(spaced(s.Code), "  ")) + "\n\n")
			st.WriteString(fmt.Sprintf("Check that Maître Desktop shows the same code (%s).", s.Code))
		}
	}
	return top + "\n\n" + panel.Render(st.String()) + "\n"
}

// spaced puts a gap in the middle of a six-digit code: "123 456".
func spaced(code string) string {
	if len(code) == 6 {
		return code[:3] + " " + code[3:]
	}
	return code
}

func (m *installerModel) viewDisks() string {
	var head string
	switch m.level {
	case levelDisks:
		head = bold.Render("Disks in this machine") + dim.Render("   Look inside one to tell them apart. Nothing is changed by looking.")
	case levelPartitions:
		head = bold.Render(diskName(m.disk)) + dim.Render("  "+wholeBytes(m.disk.GetSizeBytes())+"  "+m.disk.GetPath())
	case levelFolder:
		name := orDash(m.part.GetLabel())
		if m.part.GetFstype() != "" {
			name += " (" + m.part.GetFstype() + ")"
		}
		head = dim.Render(diskName(m.disk)+" › ") + bold.Render(name) + dim.Render(" › /") + m.dir
		if f := m.folder; f != nil && f.GetTotalBytes() > 0 {
			head += dim.Render(fmt.Sprintf("   %s of %s used", wholeBytes(f.GetUsedBytes()), wholeBytes(f.GetTotalBytes())))
		}
	}
	var b strings.Builder
	b.WriteString(head + "\n")
	switch {
	case m.level == levelDisks && m.dErr != nil:
		b.WriteString("\n" + warn.Render(m.dErr.Error()) + "\n")
	case m.level == levelDisks && m.disks == nil:
		b.WriteString("\n" + dim.Render("reading the disks...") + "\n")
	case m.level == levelFolder && m.fErr != nil:
		b.WriteString("\n" + warn.Render(m.fErr.Error()) + "\n")
	case m.level == levelFolder && m.folder == nil:
		b.WriteString("\n" + dim.Render("opening...") + "\n")
	case m.level == levelFolder && len(m.folder.GetEntries()) == 0:
		b.WriteString("\n" + dim.Render("This folder is empty.") + "\n")
	default:
		b.WriteString(panel.Render(m.table.View()) + "\n")
		if m.level == levelFolder && m.folder.GetMore() > 0 {
			b.WriteString(dim.Render(fmt.Sprintf("and %d more not shown", m.folder.GetMore())) + "\n")
		}
	}
	return b.String()
}
