// Package tui is the Node's screen (decision 72 revised, 2026-10-25): a
// text interface on tty1 with no login. Pages: Status (banner, network,
// discovery, CPU and memory), Containers (sortable table with load), Logs
// (journal of the agent or a Service). Keys: 1 2 3 pages, r reboot.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/ascii"
)

// Status is the static and slow-changing part of the Status page.
type Status struct {
	Version   string
	Node      string
	Fleet     string
	Addresses []string
	Discovery string
	Trusted   int
	Services  []string
	Started   time.Time
}

// Container is one row of the Containers page.
type Container struct {
	Name     string
	Service  string // Maître Service name, if any
	State    string // running, exited, ...
	Status   string // podman's human status, e.g. "Up 3 hours (healthy)"
	Image    string
	CPUPct   float64
	MemBytes uint64
	MemPct   float64
	Restarts uint32
}

// Source feeds the screen; the agent implements it, the demo fakes it.
type Source interface {
	Status() Status
	Metrics() *maitrev1.NodeMetrics
	Containers(ctx context.Context) ([]Container, error)
	// Units lists journal units for the Logs page: the agent first, then Services.
	Units() []string
	Logs(ctx context.Context, unit string, lines int) ([]string, error)
	Reboot() error
}

// ErrQuit, returned by Source.Reboot, ends the screen instead (the demo).
var ErrQuit = errors.New("tui: quit")

// Run drives the screen on stdin/stdout until ctx ends.
func Run(ctx context.Context, src Source) error {
	quietConsole()
	m := newModel(src)
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen(), tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout))
	_, err := p.Run()
	if err != nil && ctx.Err() != nil {
		return nil
	}
	return err
}

// quietConsole keeps kernel messages off the screen (they still reach the
// journal). Root only; silently a no-op elsewhere.
func quietConsole() {
	_ = os.WriteFile("/proc/sys/kernel/printk", []byte("1\n"), 0o644)
}

type page int

const (
	pageStatus page = iota
	pageContainers
	pageLogs
)

type tickMsg time.Time
type containersMsg struct {
	rows []Container
	err  error
}
type logsMsg struct {
	unit  string
	lines []string
	err   error
}

type model struct {
	src        Source
	page       page
	width      int
	height     int
	status     Status
	metrics    *maitrev1.NodeMetrics
	containers []Container
	cErr       error
	table      table.Model
	sortCol    int
	sortDesc   bool
	units      []string
	unit       int
	logs       []string
	lErr       error
	vp         viewport.Model
	follow     bool // keep the log scrolled to its end
	confirm    bool // reboot confirmation pending
	message    string
	cpuHist    []float64 // percent, oldest first
	memHist    []float64
}

const histLen = 32

func (m *model) record() {
	if m.metrics == nil {
		return
	}
	memPct := 0.0
	if m.metrics.GetMemoryTotalBytes() > 0 {
		memPct = 100 * float64(m.metrics.GetMemoryUsedBytes()) / float64(m.metrics.GetMemoryTotalBytes())
	}
	m.cpuHist = append(m.cpuHist, m.metrics.GetCpuPercent())
	m.memHist = append(m.memHist, memPct)
	if len(m.cpuHist) > histLen {
		m.cpuHist = m.cpuHist[len(m.cpuHist)-histLen:]
	}
	if len(m.memHist) > histLen {
		m.memHist = m.memHist[len(m.memHist)-histLen:]
	}
}

var columns = []string{"Name", "State", "CPU", "Memory", "Restarts", "Image"}

func newModel(src Source) *model {
	t := table.New(table.WithFocused(true), table.WithHeight(10))
	s := table.DefaultStyles()
	s.Header = s.Header.Bold(true).BorderBottom(true).BorderStyle(lipgloss.NormalBorder())
	s.Selected = s.Selected.Reverse(true)
	t.SetStyles(s)
	m := &model{src: src, status: src.Status(), metrics: src.Metrics(), table: t, units: src.Units(), sortCol: 2, sortDesc: true, follow: true}
	m.vp = viewport.New(80, 10)
	m.record()
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(tick(), m.fetchContainers(), m.fetchLogs())
}

