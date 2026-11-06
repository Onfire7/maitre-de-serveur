package debugconsole

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// Styles. This app runs on a developer's terminal rather than on the
// Node's console, so unlike internal/tui it is not held to the eight
// colours the Linux console keeps (decision 212) and keeps its own set.
var (
	dim    = lipgloss.NewStyle().Faint(true)
	bold   = lipgloss.NewStyle().Bold(true)
	warn   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	bad    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	good   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	accent = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	tabOn  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("7")).Padding(0, 1)
	tabOff = lipgloss.NewStyle().Faint(true).Padding(0, 1)
	panel  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("6")).Padding(0, 1)
	label  = lipgloss.NewStyle().Faint(true).Width(13)
	// The amber pill is the Desktop Console's, on purpose: decision 323
	// wants a debug image advertised the same way wherever a Node is
	// described, because the risk is one running in production unnoticed.
	pill       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("3")).Padding(0, 1)
	pagerTitle = func() lipgloss.Style {
		b := lipgloss.RoundedBorder()
		b.Right = "├"
		return lipgloss.NewStyle().BorderStyle(b).Padding(0, 1)
	}()
	pagerInfo = func() lipgloss.Style {
		b := lipgloss.RoundedBorder()
		b.Left = "┤"
		return pagerTitle.BorderStyle(b)
	}()
)

func (m *model) View() string {
	body := m.header() + "\n\n" + m.body()
	if m.width > 0 {
		// A last resort only: every page is laid out to fit, and a test
		// holds them to it. This keeps a terminal narrower than anything
		// sensible from wrapping a table into nonsense.
		body = lipgloss.NewStyle().MaxWidth(m.width).Render(body)
	}
	return screen(body, bottomBar(m.width, pageNames, int(m.page), m.hint()), m.height)
}

// body is the page under the header, before the width is enforced.
func (m *model) body() string {
	var body string
	switch m.page {
	case pageOverview:
		body = m.viewOverview()
	case pageSpace:
		body = m.viewSpace()
	case pageServices:
		body = m.viewServices()
	case pageLogs:
		body = m.viewLogs()
	case pageFiles:
		body = m.viewFiles()
	case pageRun:
		body = m.viewRun()
	}
	return body
}

// header names the Node on every page, and says loudly when its image
// carries the debug surface.
func (m *model) header() string {
	who := m.addr
	if n := m.info.GetName(); n != "" {
		who = n + dim.Render("  ·  "+m.addr)
	}
	line := bold.Render("Maître Debug Console") + "   " + who
	switch {
	case m.info == nil:
		// Nothing answered yet, or the Node refused: said once, here.
	case m.info.GetDebugBuild():
		line += "   " + pill.Render("DEBUG BUILD")
	default:
		line += "   " + dim.Render("shipped image — Files and Run are not in it")
	}
	return line
}

func (m *model) hint() string {
	if m.confirm != nil {
		return warn.Bold(true).Render(m.confirm.prompt + "   [y] yes   [other] no")
	}
	if m.working != "" {
		return accent.Render("working: " + m.working)
	}
	var hint string
	switch m.page {
	case pageSpace:
		hint = "r refresh   p prune   P prune and drop Rollback   m move the store off root"
	case pageServices:
		hint = "r refresh   ↑↓ select"
	case pageLogs:
		hint = "←/→ source   ↑↓ PgUp PgDn scroll   r refresh"
	case pageFiles, pageRun:
		if m.editing() {
			hint = "enter run   tab complete   esc leave the field"
		} else {
			hint = "i edit   1-6 pages   ↑↓ scroll"
		}
	default:
		hint = "r refresh   q quit"
	}
	out := dim.Render(hint)
	if m.message != "" {
		out += "   " + warn.Render(m.message)
	}
	return out
}

// prose is an explanation wrapped to the terminal, so that the body's
// MaxWidth — which keeps a wide table from running off the edge — never
// cuts a sentence in half.
// wrap folds text to width, so no page is wider than the window and
// nothing is lost to a truncation.
func wrap(text string, width int) string {
	if width <= 0 {
		return text
	}
	return lipgloss.NewStyle().Width(width).Render(text)
}

func (m *model) prose(text string) string {
	st := dim
	if m.width > 4 {
		st = dim.Width(m.width - 2)
	}
	return st.Render(text)
}

