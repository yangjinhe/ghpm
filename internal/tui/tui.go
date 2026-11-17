// Package tui implements the interactive terminal interface.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"ghpm/internal/app"
	"ghpm/internal/platform"
	"ghpm/internal/version"
)

type mode int

const (
	modeList mode = iota
	modeAdd
	modeDetail
	modeConfirm
	modeHelp
)

// Run starts the TUI. It blocks until the user quits.
func Run(parent context.Context, svc *app.Service) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	m := newModel(ctx, cancel, svc)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	m.program = p
	if _, err := p.Run(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

type model struct {
	ctx     context.Context
	cancel  context.CancelFunc
	svc     *app.Service
	program *tea.Program

	width, height int
	views         []app.AppView
	cursor        int
	offset        int
	selected      map[string]bool

	mode       mode
	addInput   textinput.Model
	detail     *app.AppView
	confirmMsg string
	confirmCmd func() tea.Cmd

	spinner  spinner.Model
	busy     bool
	progress map[string]*progressState

	status string
	err    error
	help   bool
}

type progressState struct {
	phase string
	pct   float64
	speed float64
}

func newModel(ctx context.Context, cancel context.CancelFunc, svc *app.Service) *model {
	ti := textinput.New()
	ti.Placeholder = "owner/repo or https://github.com/owner/repo"
	ti.CharLimit = 300
	ti.Prompt = "› "
	sp := spinner.New(spinner.WithSpinner(spinner.Dot))
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	return &model{
		ctx: ctx, cancel: cancel, svc: svc,
		selected: map[string]bool{},
		addInput: ti,
		spinner:  sp,
		progress: map[string]*progressState{},
		width:    90, height: 30,
	}
}

// ---- messages ----

type viewsMsg struct {
	views []app.AppView
	err   error
}
type checkDoneMsg struct{ results []app.CheckResult }
type installDoneMsg struct{ results []app.InstallResult }
type addDoneMsg struct {
	res *app.AddResult
	err error
}
type actionDoneMsg struct {
	text string
	err  error
}
type opEventMsg struct{ ev app.Event }

// ---- commands ----

func (m *model) loadViewsCmd() tea.Cmd {
	return func() tea.Msg {
		views, err := m.svc.Views()
		return viewsMsg{views: views, err: err}
	}
}

func (m *model) checkCmd(ids []string) tea.Cmd {
	return func() tea.Msg {
		pf := func(ev app.Event) { m.program.Send(opEventMsg{ev}) }
		results := m.svc.Check(m.ctx, ids, pf)
		return checkDoneMsg{results: results}
	}
}

func (m *model) startInstall(ids []string, verb string) tea.Cmd {
	if len(ids) == 0 || m.busy {
		return nil
	}
	m.busy = true
	m.err = nil
	m.progress = map[string]*progressState{}
	m.status = fmt.Sprintf("%s %d app(s)…", verb, len(ids))
	pf := func(ev app.Event) { m.program.Send(opEventMsg{ev}) }
	ctx := m.ctx
	svc := m.svc
	return func() tea.Msg {
		results := svc.InstallMany(ctx, ids, app.InstallOptions{Progress: pf})
		return installDoneMsg{results: results}
	}
}

func (m *model) startAdd(input string) tea.Cmd {
	if m.busy {
		return nil
	}
	m.busy = true
	m.err = nil
	m.status = "adding " + input + "…"
	m.progress = map[string]*progressState{}
	pf := func(ev app.Event) { m.program.Send(opEventMsg{ev}) }
	ctx, svc := m.ctx, m.svc
	go func() {
		res, err := svc.Add(ctx, app.AddOptions{Input: input, Install: true, Progress: pf})
		m.program.Send(addDoneMsg{res: res, err: err})
	}()
	return nil
}

func (m *model) actionCmd(fn func() (string, error)) tea.Cmd {
	return func() tea.Msg {
		text, err := fn()
		return actionDoneMsg{text: text, err: err}
	}
}

// ---- tea.Model ----

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.loadViewsCmd(), m.spinner.Tick)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.addInput.Width = min(70, max(20, m.width-8))
		m.clampOffset()
		return m, nil

	case viewsMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.views = msg.views
		m.pruneSelection()
		if m.cursor >= len(m.views) {
			m.cursor = max(0, len(m.views)-1)
		}
		m.clampOffset()
		// Kick off an update check once views are known.
		if !m.busy && len(m.views) > 0 && m.status == "" {
			m.status = "checking for updates…"
			return m, m.checkCmd(nil)
		}
		return m, nil

	case checkDoneMsg:
		updates, failed := 0, 0
		for _, r := range msg.results {
			if r.Err != nil {
				failed++
				continue
			}
			if r.UpdateAvailable && !r.Pinned {
				updates++
			}
		}
		switch {
		case failed > 0:
			m.err = fmt.Errorf("%d check(s) failed", failed)
			m.status = fmt.Sprintf("%d app(s) checked", len(msg.results))
		case updates > 0:
			m.status = fmt.Sprintf("%d update(s) available", updates)
		default:
			m.status = "all apps are up to date"
		}
		return m, m.loadViewsCmd()

	case opEventMsg:
		m.applyEvent(msg.ev)
		return m, nil

	case installDoneMsg:
		m.busy = false
		m.progress = map[string]*progressState{}
		ok, failed := 0, 0
		for _, r := range msg.results {
			for _, w := range r.Warnings {
				m.status = "warning: " + w
			}
			if r.Err != nil {
				failed++
				m.err = r.Err
			} else {
				ok++
			}
		}
		if failed == 0 {
			m.status = fmt.Sprintf("%d app(s) installed", ok)
		} else {
			m.status = fmt.Sprintf("%d installed, %d failed", ok, failed)
		}
		return m, m.loadViewsCmd()

	case addDoneMsg:
		m.busy = false
		m.progress = map[string]*progressState{}
		if msg.err != nil {
			m.err = msg.err
			m.status = ""
			return m, nil
		}
		m.err = nil
		m.status = fmt.Sprintf("added %s (%s)", msg.res.App.ID, msg.res.Chosen.Name)
		if msg.res.Installed != nil && msg.res.Installed.Err != nil {
			m.err = msg.res.Installed.Err
		}
		return m, m.loadViewsCmd()

	case actionDoneMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.err = nil
			m.status = msg.text
		}
		return m, m.loadViewsCmd()

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.busy {
			return m, cmd
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// Forward other messages to the text input while adding.
	if m.mode == modeAdd {
		var cmd tea.Cmd
		m.addInput, cmd = m.addInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *model) applyEvent(ev app.Event) {
	switch ev.Kind {
	case "resolve":
		m.progress[ev.ID] = &progressState{phase: "resolving"}
	case "install":
		m.progress[ev.ID] = &progressState{phase: "installing"}
	case "download":
		st, ok := m.progress[ev.ID]
		if !ok {
			st = &progressState{}
			m.progress[ev.ID] = st
		}
		switch {
		case ev.Download.Done:
			st.phase = "verifying"
			st.pct = 100
		case ev.Download.Total > 0:
			st.phase = "downloading"
			st.pct = ev.Download.Percent()
			st.speed = ev.Download.BytesPerSec
		default:
			st.phase = "downloading"
			st.speed = ev.Download.BytesPerSec
		}
	case "done":
		delete(m.progress, ev.ID)
	case "error":
		delete(m.progress, ev.ID)
		m.err = fmt.Errorf("%s: %w", ev.ID, ev.Err)
	case "check":
		// Ignore individual check chatter; summary arrives in checkDoneMsg.
	}
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch m.mode {
	case modeAdd:
		switch key {
		case "esc":
			m.mode = modeList
			m.addInput.Blur()
			return m, nil
		case "enter":
			input := strings.TrimSpace(m.addInput.Value())
			if input == "" {
				return m, nil
			}
			m.mode = modeList
			m.addInput.Blur()
			return m, m.startAdd(input)
		}
		var cmd tea.Cmd
		m.addInput, cmd = m.addInput.Update(msg)
		return m, cmd

	case modeDetail:
		m.mode = modeList
		return m, nil

	case modeHelp:
		m.mode = modeList
		return m, nil

	case modeConfirm:
		switch key {
		case "y", "Y", "enter":
			m.mode = modeList
			run := m.confirmCmd
			m.confirmCmd = nil
			m.confirmMsg = ""
			if run != nil {
				return m, run()
			}
			return m, nil
		case "n", "N", "esc", "q":
			m.mode = modeList
			m.confirmCmd = nil
			m.confirmMsg = ""
			return m, nil
		}
		return m, nil
	}

	// List mode. While an operation runs only quitting is allowed, so the
	// cursor cannot point at a list that is being reloaded underneath it.
	if m.busy && key != "q" && key != "ctrl+c" {
		return m, nil
	}
	switch key {
	case "ctrl+c", "q":
		m.cancel()
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.clampOffset()
		}
	case "down", "j":
		if m.cursor < len(m.views)-1 {
			m.cursor++
			m.clampOffset()
		}
	case "g":
		m.cursor = 0
		m.clampOffset()
	case "G":
		m.cursor = max(0, len(m.views)-1)
		m.clampOffset()
	case " ":
		if v := m.current(); v != nil {
			m.selected[v.ID] = !m.selected[v.ID]
			if !m.selected[v.ID] {
				delete(m.selected, v.ID)
			}
		}
	case "esc":
		m.selected = map[string]bool{}
		m.err = nil
	case "a":
		m.mode = modeAdd
		m.addInput.SetValue("")
		m.addInput.Focus()
		m.err = nil
		return m, textinput.Blink
	case "i":
		return m, m.startInstall(m.targetIDs(), "installing")
	case "u":
		return m, m.startInstall(m.targetIDs(), "updating")
	case "U":
		ids := m.svc.UpdateIDs()
		if len(ids) == 0 {
			m.status = "No known updates; press c to check again."
			return m, nil
		}
		m.confirmMsg = fmt.Sprintf("Update %d app(s)?", len(ids))
		m.confirmCmd = func() tea.Cmd { return m.startInstall(ids, "updating") }
		m.mode = modeConfirm
	case "c", "r":
		if m.busy {
			return m, nil
		}
		m.err = nil
		m.status = "checking for updates…"
		return m, m.checkCmd(nil)
	case "enter":
		if v := m.current(); v != nil {
			m.detail = v
			m.mode = modeDetail
		}
	case "p":
		v := m.current()
		if v == nil {
			return m, nil
		}
		if !v.Installed {
			m.err = fmt.Errorf("%s is not installed", v.ID)
			return m, nil
		}
		id, pin := v.ID, !v.Pinned
		mode := "unpinned"
		if pin {
			mode = "pinned"
		}
		return m, m.actionCmd(func() (string, error) {
			err := m.svc.SetPinned(id, pin)
			return fmt.Sprintf("%s %s", id, mode), err
		})
	case "d":
		v := m.current()
		if v == nil {
			return m, nil
		}
		id := v.ID
		m.confirmMsg = fmt.Sprintf("Remove %s and delete its files?", id)
		m.confirmCmd = func() tea.Cmd {
			return m.actionCmd(func() (string, error) {
				_, err := m.svc.Remove(id, false)
				return id + " removed", err
			})
		}
		m.mode = modeConfirm
	case "?":
		m.mode = modeHelp
	case "ctrl+l":
		return m, m.loadViewsCmd()
	}
	return m, nil
}

