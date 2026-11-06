package debugconsole

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
)

// PullFloor mirrors services.PullFloor, the free space a Node insists on
// before it will pull an image: below it the image store is the problem,
// so that is where the Space gauge turns red. A test keeps the two equal.
const PullFloor = 4 << 30

// SystemFloor is the root filesystem the Console calls low, matching the
// Desktop's gauge. A root this full cannot stage an update (decision 324)
// and a journal this full is the next outage.
const SystemFloor = 3 << 30

// Run drives the Console on this terminal until ctx ends or the user quits.
func Run(ctx context.Context, src Source, addr string) error {
	m := newModel(src, addr)
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen(), tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout))
	_, err := p.Run()
	if err != nil && ctx.Err() != nil {
		return nil
	}
	return err
}

type page int

const (
	pageOverview page = iota
	pageSpace
	pageServices
	pageLogs
	pageFiles
	pageRun
)

var pageNames = []string{"1 Overview", "2 Space", "3 Services", "4 Logs", "5 Files", "6 Run"}

// Each message carries what one call answered, so a slow call never
// blocks the screen and a failed one shows on its own page only.
type tickMsg time.Time
type infoMsg struct {
	info    *maitrev1.NodeInfo
	update  *maitrev1.UpdateStatus
	metrics *maitrev1.NodeMetrics
	err     error
}
type imagesMsg struct {
	res *maitrev1.ListImagesResponse
	err error
}
type servicesMsg struct {
	rows []*maitrev1.ServiceStatus
	err  error
}
type logsMsg struct {
	source string
	res    *maitrev1.GetNodeLogsResponse
	err    error
}
type fileMsg struct {
	path    string
	content []byte
	err     error
}
type runMsg struct {
	line   string
	output string
	err    error
}
type doneMsg struct {
	text string
	err  error
}

type confirmation struct {
	prompt string
	run    func(*model) tea.Cmd
}

type model struct {
	src  Source
	addr string
	page page

	width, height int

	info     *maitrev1.NodeInfo
	update   *maitrev1.UpdateStatus
	metrics  *maitrev1.NodeMetrics
	infoErr  error
	images   *maitrev1.ListImagesResponse
	imgErr   error
	imgTable table.Model
	services []*maitrev1.ServiceStatus
	svcErr   error
	svcTable table.Model

	logSources []*maitrev1.LogSource
	logSource  int
	logs       []string
	logErr     error
	logVP      viewport.Model
	follow     bool

	path    textinput.Model
	fileVP  viewport.Model
	fileOf  string
	fileErr error

	cmd    textinput.Model
	runVP  viewport.Model
	ranAt  string
	runErr error

	confirm *confirmation
	// message is the last thing an action said: a result, or a refusal.
	message string
	working string
}

func newModel(src Source, addr string) *model {
	m := &model{src: src, addr: addr, follow: true}
	m.imgTable = newTable()
	m.svcTable = newTable()
	m.logVP = viewport.New(80, 10)
	m.fileVP = viewport.New(80, 10)
	m.runVP = viewport.New(80, 10)

	m.path = textinput.New()
	m.path.Prompt = "path  "
	m.path.Placeholder = "/etc/containers/storage.conf"
	m.path.SetSuggestions(Suggestions)
	m.path.ShowSuggestions = true

	m.cmd = textinput.New()
	m.cmd.Prompt = "run   "
	m.cmd.Placeholder = "bootc status"
	m.cmd.SetSuggestions(Commands)
	m.cmd.ShowSuggestions = true
	m.path.Focus()
	return m
}

// tableBudget is how much width the columns of a table may add up to: the
// terminal, less the panel's border and padding, less the padding the
// table puts on either side of every cell.
func tableBudget(width, columns int) int {
	const panelChrome = 4 // a border and a padding of one, both sides
	return max(40, width-panelChrome-2*columns)
}

func newTable() table.Model {
	t := table.New(table.WithFocused(true), table.WithHeight(10))
	s := table.DefaultStyles()
	s.Header = s.Header.Bold(true).BorderBottom(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("6"))
	s.Selected = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("6"))
	t.SetStyles(s)
	return t
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(tick(), m.fetchInfo(), m.fetchImages(), m.fetchServices(), m.fetchLogs())
}