func tick() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) fetchContainers() tea.Cmd {
	src := m.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rows, err := src.Containers(ctx)
		return containersMsg{rows, err}
	}
}

func (m *model) fetchLogs() tea.Cmd {
	if len(m.units) == 0 {
		return nil
	}
	src, unit := m.src, m.units[m.unit%len(m.units)]
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		lines, err := src.Logs(ctx, unit, 300)
		return logsMsg{unit, lines, err}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.table.SetHeight(max(3, m.height-10))
		m.layoutTable()
		m.vp.Width = max(20, m.width-4)
		m.vp.Height = max(3, m.height-6)
		m.setLogs()
	case tickMsg:
		m.status = m.src.Status()
		m.metrics = m.src.Metrics()
		m.record()
		m.units = m.src.Units()
		cmds := []tea.Cmd{tick()}
		if m.page == pageContainers {
			cmds = append(cmds, m.fetchContainers())
		}
		if m.page == pageLogs {
			cmds = append(cmds, m.fetchLogs())
		}
		return m, tea.Batch(cmds...)
	case containersMsg:
		m.containers, m.cErr = msg.rows, msg.err
		m.layoutTable()
	case logsMsg:
		if len(m.units) > 0 && msg.unit == m.units[m.unit%len(m.units)] {
			m.logs, m.lErr = msg.lines, msg.err
			m.setLogs()
		}
	case tea.KeyMsg:
		if m.confirm {
			switch msg.String() {
			case "y", "Y":
				m.confirm = false
				m.message = "rebooting..."
				if err := m.src.Reboot(); errors.Is(err, ErrQuit) {
					return m, tea.Quit
				} else if err != nil {
					m.message = "reboot failed: " + err.Error()
				}
			default:
				m.confirm = false
				m.message = ""
			}
			return m, nil
		}
		switch msg.String() {
		case "1":
			m.page = pageStatus
		case "2":
			m.page = pageContainers
			return m, m.fetchContainers()
		case "3":
			m.page = pageLogs
			return m, m.fetchLogs()
		case "r":
			if m.page == pageStatus {
				m.confirm = true
			}
		case "s":
			m.sortCol = (m.sortCol + 1) % len(columns)
			m.layoutTable()
		case "S":
			m.sortDesc = !m.sortDesc
			m.layoutTable()
		case "left", "h":
			if m.page == pageLogs && len(m.units) > 0 {
				m.unit = (m.unit + len(m.units) - 1) % len(m.units)
				m.logs, m.follow = nil, true
				m.setLogs()
				return m, m.fetchLogs()
			}
		case "right", "l", "tab":
			if m.page == pageLogs && len(m.units) > 0 {
				m.unit = (m.unit + 1) % len(m.units)
				m.logs, m.follow = nil, true
				m.setLogs()
				return m, m.fetchLogs()
			}
		case "enter":
			if m.page == pageContainers {
				if r := m.table.SelectedRow(); r != nil {
					for i, u := range m.units {
						if strings.TrimSuffix(u, ".service") == r[0] {
							m.unit = i
						}
					}
					m.page = pageLogs
					m.logs, m.follow = nil, true
					m.setLogs()
					return m, m.fetchLogs()
				}
			}
		}
		if m.page == pageContainers {
			var cmd tea.Cmd
			m.table, cmd = m.table.Update(msg)
			return m, cmd
		}
		if m.page == pageLogs {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			m.follow = m.vp.AtBottom()
			return m, cmd
		}
	}
	return m, nil
}

// setLogs loads the current lines into the viewport, highlighting warnings,
// and keeps following the end unless the user scrolled up.
func (m *model) setLogs() {
	var b strings.Builder
	for i, l := range m.logs {
		if strings.Contains(l, "level=ERROR") || strings.Contains(l, "level=WARN") {
			l = warn.Render(l)
		}
		b.WriteString(l)
		if i < len(m.logs)-1 {
			b.WriteString("\n")
		}
	}
	m.vp.SetContent(b.String())
	if m.follow {
		m.vp.GotoBottom()
	}
}

