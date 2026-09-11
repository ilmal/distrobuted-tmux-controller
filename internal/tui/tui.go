// Package tui is the interactive fleet dashboard (bubbletea).
package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ilmal/distrobuted-tmux-controller/internal/client"
	"github.com/ilmal/distrobuted-tmux-controller/internal/colors"
	"github.com/ilmal/distrobuted-tmux-controller/internal/config"
	"github.com/ilmal/distrobuted-tmux-controller/internal/model"
	"github.com/ilmal/distrobuted-tmux-controller/internal/tmux"
)

type row struct {
	model.Session
	def   colors.Def
	stale bool
}

func (r row) key() string { return r.Host + "\x1f" + r.Name }

type Model struct {
	cfg  *config.Config
	rows []row
	cur  int

	sort       int // 0 color, 1 host, 2 name, 3 activity
	desc       bool
	filter     string
	totalShown int

	view  int // 0 list, 1 preview, 2 help
	vp    viewport.Model
	vpRow row

	hubOK   bool
	hubErr  string
	fetched time.Time

	width, height int

	input     textinput.Model
	inputMode string // "", "filter", "rename", "tag", "new"
	inputHost string
	inputRow  int

	colorPick    bool
	colorPickCur int
	hostPick     bool
	hostPickCur  int
	confirmRow   int

	status   string
	statusAt time.Time
	err      string
}

// ---- messages ----

type fleetMsg struct {
	fr    *model.FleetResponse
	err   error
	local bool
}

type tickMsg time.Time

type attachedMsg struct{ err error }

type previewMsg struct {
	content string
	err     error
}

type actionMsg struct {
	err  error
	note string
}

// ---- styles ----

var (
	sBar      = lipgloss.NewStyle().Foreground(lipgloss.Color("253")).Bold(true)
	sDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	sErr      = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	sOK       = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	sSel      = lipgloss.NewStyle().Background(lipgloss.Color("236"))
	sHead     = lipgloss.NewStyle().Foreground(lipgloss.Color("60")).Bold(true)
	sAttached = lipgloss.NewStyle().Foreground(lipgloss.Color("114")).Bold(true)
)

// ---- construction ----

func New(cfg *config.Config) Model {
	ti := textinput.New()
	ti.CharLimit = 64
	ti.Prompt = ""
	return Model{cfg: cfg, input: ti, confirmRow: -1}
}

func Run(cfg *config.Config) error {
	p := tea.NewProgram(New(cfg), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{fetchCmd(m.cfg), tickCmd()}
	if os.Getenv("TMUX") == "" {
		cmds = append(cmds, tea.SetWindowTitle("🧭 dtc · fleet"))
	}
	return tea.Batch(cmds...)
}

func tickCmd() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func fetchCmd(cfg *config.Config) tea.Cmd {
	return func() tea.Msg {
		fr, err := client.FetchFleet(cfg)
		if err == nil {
			return fleetMsg{fr: fr}
		}
		// hub unreachable — degraded local-only view
		sessions, lerr := tmux.List()
		if lerr != nil {
			return fleetMsg{err: err, local: true}
		}
		for i := range sessions {
			sessions[i].Host = cfg.Hostname
		}
		return fleetMsg{err: err, local: true, fr: &model.FleetResponse{
			Host: []model.Host{{Name: cfg.Hostname, LastSeen: time.Now(), Sessions: sessions}}},
		}
	}
}

// ---- update ----

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.vp.Width = msg.Width
		m.vp.Height = msg.Height - 6
		return m, nil

	case tickMsg:
		cmds := []tea.Cmd{tickCmd()}
		if time.Since(m.statusAt) > 4*time.Second && m.status != "" {
			m.status = ""
		}
		cmds = append(cmds, fetchCmd(m.cfg))
		return m, tea.Batch(cmds...)

	case fleetMsg:
		m.fetched = time.Now()
		if msg.local {
			m.hubOK = false
			if msg.err != nil {
				m.hubErr = tmux.Sanitize(msg.err.Error())
			}
		} else {
			m.hubOK = true
			m.hubErr = ""
		}
		if msg.fr != nil {
			m.loadFleet(msg.fr)
		}
		if m.width == 0 { // degenerate pty (0x0): pick usable defaults
			m.width, m.height = 80, 24
		}
		return m, nil

	case attachedMsg:
		if msg.err != nil {
			m.setErr("attach: " + msg.err.Error())
		}
		return m, fetchCmd(m.cfg)

	case previewMsg:
		if msg.err != nil {
			m.setErr("preview: " + msg.err.Error())
			m.view = 0
			return m, nil
		}
		m.vp.SetContent(msg.content)
		m.vp.GotoTop()
		m.view = 1
		return m, nil

	case actionMsg:
		if msg.err != nil {
			m.setErr(msg.err.Error())
		} else if msg.note != "" {
			m.status = msg.note
			m.statusAt = time.Now()
		}
		return m, fetchCmd(m.cfg)
	}

	if m.inputMode != "" {
		return m.updateInput(msg)
	}
	if m.colorPick {
		return m.updateColorPick(msg)
	}
	if m.hostPick {
		return m.updateHostPick(msg)
	}
	if m.confirmRow >= 0 {
		return m.updateConfirm(msg)
	}

	switch m.view {
	case 1:
		return m.updatePreview(msg)
	case 2:
		return m.updateHelp(msg)
	}
	return m.updateList(msg)
}