func tick() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// callTimeout is generous: a Node being diagnosed is a Node that answers
// slowly. Prune and the store move get their own, much longer, budget.
const callTimeout = 30 * time.Second

func (m *model) fetchInfo() tea.Cmd {
	src := m.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		info, err := src.Info(ctx)
		if err != nil {
			return infoMsg{err: err}
		}
		// The update status and the gauges are separate failures: an older
		// Node, or one whose metrics are not ready, still has a name.
		upd, _ := src.UpdateStatus(ctx)
		mt, _ := src.Metrics(ctx)
		return infoMsg{info: info, update: upd, metrics: mt}
	}
}

func (m *model) fetchImages() tea.Cmd {
	src := m.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		res, err := src.Images(ctx)
		return imagesMsg{res, err}
	}
}

func (m *model) fetchServices() tea.Cmd {
	src := m.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		rows, err := src.Services(ctx)
		return servicesMsg{rows, err}
	}
}

func (m *model) fetchLogs() tea.Cmd {
	src, source := m.src, m.currentSource()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		res, err := src.Logs(ctx, source, 500)
		return logsMsg{source, res, err}
	}
}

// currentSource is the journal the Logs page shows; "" is the agent, which
// is what a Node answers first.
func (m *model) currentSource() string {
	if len(m.logSources) == 0 {
		return ""
	}
	id := m.logSources[m.logSource%len(m.logSources)].GetId()
	if id == "agent" {
		return ""
	}
	return id
}

func (m *model) readFile(path string) tea.Cmd {
	src := m.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		b, err := src.ReadFile(ctx, path)
		return fileMsg{path, b, err}
	}
}