func (m *model) viewOverview() string {
	if m.infoErr != nil {
		return bad.Render(m.infoErr.Error()) + "\n\n" + m.prose("The Console needs a Trusted Device identity this Node trusts: `maitre keygen`, then the key in its trust list.") + "\n"
	}
	if m.info == nil {
		return dim.Render("asking the Node...") + "\n"
	}
	n := m.info
	var id strings.Builder
	id.WriteString(bold.Render("Node") + "\n")
	id.WriteString(row("Name", bold.Render(n.GetName())))
	id.WriteString(row("Fleet", n.GetFleetId()))
	id.WriteString(row("Node id", shortID(n.GetNodeId())))
	id.WriteString(row("Version", n.GetVersion()))
	id.WriteString(row("Uptime", (time.Duration(n.GetUptimeSeconds()) * time.Second).String()))
	if n.GetDevMode() {
		id.WriteString(row("Mode", warn.Render("dev — not on Maître Server, so the image calls do not work")))
	}
	if n.GetDebugBuild() {
		id.WriteString(row("Debug", warn.Render("yes — this image answers Files and Run over the network")))
	} else {
		id.WriteString(row("Debug", dim.Render("no — a shipped image; Files and Run answer \"not a debug build\"")))
	}
	id.WriteString(row("Peers", fmt.Sprintf("%d", len(n.GetPeers()))))

	var img strings.Builder
	img.WriteString(bold.Render("Image") + "\n")
	if u := m.update; u == nil {
		img.WriteString(dim.Render("this Node did not answer GetUpdateStatus") + "\n")
	} else {
		img.WriteString(row("Booted", deployment(u.GetBooted())))
		if u.GetStaged() != nil {
			img.WriteString(row("Staged", deployment(u.GetStaged())))
		}
		if u.GetRollback() != nil {
			back := deployment(u.GetRollback())
			if u.GetRollbackQueued() {
				back = warn.Render("queued: ") + back
			}
			img.WriteString(row("Rollback", back))
		}
		state := strings.ToLower(strings.TrimPrefix(u.GetState().String(), "STATE_"))
		if u.GetMessage() != "" {
			state += dim.Render("  " + u.GetMessage())
		}
		img.WriteString(row("Update", state))
		if u.GetLastRollback() != "" {
			// Decision 325: greenboot rolled an update back, which is the
			// loudest thing a Node can say about itself.
			img.WriteString(row("Rolled back", warn.Render(u.GetLastRollback())))
		}
		if u.GetAvailable() != "" {
			img.WriteString(row("Available", shortID(u.GetAvailable())))
		}
	}

	idText, imgText := strings.TrimRight(id.String(), "\n"), strings.TrimRight(img.String(), "\n")
	top := lipgloss.JoinHorizontal(lipgloss.Top, panel.Render(idText), " ", panel.Render(imgText))
	if m.width > 0 && lipgloss.Width(top) > m.width {
		// Side by side does not fit. Stacked, they share a width, because
		// two boxes of different widths one above the other read as a
		// mistake rather than as a layout.
		w := max(lipgloss.Width(idText), lipgloss.Width(imgText)) + 2
		box := panel.Width(w)
		top = lipgloss.JoinVertical(lipgloss.Left, box.Render(idText), box.Render(imgText))
	}
	return top + "\n\n" + m.spacePanel() + "\n"
}

// spacePanel is the gauges, root and image store first. The incident of
// 2026-11-04 was a full root partition with no gauge for it anywhere, so
// these two are the first numbers the Console shows.
func (m *model) spacePanel() string {
	var b strings.Builder
	b.WriteString(bold.Render("Space and load") + "\n")
	mt := m.metrics
	if mt == nil {
		b.WriteString(dim.Render("no readings yet") + "\n")
		return panel.Render(strings.TrimRight(b.String(), "\n"))
	}
	b.WriteString(gaugeRow("Root", mt.GetSystemDiskUsedBytes(), mt.GetSystemDiskTotalBytes(), SystemFloor, ""))
	b.WriteString(gaugeRow("Image store", mt.GetImageStoreUsedBytes(), mt.GetImageStoreTotalBytes(), PullFloor, ""))
	if store := mt.GetImageStoreDir(); store != "" {
		if mt.GetImageStoreTotalBytes() == mt.GetSystemDiskTotalBytes() {
			// Decision 319: a store still on root is the thing to fix, and
			// the Space page's `m` is how.
			store = warn.Render(store + "  (still on root: Space, then m)")
		}
		b.WriteString(row("Store", store))
	}
	where := ""
	if mt.GetPoolFallback() {
		where = dim.Render("no Drive: on the OS disk")
	}
	b.WriteString(gaugeRow("Pool", mt.GetPoolUsedBytes(), mt.GetPoolTotalBytes(), 0, where))
	b.WriteString(gaugeRow("Memory", mt.GetMemoryUsedBytes(), mt.GetMemoryTotalBytes(), 0, ""))
	b.WriteString(row("CPU", fmt.Sprintf("%.1f%%   load %.2f", mt.GetCpuPercent(), mt.GetLoad_1M())))
	b.WriteString(row("Network", fmt.Sprintf("↓ %s/s   ↑ %s/s", humanBytes(uint64(mt.GetNetRxBytesPerSec())), humanBytes(uint64(mt.GetNetTxBytesPerSec())))))
	if mt.GetTemperatureC() > 0 {
		t := fmt.Sprintf("%.0f °C", mt.GetTemperatureC())
		if mt.GetTemperatureC() > 80 {
			t = warn.Render(t)
		}
		b.WriteString(row("Temp", t))
	}
	return panel.Render(strings.TrimRight(b.String(), "\n"))
}