func (m *Model) setErr(s string) {
	m.err = s
	m.status = ""
	m.statusAt = time.Now()
}

func (m *Model) clearErr() { m.err = "" }

func (m Model) updateInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			if m.inputMode == "filter" {
				m.filter = ""
			}
			m.inputMode = ""
			m.input.Blur()
			m.input.SetValue("")
			return m, nil
		case tea.KeyEnter:
			val := strings.TrimSpace(m.input.Value())
			mode := m.inputMode
			m.inputMode = ""
			m.input.Blur()
			m.input.SetValue("")
			switch mode {
			case "filter":
				m.filter = val
				m.cur = 0
				return m, nil
			case "rename":
				return m, m.renameCmd(m.rows[m.inputRow], val)
			case "tag":
				return m, m.tagCmd(m.rows[m.inputRow], val)
			case "new":
				if val != "" {
					return m, m.newCmd(m.inputHost, val)
				}
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.inputMode == "filter" {
		m.filter = strings.TrimSpace(m.input.Value())
		m.cur = 0
	}
	return m, cmd
}

func (m Model) updateColorPick(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc", "q":
			m.colorPick = false
		case "up", "k":
			if m.colorPickCur > 0 {
				m.colorPickCur--
			}
		case "down", "j":
			if m.colorPickCur < len(colors.Palette) {
				m.colorPickCur++
			}
		case "enter":
			r := m.rows[m.cur]
			m.colorPick = false
			var color string
			if m.colorPickCur < len(colors.Palette) {
				color = colors.Palette[m.colorPickCur].Name
			}
			return m, m.colorCmd(r, color)
		}
	}
	return m, nil
}

func (m Model) pickableHosts() []string {
	set := map[string]bool{m.cfg.Hostname: true}
	for _, r := range m.rows {
		set[r.Host] = true
	}
	for h := range m.cfg.Hosts {
		set[h] = true
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

func (m Model) updateHostPick(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc", "q":
			m.hostPick = false
		case "up", "k":
			if m.hostPickCur > 0 {
				m.hostPickCur--
			}
		case "down", "j":
			if m.hostPickCur < len(m.pickableHosts())-1 {
				m.hostPickCur++
			}
		case "enter":
			hosts := m.pickableHosts()
			m.inputHost = hosts[m.hostPickCur]
			m.hostPick = false
			m.hostPickCur = 0
			m.inputMode = "new"
			m.input.Placeholder = "new session name on " + m.inputHost
			m.input.SetValue("")
			m.input.Focus()
			return m, textinput.Blink
		}
	}
	return m, nil
}

func (m Model) updateConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc", "q", "n":
			m.confirmRow = -1
		case "y", "enter":
			r := m.rows[m.confirmRow]
			m.confirmRow = -1
			return m, m.killCmd(r)
		}
	}
	return m, nil
}

func (m Model) updatePreview(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc", "q", "p":
			m.view = 0
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m Model) updateHelp(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc", "q", "?":
			m.view = 0
		}
	}
	return m, nil
}