func (m *model) layoutTable() {
	w := max(60, m.width-6)
	name := max(12, (w-44)/2)
	image := max(12, w-44-name)
	m.table.SetColumns([]table.Column{
		{Title: sortTitle(m, 0), Width: name},
		{Title: sortTitle(m, 1), Width: 9},
		{Title: sortTitle(m, 2), Width: 7},
		{Title: sortTitle(m, 3), Width: 12},
		{Title: sortTitle(m, 4), Width: 9},
		{Title: sortTitle(m, 5), Width: image},
	})
	rows := append([]Container(nil), m.containers...)
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		var less bool
		switch m.sortCol {
		case 1:
			less = a.State < b.State
		case 2:
			less = a.CPUPct < b.CPUPct
		case 3:
			less = a.MemBytes < b.MemBytes
		case 4:
			less = a.Restarts < b.Restarts
		case 5:
			less = a.Image < b.Image
		default:
			less = a.Name < b.Name
		}
		if m.sortDesc {
			return !less && (a != b)
		}
		return less
	})
	out := make([]table.Row, 0, len(rows))
	for _, c := range rows {
		out = append(out, table.Row{c.Name, c.State, fmt.Sprintf("%.1f%%", c.CPUPct), humanBytes(c.MemBytes), fmt.Sprint(c.Restarts), c.Image})
	}
	m.table.SetRows(out)
}

func sortTitle(m *model, col int) string {
	if col != m.sortCol {
		return columns[col]
	}
	if m.sortDesc {
		return columns[col] + " ▼"
	}
	return columns[col] + " ▲"
}

// Styles. Colours are the terminal's own 16, which the Linux console has.
var (
	dim     = lipgloss.NewStyle().Faint(true)
	bold    = lipgloss.NewStyle().Bold(true)
	warn    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	good    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	accent  = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	tabOn   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("7")).Padding(0, 1)
	tabOff  = lipgloss.NewStyle().Faint(true).Padding(0, 1)
	panel   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("6")).Padding(0, 1)
	label   = lipgloss.NewStyle().Faint(true).Width(11)
	keyHint = lipgloss.NewStyle().Faint(true)
)

func (m *model) View() string {
	var body string
	switch m.page {
	case pageStatus:
		body = m.viewStatus()
	case pageContainers:
		body = m.viewContainers()
	case pageLogs:
		body = m.viewLogs()
	}
	// Bottom line of every page: tabs on the left, the page's keys centred
	// (or pushed right when the centre would overlap the tabs).
	names := []string{"1 Status", "2 Containers", "3 Logs"}
	var tabs []string
	for i, n := range names {
		if page(i) == m.page {
			tabs = append(tabs, tabOn.Render(n))
		} else {
			tabs = append(tabs, tabOff.Render(n))
		}
	}
	left := strings.Join(tabs, "")
	var hint string
	switch {
	case m.confirm:
		hint = warn.Bold(true).Render("Reboot this Node now? Services stop for about a minute.   [y] yes   [any other key] no")
	case m.page == pageStatus:
		hint = keyHint.Render("r reboot")
	case m.page == pageContainers:
		hint = keyHint.Render("s sort   S reverse   ↑↓ select   enter logs")
	case m.page == pageLogs:
		hint = keyHint.Render("←/→ unit   ↑↓ PgUp PgDn scroll")
	}
	if m.message != "" && !m.confirm {
		hint += "   " + warn.Render(m.message)
	}
	width := max(m.width, lipgloss.Width(left)+lipgloss.Width(hint)+2)
	lw, hw := lipgloss.Width(left), lipgloss.Width(hint)
	var bar string
	if start := (width - hw) / 2; start >= lw+2 {
		bar = left + strings.Repeat(" ", start-lw) + hint
	} else {
		bar = left + strings.Repeat(" ", max(2, width-lw-hw)) + hint
	}
	body = strings.TrimRight(body, "\n")
	lines := strings.Count(body, "\n") + 1
	gap := max(1, m.height-lines-1)
	return body + strings.Repeat("\n", gap) + bar
}