func (m *model) targetIDs() []string {
	var ids []string
	for _, v := range m.views {
		if m.selected[v.ID] {
			ids = append(ids, v.ID)
		}
	}
	if len(ids) == 0 {
		if v := m.current(); v != nil {
			ids = append(ids, v.ID)
		}
	}
	return ids
}

func (m *model) current() *app.AppView {
	if m.cursor < 0 || m.cursor >= len(m.views) {
		return nil
	}
	return &m.views[m.cursor]
}

func (m *model) pruneSelection() {
	alive := map[string]bool{}
	for _, v := range m.views {
		alive[v.ID] = true
	}
	for id := range m.selected {
		if !alive[id] {
			delete(m.selected, id)
		}
	}
}

func listHeight(m *model) int {
	h := m.height - 8
	if h < 3 {
		h = 3
	}
	return h
}

func (m *model) clampOffset() {
	h := listHeight(m)
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

// ---- view ----

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	okStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	updateStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	pinnedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("135"))
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	cursorStyle   = lipgloss.NewStyle().Background(lipgloss.Color("236"))
	boxStyle      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).BorderForeground(lipgloss.Color("240"))
)

func (m *model) View() string {
	if m.width == 0 {
		return ""
	}
	switch m.mode {
	case modeAdd:
		return m.viewAdd()
	case modeDetail:
		return m.viewDetail()
	case modeHelp:
		return m.viewHelp()
	case modeConfirm:
		return m.viewConfirm()
	}
	return m.viewList()
}