func (m Model) updateList(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c", "q":
		return m, tea.Quit

	case "up", "k":
		if m.cur > 0 {
			m.cur--
		}
	case "down", "j":
		if m.cur < len(m.rows)-1 {
			m.cur++
		}
	case "pgup":
		m.cur -= 15
		if m.cur < 0 {
			m.cur = 0
		}
	case "pgdown":
		m.cur += 15
		if m.cur > len(m.rows)-1 {
			m.cur = len(m.rows) - 1
		}
	case "g", "home":
		m.cur = 0
	case "G", "end":
		m.cur = len(m.rows) - 1

	case "enter":
		if len(m.rows) > 0 {
			return m, m.attachCmd(m.rows[m.cur])
		}
	case "p":
		if len(m.rows) > 0 {
			m.vpRow = m.rows[m.cur]
			return m, previewCmd(m.cfg, m.rows[m.cur])
		}
	case "c":
		if len(m.rows) > 0 {
			m.colorPick = true
			m.colorPickCur = 0
		}
	case "t":
		if len(m.rows) > 0 {
			m.inputMode = "tag"
			m.inputRow = m.cur
			m.input.Placeholder = "tag for " + m.rows[m.cur].Name + " (empty clears)"
			m.input.SetValue(m.rows[m.cur].Tag)
			m.input.Focus()
			return m, textinput.Blink
		}
	case "r":
		if len(m.rows) > 0 {
			m.inputMode = "rename"
			m.inputRow = m.cur
			m.input.Placeholder = "rename " + m.rows[m.cur].Name + " to"
			m.input.SetValue(m.rows[m.cur].Name)
			m.input.Focus()
			return m, textinput.Blink
		}
	case "n":
		m.hostPick = true
		m.hostPickCur = 0
	case "K", "x":
		if len(m.rows) > 0 {
			m.confirmRow = m.cur
		}
	case "/":
		m.inputMode = "filter"
		m.input.Placeholder = "filter (name/host/tag)"
		m.input.SetValue(m.filter)
		m.input.Focus()
		return m, textinput.Blink
	case "s":
		m.sort = (m.sort + 1) % 4
		m.cur = 0
	case "S":
		m.desc = !m.desc
	case "R":
		return m, fetchCmd(m.cfg)
	case "?":
		m.view = 2
	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.cur = 0
		}
	}
	return m, nil
}

// ---- actions (tea.Cmd factories) ----

// route decides how to operate on `host`.
func (m Model) route(host string) (alias string, remote bool, err error) {
	if m.cfg.IsLocal(host) {
		return "", false, nil
	}
	alias, ok := m.cfg.SSHFor(host)
	if !ok || alias == "" {
		return "", false, fmt.Errorf("no ssh route for host %q (add [hosts.%s] to config)", host, host)
	}
	return alias, true, nil
}

func (m Model) doTmux(host string, args ...string) error {
	alias, remote, err := m.route(host)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if remote {
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = tmux.Quote(a)
		}
		cmd = exec.CommandContext(ctx, "ssh", "-o", "ConnectTimeout=8", alias, "tmux "+strings.Join(quoted, " "))
	} else {
		cmd = exec.CommandContext(ctx, "tmux", args...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(tmux.Sanitize(string(out))))
	}
	return nil
}

// kick asks the host's agent for an immediate heartbeat so the hub reflects
// the mutation right away.
func (m Model) kickAgent(host string) {
	alias, remote, err := m.route(host)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if remote {
		cmd = exec.CommandContext(ctx, "ssh", "-o", "ConnectTimeout=8", alias, "bash -lc 'dtc agent --once' 2>/dev/null || true")
	} else {
		self, err := os.Executable()
		if err != nil {
			return
		}
		cmd = exec.CommandContext(ctx, self, "agent", "--once")
	}
	_ = cmd.Run()
}

// runMutation runs the tmux mutation, kicks the agent, returns a cmd carrying
// the outcome. (kick is synchronous inside the cmd goroutine.)
func (m Model) runMutation(host string, note string, args ...string) tea.Cmd {
	return func() tea.Msg {
		err := m.doTmux(host, args...)
		if err == nil {
			m.kickAgent(host)
		}
		return actionMsg{err: err, note: note}
	}
}

func (m Model) attachCmd(r row) tea.Cmd {
	alias, remote, err := m.route(r.Host)
	if err != nil {
		m.setErr(err.Error())
		return nil
	}
	var cmd *exec.Cmd
	if remote {
		cmd = exec.Command("ssh", "-t", "-o", "ConnectTimeout=8", alias,
			"tmux new-session -A -s "+tmux.Quote(r.Name))
	} else {
		cmd = tmux.AttachCmd(r.Name)
	}
	if os.Getenv("TMUX") == "" {
		title := r.def.Emoji + " " + r.Name
		if remote {
			title += " · " + r.Host
		}
		fmt.Fprintf(os.Stdout, "\x1b]2;%s\x07", title)
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return attachedMsg{err} })
}