func (m *model) viewSpace() string {
	if m.imgErr != nil {
		if TooOld(m.imgErr) {
			return m.prose("This Node does not answer the image list (decision 326): it is older than the call, or it is a dev Node, which serves no Services. On a debug build, `maitre debug run podman images` asks podman directly.") + "\n"
		}
		return bad.Render(m.imgErr.Error()) + "\n"
	}
	res := m.images
	if res == nil {
		return dim.Render("asking the Node...") + "\n"
	}
	var b strings.Builder
	used := res.GetTotalBytes() - res.GetFreeBytes()
	b.WriteString(gaugeRow("Store", used, res.GetTotalBytes(), PullFloor, ""))
	b.WriteString(row("Where", res.GetStoreDir()))
	var unnamed uint64
	for _, im := range res.GetImages() {
		if im.GetService() == "" {
			unnamed += im.GetSizeBytes()
		}
	}
	b.WriteString(row("Images", fmt.Sprintf("%d, of which %s no Service names", len(res.GetImages()), humanBytes(unnamed))))
	b.WriteString("\n")
	if len(res.GetImages()) == 0 {
		b.WriteString(dim.Render("No images: this Node has pulled nothing.") + "\n")
	} else {
		b.WriteString(panel.Render(m.imgTable.View()) + "\n")
	}
	return b.String()
}

func (m *model) viewServices() string {
	if m.svcErr != nil {
		return bad.Render(m.svcErr.Error()) + "\n"
	}
	if len(m.services) == 0 {
		return dim.Render("No Services on this Node.") + "\n"
	}
	return panel.Render(m.svcTable.View()) + "\n"
}

func (m *model) viewLogs() string {
	title := "agent"
	if len(m.logSources) > 0 {
		s := m.logSources[m.logSource%len(m.logSources)]
		title = s.GetLabel()
		if title == "" {
			title = s.GetId()
		}
		title += dim.Render(fmt.Sprintf("  %d/%d", m.logSource%len(m.logSources)+1, len(m.logSources)))
	}
	return pager(title, m.logVP, m.follow, m.logErr)
}

func (m *model) viewFiles() string {
	var b strings.Builder
	b.WriteString(m.path.View() + "\n\n")
	if m.info != nil && !m.info.GetDebugBuild() {
		b.WriteString(m.prose("This Node runs a shipped image, so it has no file-reading procedure to answer with. Build its image with -tags maitredebug to use this page; the Space, Services and Logs pages work on every Node.") + "\n")
		return b.String()
	}
	if m.fileErr != nil {
		return b.String() + bad.Render(m.fileErr.Error()) + "\n"
	}
	if m.fileOf == "" {
		b.WriteString(m.prose("A path under the Node's allowlist; tab completes the ones worth reading first.") + "\n")
		return b.String()
	}
	return b.String() + pager(m.fileOf, m.fileVP, false, nil)
}