func (m *model) header() string {
	updates := 0
	for _, v := range m.views {
		if v.UpdateAvailable && !v.Pinned {
			updates++
		}
	}
	left := titleStyle.Render("ghpm") + dimStyle.Render(" "+version.String())
	mid := dimStyle.Render(platform.Current().String())
	right := dimStyle.Render(fmt.Sprintf("%d apps · %d updates", len(m.views), updates))
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(mid) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	return left + "  " + mid + strings.Repeat(" ", gap) + right
}

func (m *model) viewList() string {
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(strings.Repeat("─", m.width)))
	b.WriteString("\n")

	if len(m.views) == 0 {
		b.WriteString(dimStyle.Render("No apps configured yet. Press a to add a GitHub repository."))
		b.WriteString("\n")
	} else {
		h := listHeight(m)
		end := min(len(m.views), m.offset+h)
		for i := m.offset; i < end; i++ {
			b.WriteString(m.renderRow(i))
			b.WriteString("\n")
		}
		for i := end - m.offset; i < h; i++ {
			b.WriteString("\n")
		}
	}

	b.WriteString(dimStyle.Render(strings.Repeat("─", m.width)))
	b.WriteString("\n")
	if m.busy {
		b.WriteString(m.renderProgress())
	} else {
		b.WriteString(m.renderStatus())
	}
	b.WriteString("\n")
	b.WriteString(m.footer())
	return b.String()
}