func previewCmd(cfg *config.Config, r row) tea.Cmd {
	return func() tea.Msg {
		var out []byte
		var err error
		if cfg.IsLocal(r.Host) {
			out, err = captureLocal(r.Name)
		} else {
			alias, ok := cfg.SSHFor(r.Host)
			if !ok || alias == "" {
				return previewMsg{err: fmt.Errorf("no ssh route for %q", r.Host)}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "ssh", "-o", "ConnectTimeout=8", alias,
				"tmux capture-pane -t "+tmux.Quote(r.Name)+" -p -S -3000")
			out, err = cmd.Output()
		}
		if err != nil {
			return previewMsg{err: err}
		}
		return previewMsg{content: tmux.Sanitize(string(out))}
	}
}

func captureLocal(name string) ([]byte, error) {
	return exec.Command("tmux", "capture-pane", "-t", name, "-p", "-S", "-3000").Output()
}

func (m Model) renameCmd(r row, newName string) tea.Cmd {
	if newName == "" || newName == r.Name {
		return nil
	}
	def := colors.For(newName, r.Color)
	return func() tea.Msg {
		err := m.doTmux(r.Host, "rename-session", "-t", r.Name, newName)
		if err == nil {
			err = m.doTmux(r.Host, "set-option", "-t", newName, "@dtc-title", def.Emoji+" "+newName)
		}
		if err == nil {
			m.kickAgent(r.Host)
		}
		return actionMsg{err: err, note: "renamed to " + newName}
	}
}

func (m Model) tagCmd(r row, tag string) tea.Cmd {
	return func() tea.Msg {
		var err error
		if tag == "" {
			err = m.doTmux(r.Host, "set-option", "-u", "-t", r.Name, "@dtc-tag")
		} else {
			err = m.doTmux(r.Host, "set-option", "-t", r.Name, "@dtc-tag", tag)
		}
		if err == nil {
			m.kickAgent(r.Host)
			perr := client.PatchMeta(m.cfg, model.MetaPatch{Host: r.Host, Name: r.Name, Tag: &tag})
			if perr != nil {
				// non-fatal: heartbeat will reconcile
			}
		}
		note := "tag set to " + tag
		if tag == "" {
			note = "tag cleared"
		}
		return actionMsg{err: err, note: note}
	}
}

func (m Model) colorCmd(r row, color string) tea.Cmd {
	return func() tea.Msg {
		var err error
		if color == "" { // auto
			err = m.doTmux(r.Host, "set-option", "-u", "-t", r.Name, "@dtc-color")
		} else {
			err = m.doTmux(r.Host, "set-option", "-t", r.Name, "@dtc-color", color)
		}
		def := colors.For(r.Name, color)
		if err == nil {
			err = m.doTmux(r.Host, "set-option", "-t", r.Name, "@dtc-title", def.Emoji+" "+r.Name)
		}
		if err == nil {
			m.kickAgent(r.Host)
			_ = client.PatchMeta(m.cfg, model.MetaPatch{Host: r.Host, Name: r.Name, Color: &color})
		}
		note := "color: " + def.Name
		if color == "" {
			note = "color: auto"
		}
		return actionMsg{err: err, note: note}
	}
}

func (m Model) killCmd(r row) tea.Cmd {
	return m.runMutation(r.Host, "killed "+r.Name, "kill-session", "-t", r.Name)
}

func (m Model) newCmd(host, name string) tea.Cmd {
	return m.runMutation(host, "created "+name+" on "+host, "new-session", "-d", "-s", name)
}

// ---- fleet loading ----

func (m *Model) loadFleet(fr *model.FleetResponse) {
	prevKey := ""
	if m.cur >= 0 && m.cur < len(m.rows) {
		prevKey = m.rows[m.cur].key()
	}
	var rows []row
	for _, h := range fr.Host {
		for _, s := range h.Sessions {
			rows = append(rows, row{Session: s, def: colors.For(s.Name, s.Color), stale: h.Stale()})
		}
	}
	m.rows = rows
	m.applyFilterSort()
	m.restoreCursor(prevKey)
}