func row(k, v string) string { return label.Render(k) + v + "\n" }

func (m *model) viewStatus() string {
	s := m.status
	var id strings.Builder
	id.WriteString(bold.Render("Node") + "\n")
	id.WriteString(row("Name", bold.Render(s.Node)))
	id.WriteString(row("Fleet", s.Fleet))
	id.WriteString(row("Version", s.Version))
	if len(s.Addresses) == 0 {
		id.WriteString(row("Network", warn.Render("waiting for a cable...")))
	} else {
		id.WriteString(row("Network", strings.Join(s.Addresses, ", ")))
	}
	disc := s.Discovery
	if strings.HasPrefix(disc, "announced") {
		disc = good.Render("●") + " " + disc
	} else {
		disc = warn.Render("●") + " " + disc
	}
	id.WriteString(row("Discovery", disc))
	id.WriteString(row("Trusted", fmt.Sprintf("%d device(s)", s.Trusted)))
	id.WriteString(row("Uptime", time.Since(s.Started).Round(time.Second).String()))
	if len(s.Services) == 0 {
		id.WriteString(row("Services", dim.Render("none yet")))
	} else {
		id.WriteString(row("Services", strings.Join(s.Services, ", ")))
	}
	nodeBox := panel.Render(strings.TrimRight(id.String(), "\n"))

	// Banner on the left, the Node box flush with the top right corner.
	banner := accent.Render(ascii.Banner())
	bw, nw := lipgloss.Width(banner), lipgloss.Width(nodeBox)
	var top string
	if m.width >= bw+nw+2 {
		top = lipgloss.JoinHorizontal(lipgloss.Top, banner, strings.Repeat(" ", m.width-bw-nw), nodeBox)
	} else {
		top = lipgloss.JoinVertical(lipgloss.Left, banner, nodeBox)
	}

	var res strings.Builder
	res.WriteString(bold.Render("Resources") + "\n")
	if mt := m.metrics; mt != nil {
		rows := sparkline(m.cpuHist)
		res.WriteString(row("CPU", rows[0]))
		res.WriteString(row("", fmt.Sprintf("%s %5.1f%%   load %.2f", rows[1], mt.GetCpuPercent(), mt.GetLoad_1M())))
		res.WriteString(row("", rows[2]))
		memPct := 0.0
		if mt.GetMemoryTotalBytes() > 0 {
			memPct = 100 * float64(mt.GetMemoryUsedBytes()) / float64(mt.GetMemoryTotalBytes())
		}
		rows = sparkline(m.memHist)
		res.WriteString(row("Memory", rows[0]))
		res.WriteString(row("", fmt.Sprintf("%s %5.1f%%   %s of %s", rows[1], memPct, humanBytes(mt.GetMemoryUsedBytes()), humanBytes(mt.GetMemoryTotalBytes()))))
		res.WriteString(row("", rows[2]))
		if mt.GetPoolTotalBytes() > 0 {
			poolPct := 100 * float64(mt.GetPoolUsedBytes()) / float64(mt.GetPoolTotalBytes())
			where := ""
			if mt.GetPoolFallback() {
				where = dim.Render("  (no Drive: on the OS disk)")
			}
			res.WriteString(row("Pool", fmt.Sprintf("%s %5.1f%%   %s of %s%s", gauge(poolPct), poolPct, humanBytes(mt.GetPoolUsedBytes()), humanBytes(mt.GetPoolTotalBytes()), where)))
		}
		res.WriteString(row("Network", fmt.Sprintf("↓ %s/s   ↑ %s/s", humanBytes(uint64(mt.GetNetRxBytesPerSec())), humanBytes(uint64(mt.GetNetTxBytesPerSec())))))
		if mt.GetTemperatureC() > 0 {
			t := fmt.Sprintf("%.0f °C", mt.GetTemperatureC())
			if mt.GetTemperatureC() > 80 {
				t = warn.Render(t)
			}
			res.WriteString(row("Temp", t))
		}
	} else {
		res.WriteString(dim.Render("no readings yet") + "\n")
	}
	resBox := panel.Render(strings.TrimRight(res.String(), "\n"))

	var b strings.Builder
	b.WriteString(top + "\n\n" + resBox + "\n\n")
	b.WriteString(dim.Render("Open Maître Desktop on a computer on this network to manage it.") + "\n")
	return b.String()
}