func (m *model) renderRow(i int) string {
	v := m.views[i]
	sel := "○"
	if m.selected[v.ID] {
		sel = "●"
	}
	cursor := " "
	if i == m.cursor {
		cursor = "▸"
	}
	id := v.ID
	if w := 18; len(id) > w {
		id = id[:w-1] + "…"
	}
	ver := "not installed"
	switch {
	case v.Installed:
		ver = v.CurrentVersion
		if v.UpdateAvailable {
			ver = fmt.Sprintf("%s → %s", v.CurrentVersion, strings.TrimPrefix(v.LatestTag, "v"))
		}
	}
	status := ""
	switch {
	case !v.Installed:
		status = dimStyle.Render("available")
	case v.Pinned:
		status = pinnedStyle.Render("pinned")
	case v.CheckError != "":
		status = errorStyle.Render("check failed")
	case v.UpdateAvailable:
		status = updateStyle.Render("update")
	case v.LatestVersion == "":
		status = dimStyle.Render("not checked")
	default:
		status = okStyle.Render("up to date")
	}
	line := fmt.Sprintf("%s %s %-18s  %-24s  %s", cursor, sel, id, ver, status)
	if i == m.cursor {
		return cursorStyle.Render(padRight(line, m.width))
	}
	if m.selected[v.ID] {
		return selectedStyle.Render(line)
	}
	return line
}

func (m *model) renderProgress() string {
	ids := make([]string, 0, len(m.progress))
	for id := range m.progress {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return m.spinner.View() + " " + m.status
	}
	var parts []string
	for _, id := range ids {
		st := m.progress[id]
		switch st.phase {
		case "downloading":
			barW := 16
			filled := int(st.pct / 100 * float64(barW))
			bar := strings.Repeat("█", filled) + strings.Repeat("░", barW-filled)
			parts = append(parts, fmt.Sprintf("%s %s %3.0f%% %s/s", id, bar, st.pct, humanRate(st.speed)))
		default:
			parts = append(parts, fmt.Sprintf("%s %s", id, st.phase))
		}
	}
	line := m.spinner.View() + " " + strings.Join(parts, "   ")
	return truncate(line, m.width)
}