func (m *Model) applyFilterSort() {
	f := strings.ToLower(m.filter)
	var rows []row
	for _, r := range m.rows {
		if f == "" ||
			strings.Contains(strings.ToLower(r.Name), f) ||
			strings.Contains(strings.ToLower(r.Host), f) ||
			strings.Contains(strings.ToLower(r.Tag), f) {
			rows = append(rows, r)
		}
	}
	m.totalShown = len(rows)
	less := m.less()
	if m.desc {
		sort.SliceStable(rows, func(i, j int) bool { return less(rows[j], rows[i]) })
	} else {
		sort.SliceStable(rows, func(i, j int) bool { return less(rows[i], rows[j]) })
	}
	m.rows = rows
}

func (m Model) less() func(a, b row) bool {
	byName := func(a, b row) bool { return a.Name < b.Name }
	switch m.sort {
	case 1:
		return func(a, b row) bool {
			if a.Host != b.Host {
				return a.Host < b.Host
			}
			return byName(a, b)
		}
	case 2:
		return byName
	case 3:
		return func(a, b row) bool { return a.Activity > b.Activity }
	default: // color groups in palette order
		return func(a, b row) bool {
			if a.def.Name != b.def.Name {
				return paletteIndex(a.def.Name) < paletteIndex(b.def.Name)
			}
			return byName(a, b)
		}
	}
}

func paletteIndex(name string) int {
	for i, d := range colors.Palette {
		if d.Name == name {
			return i
		}
	}
	return len(colors.Palette)
}

func (m *Model) restoreCursor(key string) {
	for i, r := range m.rows {
		if r.key() == key {
			m.cur = i
			return
		}
	}
	if m.cur > len(m.rows)-1 {
		m.cur = len(m.rows) - 1
	}
	if m.cur < 0 {
		m.cur = 0
	}
}

// ---- view ----

func (m Model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	switch m.view {
	case 1:
		return m.viewPreview()
	case 2:
		return m.viewHelp()
	}
	base := m.viewList()
	if ov := m.pickerOverlay(); ov != "" {
		return base + ov
	}
	return base
}

func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

func pad(s string, w int) string {
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}