func (m *model) viewRun() string {
	var b strings.Builder
	b.WriteString(m.cmd.View() + "\n\n")
	if m.info != nil && !m.info.GetDebugBuild() {
		b.WriteString(m.prose("This Node runs a shipped image, so it has no command to run: the procedure is absent from the binary rather than guarded inside it. Build its image with -tags maitredebug to use this page.") + "\n")
		return b.String()
	}
	if m.ranAt == "" {
		b.WriteString(m.prose("A command and its arguments, split on spaces; tab completes a few worth running first. For anything that needs quoting use `maitre debug run`, which passes the arguments through exactly.") + "\n")
		return b.String()
	}
	title := m.ranAt
	if m.runErr != nil {
		// The output is the diagnosis even when the command failed, so the
		// failure goes in the title and the output still shows.
		title += dim.Render("  ") + warn.Render(m.runErr.Error())
	}
	return b.String() + pager(title, m.runVP, false, nil)
}

// gaugeRow is one labelled bar: how full, how much is left, and red when
// what is left is below the floor the agent itself insists on.
func gaugeRow(name string, used, total uint64, floor uint64, note string) string {
	if total == 0 {
		return row(name, dim.Render("unknown"))
	}
	pct := 100 * float64(used) / float64(total)
	free := total - used
	left := fmt.Sprintf("%s free of %s", humanBytes(free), humanBytes(total))
	if floor > 0 && free < floor {
		left = bad.Render(left + "  low: under " + humanBytes(floor))
	}
	line := fmt.Sprintf("%s %3.0f%%   %s", gauge(pct), pct, left)
	if note != "" {
		line += "   " + dim.Render(note)
	}
	return row(name, line)
}

func deployment(d *maitrev1.ImageDeployment) string {
	if d == nil {
		return dim.Render("none")
	}
	out := d.GetImage()
	if v := d.GetVersion(); v != "" {
		out += dim.Render("  " + v)
	}
	if g := d.GetDigest(); g != "" {
		out += dim.Render("  " + shortID(g))
	}
	return out
}

// shortID keeps a fingerprint or digest readable: the algorithm prefix is
// noise, the first twelve characters are the identity.
func shortID(s string) string {
	s = strings.TrimPrefix(s, "sha256:")
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func row(k, v string) string { return label.Render(k) + v + "\n" }

// pager draws a log or a file in the viewport between a title box and a
// position box, as the Node's own screen does.
func pager(title string, vp viewport.Model, follow bool, err error) string {
	t := pagerTitle.Render(title)
	header := lipgloss.JoinHorizontal(lipgloss.Center, t, strings.Repeat("─", max(0, vp.Width-lipgloss.Width(t))))
	if err != nil {
		return header + "\n\n" + bad.Render(err.Error()) + "\n"
	}
	pct := fmt.Sprintf("%3.0f%%", vp.ScrollPercent()*100)
	if follow {
		pct = "follow"
	}
	info := pagerInfo.Render(pct)
	footer := lipgloss.JoinHorizontal(lipgloss.Center, strings.Repeat("─", max(0, vp.Width-lipgloss.Width(info))), info)
	return header + "\n" + vp.View() + "\n" + footer + "\n"
}

// bottomBar is the last line of every page: tabs on the left, the page's
// keys to the right of them.
func bottomBar(width int, names []string, current int, hint string) string {
	var tabs []string
	for i, n := range names {
		if i == current {
			tabs = append(tabs, tabOn.Render(n))
		} else {
			tabs = append(tabs, tabOff.Render(n))
		}
	}
	left := strings.Join(tabs, "")
	width = max(width, lipgloss.Width(left)+lipgloss.Width(hint)+2)
	lw, hw := lipgloss.Width(left), lipgloss.Width(hint)
	if start := (width - hw) / 2; start >= lw+2 {
		return left + strings.Repeat(" ", start-lw) + hint
	}
	return left + strings.Repeat(" ", max(2, width-lw-hw)) + hint
}

func screen(body, bar string, height int) string {
	body = strings.TrimRight(body, "\n")
	lines := strings.Count(body, "\n") + 1
	gap := max(1, height-lines-1)
	return body + strings.Repeat("\n", gap) + bar
}

const edge = "│"

func gauge(pct float64) string {
	const width = 24
	n := int(pct/100*width + 0.5)
	n = max(0, min(width, n))
	st := accent
	switch {
	case pct > 95:
		st = bad
	case pct > 85:
		st = warn
	}
	return edge + st.Render(strings.Repeat("█", n)) + dim.Render(strings.Repeat("░", width-n)) + edge
}

func humanBytes(n uint64) string {
	const unit = 1024
	switch {
	case n >= unit*unit*unit*unit:
		return fmt.Sprintf("%.1f TiB", float64(n)/(unit*unit*unit*unit))
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