func (m *model) renderStatus() string {
	switch {
	case m.err != nil:
		return errorStyle.Render("✗ " + m.err.Error())
	case m.status != "":
		return dimStyle.Render(m.status)
	case len(m.selected) > 0:
		return dimStyle.Render(fmt.Sprintf("%d selected", len(m.selected)))
	}
	return ""
}

func (m *model) footer() string {
	if m.busy {
		return dimStyle.Render("working… (ctrl+c quits)")
	}
	keys := [][2]string{
		{"space", "select"}, {"i", "install"}, {"u", "update"}, {"U", "update all"},
		{"a", "add"}, {"p", "pin"}, {"d", "remove"}, {"enter", "details"},
		{"c", "check"}, {"?", "help"}, {"q", "quit"},
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, titleStyle.Render(k[0])+" "+dimStyle.Render(k[1]))
	}
	line := strings.Join(parts, dimStyle.Render(" · "))
	if lipgloss.Width(line) > m.width {
		line = strings.Join(parts[:5], dimStyle.Render(" · "))
	}
	return line
}

func (m *model) viewAdd() string {
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n\n")
	b.WriteString(titleStyle.Render("Add a GitHub repository"))
	b.WriteString("\n\n")
	b.WriteString(m.addInput.View())
	b.WriteString("\n\n")
	b.WriteString(dimStyle.Render("ghpm resolves the latest release, picks the asset for this machine"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("and installs it. Examples: jesseduffield/lazygit, sharkdp/fd"))
	b.WriteString("\n\n")
	b.WriteString(dimStyle.Render("enter: add and install   esc: cancel"))
	return b.String()
}

func (m *model) viewDetail() string {
	v := m.detail
	if v == nil {
		return ""
	}
	lines := []string{
		titleStyle.Render(v.ID),
		"",
		"repo:       " + v.Repo,
		"channel:    " + orDash(v.Channel),
		"installed:  " + orDash(v.CurrentVersion),
		"latest:     " + orDash(v.LatestTag),
		"status:     " + statusLine(v),
		"link:       " + orDash(v.LinkPath),
		"checked:    " + timeAgo(v.LastChecked),
	}
	if v.State != nil {
		lines = append(lines,
			"asset:      "+orDash(v.State.Asset),
			"sha256:     "+orDash(v.State.SHA256),
			"versions:   "+strings.Join(v.State.Versions, ", "),
		)
	}
	if v.CheckError != "" {
		lines = append(lines, errorStyle.Render("error:      "+v.CheckError))
	}
	lines = append(lines, "", dimStyle.Render("press any key to close"))
	return boxStyle.Render(strings.Join(lines, "\n"))
}

func (m *model) viewHelp() string {
	lines := []string{
		titleStyle.Render("Keyboard shortcuts"),
		"",
		"space        select / deselect row",
		"i            install selected (or cursor)",
		"u            update selected (or cursor)",
		"U            update every app with a known update",
		"a            add a repository",
		"p            pin / unpin (skips updates)",
		"d            remove installed app",
		"enter        show details",
		"c / r        check for updates",
		"esc          clear selection",
		"q            quit",
		"",
		dimStyle.Render("press any key to close"),
	}
	return boxStyle.Render(strings.Join(lines, "\n"))
}

func (m *model) viewConfirm() string {
	lines := []string{
		titleStyle.Render("Confirm"),
		"",
		m.confirmMsg,
		"",
		dimStyle.Render("y / enter: yes    n / esc: no"),
	}
	return boxStyle.Render(strings.Join(lines, "\n"))
}

func statusLine(v *app.AppView) string {
	switch {
	case !v.Installed:
		return "not installed"
	case v.Pinned:
		return "pinned"
	case v.CheckError != "":
		return "check failed"
	case v.UpdateAvailable:
		return "update available"
	case v.LatestVersion == "":
		return "not checked"
	}
	return "up to date"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func timeAgo(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return t.Local().Format("2006-01-02 15:04")
}

func humanRate(n float64) string {
	const unit = 1024.0
	if n < unit {
		return fmt.Sprintf("%.0f B", n)
	}
	div, exp := unit, 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", n/div, "KMGTP"[exp])
}

// truncate shortens s to at most width terminal cells without splitting runes.
func truncate(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r)) > width {
		r = r[:len(r)-1]
	}
	return string(r)
}

func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}