func rel(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (m Model) sortLabel() string {
	names := []string{"color", "host", "name", "activity"}
	label := names[m.sort]
	if m.desc {
		label += " ↓"
	}
	return label
}

func (m Model) viewList() string {
	var b strings.Builder

	hub := sOK.Render("hub ok")
	if !m.hubOK {
		hub = sErr.Render("HUB UNREACHABLE — local only")
	}
	age := ""
	if !m.fetched.IsZero() {
		age = " · refreshed " + rel(time.Since(m.fetched)) + " ago"
	}
	attached := 0
	for _, r := range m.rows {
		if r.Attached {
			attached++
		}
	}
	b.WriteString(sBar.Render("🧭 dtc — tmux fleet") + sDim.Render("  "+hub+age) + "\n")
	b.WriteString(sDim.Render(fmt.Sprintf("%d sessions (%d shown) · %d attached · sort: %s", len(m.rows), m.totalShown, attached, m.sortLabel())))
	if m.filter != "" {
		b.WriteString(sOK.Render(" · filter: " + m.filter))
	}
	b.WriteString("\n\n")

	const colDot, colName, colHost, colWin, colAtt, colAct, colTag = 2, 24, 12, 4, 4, 5, 12
	used := colDot + colName + colHost + colWin + colAtt + colAct + colTag
	prevW := m.width - used - 1

	header := sHead.Render(pad("●", colDot) + pad("SESSION", colName) + pad("HOST", colHost) +
		pad("W", colWin) + pad("ATT", colAtt) + pad("ACT", colAct) + pad("TAG", colTag) + "PREVIEW")
	b.WriteString(header + "\n")

	visible := m.height - 8
	if visible < 3 {
		visible = 3
	}
	start := m.cur - visible/2
	if start > len(m.rows)-visible {
		start = len(m.rows) - visible
	}
	if start < 0 {
		start = 0
	}
	end := start + visible
	if end > len(m.rows) {
		end = len(m.rows)
	}

	for i := start; i < end; i++ {
		r := m.rows[i]
		sel := i == m.cur
		dot := lipgloss.NewStyle().Foreground(lipgloss.Color(strconv.Itoa(r.def.ANSI))).Render("●")
		name := trunc(r.Name, colName-1)
		if sel {
			name = sSel.Render("▌" + pad(name, colName-1))
		} else {
			name = " " + pad(name, colName-1)
		}
		host := pad(trunc(r.Host, colHost-1)+" ", colHost)
		win := pad(strconv.Itoa(r.Windows)+" ", colWin)
		att := pad("· ", colAtt)
		if r.Attached {
			att = pad(sAttached.Render("✓ "), colAtt)
		}
		act := pad(rel(time.Since(time.Unix(r.Activity, 0)))+" ", colAct)
		tag := pad(trunc(r.Tag, colTag-1)+" ", colTag)
		prev := sDim.Render(trunc(strings.ReplaceAll(r.Preview, "\n", " "), prevW))
		line := dot + name + host + win + att + act + tag + prev
		if r.stale {
			line = sDim.Render(line)
		}
		b.WriteString(line + "\n")
	}
	if len(m.rows) == 0 {
		b.WriteString(sDim.Render("  no sessions"+filterHint(m.filter)) + "\n")
	}

	b.WriteString("\n")
	status := ""
	switch {
	case m.err != "":
		status = sErr.Render("✗ " + trunc(m.err, m.width-4))
	case m.status != "":
		status = sOK.Render("✓ " + m.status)
	case m.inputMode == "filter":
		status = sDim.Render("filter: " + m.input.View())
	case m.inputMode != "":
		status = sBar.Render(m.inputMode + ": ") + m.input.View()
	}
	if status == "" {
		status = sDim.Render("enter attach · p preview · c color · t tag · r rename · n new · K kill · / filter · s sort · S reverse · ? help · q quit")
	}
	b.WriteString(status)
	return b.String()
}

func filterHint(f string) string {
	if f != "" {
		return " (filter: " + f + " — esc clears)"
	}
	return " — press n to create one"
}

func (m Model) viewPreview() string {
	head := sBar.Render("📄 "+m.vpRow.Name) + sDim.Render(" @ "+m.vpRow.Host+"  ·  esc close · j/k scroll")
	return head + "\n" + m.vp.View() + "\n" + sDim.Render("scroll: j/k/up/down/pgup/pgdn")
}

func (m Model) viewHelp() string {
	lines := []string{
		"🧭 dtc — distributed tmux controller",
		"",
		"navigation",
		"  j/k or ↑/↓     move           g/G  top/bottom    pgup/pgdn  page",
		"  enter          attach (ssh or local)",
		"",
		"view",
		"  p              live pane preview (3000 lines)",
		"  /              filter by name/host/tag (esc clears)",
		"  s              cycle sort: color → host → name → activity",
		"  S              reverse sort      R  force refresh",
		"",
		"actions on selected session",
		"  c              set color (updates Ghostty tab emoji everywhere)",
		"  t              set tag (empty clears)     r  rename",
		"  K              kill session (confirm with y)",
		"  n              new session on any host",
		"",
		"misc",
		"  ?              this help       q  quit",
		"",
		"colors/tags/title are stored as tmux user options (@dtc-*) on the owning",
		"host and synced to the hub by the agent, so every machine sees them.",
	}
	body := lipgloss.NewStyle().Padding(1, 2).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("60")).Render(strings.Join(lines, "\n"))
	return body + sDim.Render("\n  esc back")
}

func (m Model) pickerOverlay() string {
	if m.colorPick {
		var lines []string
		for i, d := range colors.Palette {
			cur := "  "
			if i == m.colorPickCur {
				cur = "▌ "
			}
			dot := lipgloss.NewStyle().Foreground(lipgloss.Color(strconv.Itoa(d.ANSI))).Render("●")
			lines = append(lines, cur+dot+" "+d.Name)
		}
		auto := "  ○ auto (from name)"
		if m.colorPickCur == len(colors.Palette) {
			auto = "▌ ○ auto (from name)"
		}
		lines = append(lines, auto)
		return overlay("set color — "+m.rows[m.cur].Name, strings.Join(lines, "\n"))
	}
	if m.hostPick {
		var lines []string
		hosts := m.pickableHosts()
		for i, h := range hosts {
			cur := "  "
			if i == m.hostPickCur {
				cur = "▌ "
			}
			local := ""
			if m.cfg.IsLocal(h) {
				local = sDim.Render(" (local)")
			}
			lines = append(lines, cur+"▣ "+h+local)
		}
		return overlay("new session on host", strings.Join(lines, "\n"))
	}
	if m.confirmRow >= 0 && m.confirmRow < len(m.rows) {
		r := m.rows[m.confirmRow]
		return overlay("kill "+r.Name+" @ "+r.Host+"?", sErr.Render("y = kill · n/esc = cancel"))
	}
	return ""
}

func overlay(title, body string) string {
	box := lipgloss.NewStyle().Padding(1, 2).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("250")).
		Render(sBar.Render(title) + "\n\n" + body)
	return "\n" + box
}