func (m *model) exec(line string) tea.Cmd {
	src := m.src
	// The screen splits on spaces; the command form passes argv exactly,
	// so anything that needs quoting belongs there.
	fields := strings.Fields(line)
	return func() tea.Msg {
		if len(fields) == 0 {
			return runMsg{line: line}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		out, err := src.Exec(ctx, fields[0], fields[1:])
		return runMsg{line, out, err}
	}
}

func (m *model) prune(keepRollback bool) tea.Cmd {
	src := m.src
	return func() tea.Msg {
		// A prune walks the store and can take minutes on a full Node.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		res, err := src.Prune(ctx, keepRollback)
		if err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{text: fmt.Sprintf("pruned %s: %d image(s) removed, %d kept, %s back",
			res.GetStoreDir(), len(res.GetRemoved()), res.GetKept(),
			humanBytes(res.GetFreeAfterBytes()-res.GetFreeBeforeBytes()))}
	}
}

func (m *model) moveStore() tea.Cmd {
	src := m.src
	return func() tea.Msg {
		// Tens of gigabytes copied with the Services stopped.
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
		defer cancel()
		res, err := src.MoveStore(ctx, "")
		if err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{text: fmt.Sprintf("store %s -> %s: %s moved, %s back on root, %d Service(s) restarted",
			res.GetFrom(), res.GetTo(), humanBytes(res.GetMovedBytes()),
			humanBytes(res.GetRootFreeAfterBytes()-res.GetRootFreeBeforeBytes()), len(res.GetServices()))}
	}
}

// editing reports whether a text field has the keyboard, in which case the
// digits are part of what is being typed rather than page keys.
func (m *model) editing() bool {
	return (m.page == pageFiles && m.path.Focused()) || (m.page == pageRun && m.cmd.Focused())
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.imgTable.SetHeight(max(3, m.height-12))
		m.svcTable.SetHeight(max(3, m.height-10))
		for _, vp := range []*viewport.Model{&m.logVP, &m.fileVP, &m.runVP} {
			vp.Width = max(20, m.width)
			vp.Height = max(3, m.height-10)
		}
		m.path.Width = max(20, m.width-20)
		m.cmd.Width = max(20, m.width-20)
		m.layoutImages()
		m.layoutServices()
		m.setLogs()
	case tickMsg:
		cmds := []tea.Cmd{tick()}
		// Always the gauges: the numbers that say a Node is in trouble are
		// the reason this app exists. The rest follows the page in view.
		cmds = append(cmds, m.fetchInfo())
		switch m.page {
		case pageSpace:
			cmds = append(cmds, m.fetchImages())
		case pageServices:
			cmds = append(cmds, m.fetchServices())
		case pageLogs:
			cmds = append(cmds, m.fetchLogs())
		}
		return m, tea.Batch(cmds...)
	case infoMsg:
		m.infoErr = msg.err
		if msg.err == nil {
			m.info, m.update, m.metrics = msg.info, msg.update, msg.metrics
		}
	case imagesMsg:
		m.images, m.imgErr = msg.res, msg.err
		m.layoutImages()
	case servicesMsg:
		m.services, m.svcErr = msg.rows, msg.err
		m.layoutServices()
	case logsMsg:
		m.logErr = msg.err
		if msg.err == nil {
			if len(msg.res.GetSources()) > 0 {
				m.logSources = msg.res.GetSources()
			}
			m.logs = msg.res.GetLines()
			m.setLogs()
		}
	case fileMsg:
		m.fileOf, m.fileErr = msg.path, msg.err
		m.fileVP.SetContent(wrap(string(msg.content), m.fileVP.Width))
		m.fileVP.GotoTop()
	case runMsg:
		m.ranAt, m.runErr = msg.line, msg.err
		m.runVP.SetContent(wrap(msg.output, m.runVP.Width))
		m.runVP.GotoTop()
	case doneMsg:
		m.working = ""
		if msg.err != nil {
			m.message = msg.err.Error()
		} else {
			m.message = msg.text
		}
		return m, m.fetchImages()
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	if m.confirm != nil {
		c := m.confirm
		m.confirm = nil
		if s := msg.String(); s == "y" || s == "Y" {
			m.working = c.prompt
			m.message = ""
			return m, c.run(m)
		}
		m.message = "cancelled"
		return m, nil
	}
	// A focused field takes the keys, so that a path can contain a digit.
	if m.editing() {
		switch msg.Type {
		case tea.KeyEsc:
			m.path.Blur()
			m.cmd.Blur()
			return m, nil
		case tea.KeyEnter:
			if m.page == pageFiles {
				if p := strings.TrimSpace(m.path.Value()); p != "" {
					return m, m.readFile(p)
				}
				return m, nil
			}
			if line := strings.TrimSpace(m.cmd.Value()); line != "" {
				return m, m.exec(line)
			}
			return m, nil
		}
		var cmd tea.Cmd
		if m.page == pageFiles {
			m.path, cmd = m.path.Update(msg)
		} else {
			m.cmd, cmd = m.cmd.Update(msg)
		}
		return m, cmd
	}
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "1":
		m.page = pageOverview
	case "2":
		m.page = pageSpace
		return m, m.fetchImages()
	case "3":
		m.page = pageServices
		return m, m.fetchServices()
	case "4":
		m.page = pageLogs
		return m, m.fetchLogs()
	case "5":
		m.page = pageFiles
		m.path.Focus()
		return m, nil
	case "6":
		m.page = pageRun
		m.cmd.Focus()
		return m, nil
	case "i", "enter":
		if m.page == pageFiles {
			m.path.Focus()
			return m, nil
		}
		if m.page == pageRun {
			m.cmd.Focus()
			return m, nil
		}
	case "r":
		m.message = ""
		switch m.page {
		case pageSpace:
			return m, m.fetchImages()
		case pageServices:
			return m, m.fetchServices()
		case pageLogs:
			return m, m.fetchLogs()
		default:
			return m, m.fetchInfo()
		}
	case "p":
		if m.page == pageSpace {
			m.confirm = &confirmation{
				prompt: "Delete every image no Service of this Node names?",
				run:    func(m *model) tea.Cmd { return m.prune(true) },
			}
		}
	case "P":
		if m.page == pageSpace {
			m.confirm = &confirmation{
				prompt: "Delete those and each Service's Rollback target, so Rollback stops working?",
				run:    func(m *model) tea.Cmd { return m.prune(false) },
			}
		}
	case "m":
		if m.page == pageSpace {
			m.confirm = &confirmation{
				prompt: "Move the image store off the root partition? This Node's Services stop for the copy.",
				run:    func(m *model) tea.Cmd { return m.moveStore() },
			}
		}
	case "left", "h":
		if m.page == pageLogs && len(m.logSources) > 0 {
			m.logSource = (m.logSource + len(m.logSources) - 1) % len(m.logSources)
			m.logs, m.follow = nil, true
			m.setLogs()
			return m, m.fetchLogs()
		}
	case "right", "l", "tab":
		if m.page == pageLogs && len(m.logSources) > 0 {
			m.logSource = (m.logSource + 1) % len(m.logSources)
			m.logs, m.follow = nil, true
			m.setLogs()
			return m, m.fetchLogs()
		}
	}
	var cmd tea.Cmd
	switch m.page {
	case pageSpace:
		m.imgTable, cmd = m.imgTable.Update(msg)
	case pageServices:
		m.svcTable, cmd = m.svcTable.Update(msg)
	case pageLogs:
		m.logVP, cmd = m.logVP.Update(msg)
		m.follow = m.logVP.AtBottom()
	case pageFiles:
		m.fileVP, cmd = m.fileVP.Update(msg)
	case pageRun:
		m.runVP, cmd = m.runVP.Update(msg)
	}
	return m, cmd
}

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
	m.logVP.SetContent(wrap(b.String(), m.logVP.Width))
	if m.follow {
		m.logVP.GotoBottom()
	}
}