// cap is the edge on both sides of charts and gauges: a box-drawing bar
// (in the console font) that stacks into one continuous edge.
const cap = "│"

// sparkline draws the history as a three-row strip, seven levels per
// column, using only ▄ and █ (the console font has no eighth blocks), with
// the same edge as the gauges on either side.
func sparkline(hist []float64) [3]string {
	const cols = histLen
	var grid [3][]rune
	for r := range grid {
		grid[r] = []rune(strings.Repeat(" ", cols))
	}
	off := cols - len(hist)
	for i, v := range hist {
		lvl := int(v/100*6 + 0.5)
		lvl = max(0, min(6, lvl))
		// Fill from the bottom row (2) upwards, two levels per row.
		for r := 2; r >= 0 && lvl > 0; r-- {
			if lvl >= 2 {
				grid[r][off+i] = '█'
				lvl -= 2
			} else {
				grid[r][off+i] = '▄'
				lvl = 0
			}
		}
	}
	st := accent
	if len(hist) > 0 && hist[len(hist)-1] > 85 {
		st = warn
	}
	var out [3]string
	for r := range grid {
		out[r] = cap + st.Render(string(grid[r])) + cap
	}
	return out
}

func (m *model) viewContainers() string {
	var b strings.Builder
	if m.cErr != nil {
		b.WriteString(warn.Render(m.cErr.Error()) + "\n")
	} else if len(m.containers) == 0 {
		b.WriteString(dim.Render("No containers. Install a Service from Maître Desktop.") + "\n")
	} else {
		b.WriteString(panel.Render(m.table.View()) + "\n")
	}
	return b.String()
}

func (m *model) viewLogs() string {
	var b strings.Builder
	var units []string
	for i, u := range m.units {
		name := strings.TrimSuffix(u, ".service")
		if i == m.unit%max(1, len(m.units)) {
			units = append(units, tabOn.Render(name))
		} else {
			units = append(units, tabOff.Render(name))
		}
	}
	b.WriteString(strings.Join(units, "") + "\n\n")
	if m.lErr != nil {
		b.WriteString(warn.Render(m.lErr.Error()) + "\n")
		return b.String()
	}
	pos := ""
	if !m.follow {
		pos = dim.Render(fmt.Sprintf("  %3.0f%%  (scrolled; End follows again)", m.vp.ScrollPercent()*100))
	}
	b.WriteString(panel.Render(m.vp.View()) + pos + "\n")
	return b.String()
}

func gauge(pct float64) string {
	const width = histLen
	n := int(pct/100*width + 0.5)
	n = max(0, min(width, n))
	st := accent
	if pct > 85 {
		st = warn
	}
	return cap + st.Render(strings.Repeat("█", n)) + dim.Render(strings.Repeat("░", width-n)) + cap
}

func humanBytes(n uint64) string {
	const unit = 1024
	switch {
	case n >= unit*unit*unit:
		return fmt.Sprintf("%.1f GiB", float64(n)/(unit*unit*unit))
	case n >= unit*unit:
		return fmt.Sprintf("%.0f MiB", float64(n)/(unit*unit))
	case n >= unit:
		return fmt.Sprintf("%.0f KiB", float64(n)/unit)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