func (m *model) layoutImages() {
	ref := max(20, tableBudget(m.width, 4)-13-10-26)
	m.imgTable.SetColumns([]table.Column{
		{Title: "Image", Width: 13},
		{Title: "Size", Width: 10},
		{Title: "Reference", Width: ref},
		{Title: "Named by", Width: 26},
	})
	rows := make([]table.Row, 0, len(m.images.GetImages()))
	for _, im := range m.images.GetImages() {
		// Decision 320: an image a Service names is one prune keeps, and an
		// image nothing names is the space a prune gets back. Saying which
		// is the whole use of the page.
		who := dim.Render("nothing")
		if im.GetService() != "" {
			who = im.GetService()
			if im.GetRollbackTarget() {
				who += " (Rollback)"
			}
		}
		id := im.GetId()
		rows = append(rows, table.Row{id[:min(12, len(id))], humanBytes(im.GetSizeBytes()), im.GetReference(), who})
	}
	m.imgTable.SetRows(rows)
	if m.imgTable.Cursor() < 0 && len(rows) > 0 {
		m.imgTable.SetCursor(0)
	}
}

func (m *model) layoutServices() {
	// The reference and the Service name share what the fixed columns
	// leave.
	rest := max(24, tableBudget(m.width, 6)-10-8-9-14)
	name := max(12, rest/2)
	image := max(12, rest-name)
	m.svcTable.SetColumns([]table.Column{
		{Title: "Service", Width: name},
		{Title: "State", Width: 10},
		{Title: "Health", Width: 8},
		{Title: "Restarts", Width: 9},
		{Title: "Network", Width: 14},
		{Title: "Image", Width: image},
	})
	rows := make([]table.Row, 0, len(m.services))
	for _, s := range m.services {
		health := dim.Render("—")
		if s.GetState() == maitrev1.ServiceStatus_STATE_RUNNING {
			if s.GetHealthy() {
				health = good.Render("healthy")
			} else {
				health = warn.Render("not yet")
			}
		}
		state := strings.ToLower(strings.TrimPrefix(s.GetState().String(), "STATE_"))
		if s.GetState() == maitrev1.ServiceStatus_STATE_FAILED {
			state = warn.Render(state)
		}
		// Decision 317: a Service on a Tunnel has no network of its own,
		// and the restart loop that started all of this was invisible
		// partly because its traffic was reported under another name.
		net := dim.Render("own")
		if s.GetNetworkSharedWith() != "" {
			net = s.GetNetworkSharedWith()
		}
		rows = append(rows, table.Row{s.GetSpec().GetName(), state, health, fmt.Sprint(s.GetRestarts()), net, s.GetSpec().GetImage()})
	}
	m.svcTable.SetRows(rows)
	if m.svcTable.Cursor() < 0 && len(rows) > 0 {
		m.svcTable.SetCursor(0)
	}
}
