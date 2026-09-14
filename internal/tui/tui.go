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

// groupKey is the color group a row belongs to ("" = uncolored).
func (r row) groupKey() string { return r.def.Name }

type Model struct {
	cfg *config.Config

	// allRows is every visible session (hidden hosts already excluded); rows is
	// that list after the open tab and the filter are applied. Keeping them
	// separate lets the tab and filter re-scope without losing the rest.
	allRows []row
	rows    []row
	cur     int

	fleet      []model.Host // raw last-fetched fleet, incl. hidden hosts
	showHidden bool
	hiddenCnt  int

	// palette is the fleet's colors in display order (labels included); order is
	// each color group's manual session ordering, both owned by the hub. They
	// are kept so the local-only fallback still renders the last known grouping.
	palette []model.PaletteEntry
	order   []model.GroupOrder

	// grouped is the colors view (color groups as headings, manual order, no
	// sorting); the alternative is a flat list you can sort. `V` toggles.
	grouped bool

	sort     int // 0 color, 1 host, 2 activity, 3 created, 4 name
	desc     bool
	filter   string
	totalAll int

	// tabHost is the host whose tab is open; "" means the "all" tab. On a
	// single host the HOST column is dropped so the session name and preview
	// get the extra width.
	tabHost string

	view  int // 0 list, 1 preview, 2 help
	vp    viewport.Model
	vpRow row

	hubOK   bool
	hubErr  string
	fetched time.Time

	width, height int

	input      textinput.Model
	inputMode  string // "", "filter", "rename", "tag", "new"
	inputHost  string
	inputRow   int
	inputColor string // color a new session should start with ("" = none)

	colorPick    bool
	colorPickCur int
	hostPick     bool
	hostPickCur  int
	palPick      bool
	palPickCur   int
	// palRename is the color key being renamed while inputMode == "palname".
	palRename string
	// pinCursor is a session key the cursor returns to after the next refresh
	// (a reorder moves the row under the cursor).
	pinCursor  string
	confirmRow int

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
	// keep is a session key the cursor should follow after the refresh. A
	// reorder moves the row the user is looking at, so without this the
	// selection would jump to whatever now occupies that line.
	keep string
}

// ---- styles ----

var (
	accent = lipgloss.Color("62")
	selBg  = lipgloss.Color("236")

	sTitle = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(accent).Bold(true).Padding(0, 1)
	sBar   = lipgloss.NewStyle().Foreground(lipgloss.Color("253")).Bold(true)
	sDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	sErr   = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	sOK    = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	sHead  = lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Bold(true)
	sRule  = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	sGroup = lipgloss.NewStyle().Foreground(lipgloss.Color("110")).Bold(true)

	sAttached = lipgloss.NewStyle().Foreground(lipgloss.Color("114")).Bold(true)
	sFresh    = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	sWarm     = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	sCool     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	sChipKey = lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(lipgloss.Color("249")).Bold(true).Padding(0, 1)
)

func actStyle(d time.Duration) lipgloss.Style {
	switch {
	case d < 5*time.Minute:
		return sFresh
	case d < time.Hour:
		return sWarm
	case d < 24*time.Hour:
		return sCool
	}
	return sDim
}

// list column widths (sessions table)
const colName, colHost, colAtt, colAct, colTag = 26, 14, 4, 5, 12

func keycap(k, label string) string {
	kc := lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(lipgloss.Color("249")).Bold(true).Render(" " + k + " ")
	return kc + sDim.Render(" "+label+" ")
}

// ---- construction ----

func New(cfg *config.Config) Model {
	ti := textinput.New()
	ti.CharLimit = 64
	ti.Prompt = ""
	return Model{
		cfg:        cfg,
		input:      ti,
		confirmRow: -1,
		grouped:    true, // colors view is the default; V gives the flat sortable list
		palette:    colors.DefaultPalette(),
	}
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
		m.vp.Width = max(20, msg.Width-4)
		m.vp.Height = max(5, msg.Height-6)
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
		} else {
			m.err = ""
			if msg.note != "" {
				m.status = msg.note
				m.statusAt = time.Now()
			}
			if msg.keep != "" {
				m.pinCursor = msg.keep
			}
		}
		return m, fetchCmd(m.cfg)
	}

	if m.inputMode != "" {
		return m.updateInput(msg)
	}
	if m.palPick {
		return m.updatePalPick(msg)
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
				m.applyFilterSort()
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
				m.applyFilterSort()
				m.cur = 0
				return m, nil
			case "rename":
				return m, m.renameCmd(m.rows[m.inputRow], val)
			case "tag":
				return m, m.tagCmd(m.rows[m.inputRow], val)
			case "palname":
				return m, m.renameColorCmd(m.palRename, val)
			case "new":
				if val != "" {
					return m, m.newCmd(m.inputHost, val, m.inputColor)
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

// pickerColorKeys is the color picker's entries in palette display order,
// ending with "" (no color).
func (m Model) pickerColorKeys() []string {
	out := make([]string, 0, len(colors.Palette)+1)
	for _, k := range m.paletteKeys() {
		if k != "" {
			out = append(out, k)
		}
	}
	return append(out, "")
}

// pickerIndex is where `key` sits in the picker, defaulting to the last entry.
func (m Model) pickerIndex(key string) int {
	for i, k := range m.pickerColorKeys() {
		if k == key {
			return i
		}
	}
	return len(colors.Palette)
}

// palKeys is the palette manager's entries: the colors only. Uncolored is not
// a palette color you can rename or order — it always sits below them.
func (m Model) palKeys() []string {
	keys := m.paletteKeys()
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if k != "" {
			out = append(out, k)
		}
	}
	return out
}

func (m Model) updatePalPick(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	keys := m.palKeys()
	n := len(keys)
	switch key.String() {
	case "esc", "q", "P":
		m.palPick = false
	case "up", "k":
		if m.palPickCur > 0 {
			m.palPickCur--
		}
	case "down", "j":
		if m.palPickCur < n-1 {
			m.palPickCur++
		}
	case "shift+up":
		if m.palPickCur > 0 {
			cur := keys[m.palPickCur]
			m.palPickCur--
			return m, m.moveGroupTo(cur, m.palPickCur)
		}
	case "shift+down":
		if m.palPickCur < n-1 {
			cur := keys[m.palPickCur]
			m.palPickCur++
			return m, m.moveGroupTo(cur, m.palPickCur)
		}
	case "r":
		if n > 0 {
			m.palRename = keys[m.palPickCur]
			m.inputMode = "palname"
			m.input.Placeholder = "name for " + keys[m.palPickCur] + " (was " + m.colorLabel(keys[m.palPickCur]) + ")"
			m.input.SetValue(m.colorLabel(keys[m.palPickCur]))
			m.input.Focus()
			return m, textinput.Blink
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
		if m.cfg.IsHidden(h) {
			continue
		}
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
			m.colorPickCur = m.pickerIndex(m.rows[m.cur].groupKey())
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
		// A new session starts in the color you are standing on, so creating
		// another "green" session is just n, type a name, enter.
		m.inputColor = ""
		if len(m.rows) > 0 {
			m.inputColor = m.rows[m.cur].groupKey()
		}
		m.hostPick = true
		m.hostPickCur = 0
	case "V":
		m.grouped = !m.grouped
		m.applyFilterSort()
		if m.grouped {
			m.status = "colors view — groups are manual (⇧↑/⇧↓ to reorder, o opens one)"
		} else {
			m.status = "flat list — sorting by " + m.sortLabel()
		}
		m.statusAt = time.Now()
	case "P":
		m.palPick = true
		m.palPickCur = 0
	case "o":
		if len(m.rows) > 0 {
			return m, m.openGroupCmd(m.rows[m.cur].groupKey())
		}
	case "shift+up":
		if m.grouped && len(m.rows) > 0 {
			return m, m.moveSession(m.cur, -1)
		}
	case "shift+down":
		if m.grouped && len(m.rows) > 0 {
			return m, m.moveSession(m.cur, 1)
		}
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
		m.sort = (m.sort + 1) % 5
		m.applyFilterSort()
		m.cur = 0
	case "S":
		m.desc = !m.desc
		m.applyFilterSort()
	case "1", "2", "3", "4", "5":
		m.sort = int(key.String()[0] - '1')
		m.applyFilterSort()
		m.cur = 0
	case "tab", "]":
		m.cycleTab(1)
	case "shift+tab", "[":
		m.cycleTab(-1)
	case "0":
		m.setTab(allTab)
	case "6", "7", "8", "9":
		// Jump straight to a host tab: all = 0, hosts = 6…9 in tab order.
		hosts := m.visibleHosts()
		if i := int(key.String()[0] - '6'); i < len(hosts) {
			m.setTab(hosts[i])
		}
	case "H":
		m.showHidden = !m.showHidden
		prevKey := ""
		if m.cur >= 0 && m.cur < len(m.rows) {
			prevKey = m.rows[m.cur].key()
		}
		m.buildRows()
		m.restoreCursor(prevKey)
		m.status = "hidden hosts shown"
		if !m.showHidden {
			m.status = "hidden hosts hidden"
		}
		m.statusAt = time.Now()
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
		title := r.def.Title(r.Name)
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
	// The color travels with the session; renaming does not re-color it.
	title := r.def.Title(newName)
	return func() tea.Msg {
		err := m.doTmux(r.Host, "rename-session", "-t", r.Name, newName)
		if err == nil {
			err = m.doTmux(r.Host, "set-option", "-t", newName, "@dtc-title", title)
		}
		if err == nil {
			m.kickAgent(r.Host)
		}
		return actionMsg{err: err, note: "renamed to " + newName, keep: r.Host + "\x1f" + newName}
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
		if color == "" { // clear it — the session goes back to no color
			err = m.doTmux(r.Host, "set-option", "-u", "-t", r.Name, "@dtc-color")
		} else {
			err = m.doTmux(r.Host, "set-option", "-t", r.Name, "@dtc-color", color)
		}
		title := colors.For(r.Name, color).Title(r.Name)
		if err == nil {
			err = m.doTmux(r.Host, "set-option", "-t", r.Name, "@dtc-title", title)
		}
		if err == nil {
			m.kickAgent(r.Host)
			_ = client.PatchMeta(m.cfg, model.MetaPatch{Host: r.Host, Name: r.Name, Color: &color})
		}
		note := "color: " + m.colorLabel(color)
		return actionMsg{err: err, note: note, keep: r.key()}
	}
}

func (m Model) killCmd(r row) tea.Cmd {
	return m.runMutation(r.Host, "killed "+r.Name, "kill-session", "-t", r.Name)
}

// newCmd creates a session, in `color` when the cursor was standing on a color
// group. The color is written before the agent is kicked so the new session
// never flashes uncolored in the fleet.
func (m Model) newCmd(host, name, color string) tea.Cmd {
	note := "created " + name + " on " + host
	if color != "" {
		note += " in " + m.colorLabel(color)
	}
	return func() tea.Msg {
		err := m.doTmux(host, "new-session", "-d", "-s", name)
		if err == nil && color != "" {
			err = m.doTmux(host, "set-option", "-t", name, "@dtc-color", color)
		}
		if err == nil {
			m.kickAgent(host)
		}
		return actionMsg{err: err, note: note}
	}
}

// groupHint describes the keys that act on the group the cursor is inside.
func (m Model) groupHint() string {
	return "o opens all · ⇧↑/⇧↓ reorder"
}

// openGroupCmd opens every session in one color group as a new window (tab) of
// the tmux session dtc is running in, each attached — dtc's own window plus one
// per session.
//
// Sessions on this machine become windows here directly. A session on another
// machine has no local tmux session until something attaches to it, so its
// window is a plain shell running `ssh -t <host> tmux attach`; that is the same
// command `enter` uses, just planted in a window instead of taking over the
// terminal.
func (m Model) openGroupCmd(group string) tea.Cmd {
	// Snapshot the group from the unfiltered rows: a filter narrows the view,
	// but "open this color" means the color. Order it the way the group is
	// listed, so the tabs come out in the order they were read.
	var members []row
	for _, r := range m.allRows {
		if r.groupKey() == group {
			members = append(members, r)
		}
	}
	ord, rank := m.ordMap(), m.groupRank()
	sort.SliceStable(members, func(i, j int) bool {
		return m.groupLess(members[i], members[j], rank, ord)
	})
	localOnly := m.tabHost != allTab && !m.cfg.IsLocal(m.tabHost)
	if localOnly {
		members = nil
	}
	label := m.colorLabel(group)
	into, inside := tmux.CurrentSession()
	return func() tea.Msg {
		if len(members) == 0 {
			if !inside {
				return actionMsg{err: fmt.Errorf("dtc is not running inside tmux — open %q from a tmux pane", label)}
			}
			return actionMsg{err: fmt.Errorf("no sessions in %q on this host", label)}
		}
		if !inside {
			return actionMsg{err: fmt.Errorf("not inside tmux: run dtc in a tmux pane to open %q here", label)}
		}
		opened := 0
		var firstErr error
		// Each window is placed after the one before it, so the tabs come out in
		// the order the group lists them. Anchoring every window to the *session*
		// would pin them all to its current window and reverse the group.
		anchor := into
		for _, r := range members {
			var cmd *exec.Cmd
			if m.cfg.IsLocal(r.Host) {
				cmd = tmux.AttachWindowCmd(r.Name)
			} else {
				alias, ok := m.cfg.SSHFor(r.Host)
				if !ok || alias == "" {
					if firstErr == nil {
						firstErr = fmt.Errorf("no ssh route for %q", r.Host)
					}
					continue
				}
				cmd = exec.Command("ssh", "-t", "-o", "ConnectTimeout=8", alias,
					"tmux new-session -A -s "+tmux.Quote(r.Name))
			}
			id, err := tmux.OpenWindow(anchor, cmd)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			anchor = id
			// Name the tab after the session once its pane has attached.
			time.Sleep(350 * time.Millisecond)
			title := r.def.Title(r.Name)
			if !m.cfg.IsLocal(r.Host) {
				title += " · " + r.Host
			}
			_ = tmux.NameWindow(id, title)
			opened++
		}
		if opened == 0 && firstErr != nil {
			return actionMsg{err: firstErr}
		}
		note := fmt.Sprintf("opened %d session%s from %s", opened, plural(opened), label)
		if firstErr != nil {
			note += " (some failed)"
		}
		return actionMsg{note: note}
	}
}

// moveSession moves the selected session one place within its color group and
// stores the group's new order on the hub.
//
// A group's members are spread across machines, so the order cannot live in a
// tmux option on any one of them — it is fleet state, and it goes to the hub.
func (m Model) moveSession(cur, delta int) tea.Cmd {
	if cur < 0 || cur >= len(m.rows) {
		return nil
	}
	group := m.rows[cur].groupKey()
	// The group's members in display order, ignoring the tab filter: reordering
	// while a filter or another tab hides half of them would otherwise write an
	// order that forgets the hidden half.
	keys := m.paletteKeys()
	rank := map[string]int{}
	for i, k := range keys {
		rank[k] = i
	}
	ord := m.ordMap()
	var members []row
	for _, r := range m.allRows {
		if r.groupKey() != group {
			continue
		}
		if m.tabHost != allTab && r.Host != m.tabHost {
			continue
		}
		members = append(members, r)
	}
	sort.SliceStable(members, func(i, j int) bool {
		oi, oj := ord[members[i].key()], ord[members[j].key()]
		switch {
		case oi > 0 && oj > 0:
			return oi < oj
		case oi > 0:
			return true
		case oj > 0:
			return false
		}
		return members[i].Name < members[j].Name
	})
	idx := -1
	for i, r := range members {
		if r.key() == m.rows[cur].key() {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	to := idx + delta
	if to < 0 || to >= len(members) {
		return nil
	}
	members[idx], members[to] = members[to], members[idx]

	items := make([]model.OrderedItem, 0, len(members))
	for _, r := range members {
		items = append(items, model.OrderedItem{Host: r.Host, Name: r.Name})
	}
	newKey := members[to].key()
	return func() tea.Msg {
		err := client.PutOrder(m.cfg, model.GroupOrder{Group: group, Items: items})
		return actionMsg{err: err, note: "reordered " + m.colorLabel(group), keep: newKey}
	}
}

// renameColorCmd sets a color's display label. The color's key — what every
// session actually stores — never changes, so renaming cannot orphan a session.
func (m Model) renameColorCmd(key, label string) tea.Cmd {
	label = colors.NormalizeLabel(label)
	if key == "" || label == "" {
		return nil
	}
	entries := make([]model.PaletteEntry, 0, len(m.paletteKeys()))
	for _, k := range m.paletteKeys() {
		if k == "" {
			continue
		}
		l := colors.LabelOf(m.palette, k)
		if k == key {
			l = label
		}
		entries = append(entries, model.PaletteEntry{Name: k, Label: l})
	}
	return func() tea.Msg {
		err := client.PutPalette(m.cfg, entries)
		note := "renamed color " + key + " to " + label
		if err != nil {
			note = ""
		}
		return actionMsg{err: err, note: note}
	}
}

// ---- fleet loading ----

func (m *Model) loadFleet(fr *model.FleetResponse) {
	prevKey := ""
	if m.cur >= 0 && m.cur < len(m.rows) {
		prevKey = m.rows[m.cur].key()
	}
	if m.pinCursor != "" {
		prevKey = m.pinCursor
		m.pinCursor = ""
	}
	m.fleet = fr.Host
	if len(fr.Colors) > 0 {
		m.palette = fr.Colors
	}
	if fr.Order != nil {
		m.order = fr.Order
	}
	m.buildRows()
	m.restoreCursor(prevKey)
}

// ordMap resolves "host\x1fname" to its manual 1-based position.
func (m *Model) ordMap() map[string]int {
	out := map[string]int{}
	for _, g := range m.order {
		for i, it := range g.Items {
			out[it.Host+"\x1f"+it.Name] = i + 1
		}
	}
	return out
}

// paletteKeys returns the color keys in display order.
func (m *Model) paletteKeys() []string { return colors.OrderOf(m.palette) }

// colorLabel is the display name of a color key.
func (m *Model) colorLabel(key string) string {
	if key == "" {
		return "no color"
	}
	return colors.LabelOf(m.palette, key)
}

// colorRank is a key's position in the display order, or len(keys) when it is
// unknown (so it sorts last rather than jumping to the front).
func (m *Model) colorRank(key string) int {
	for i, k := range m.paletteKeys() {
		if k == key {
			return i
		}
	}
	return len(colors.Palette)
}

// buildRows flattens the cached fleet into display rows, leaving out hosts
// marked hidden — either in this machine's config or by the host itself
// (heartbeat `hidden`) — unless reveal is on.
func (m *Model) buildRows() {
	var rows []row
	m.hiddenCnt = 0
	for _, h := range m.fleet {
		if m.cfg.HiddenHost(h.Name, h.Hidden) {
			m.hiddenCnt += len(h.Sessions)
			if !m.showHidden {
				continue
			}
		}
		for _, s := range h.Sessions {
			rows = append(rows, row{Session: s, def: colors.For(s.Name, s.Color), stale: h.Stale()})
		}
	}
	m.allRows = rows
	m.totalAll = len(rows)
	// A tab pointing at a host that is no longer visible falls back to "all".
	if m.tabHost != "" && m.tabHost != allTab && !m.tabVisible(m.tabHost) {
		m.tabHost = allTab
	}
	m.applyFilterSort()
}

const allTab = ""

// visibleHosts lists the hosts that are currently in view (hidden ones left
// out unless revealed), in tab order: local machine first, then alphabetical.
func (m *Model) visibleHosts() []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range m.fleet {
		if m.cfg.HiddenHost(h.Name, h.Hidden) && !m.showHidden {
			continue
		}
		if !seen[h.Name] {
			seen[h.Name] = true
			out = append(out, h.Name)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := m.cfg.IsLocal(out[i]), m.cfg.IsLocal(out[j])
		if li != lj {
			return li
		}
		return out[i] < out[j]
	})
	return out
}

func (m *Model) tabVisible(host string) bool {
	for _, h := range m.visibleHosts() {
		if h == host {
			return true
		}
	}
	return false
}

// tabCounts is how many sessions each tab holds: the "all" tab plus one entry
// per host, counted over everything in view (independent of the filter, so the
// tab labels stay stable while you type).
func (m *Model) tabCounts() (all int, perHost map[string]int) {
	perHost = map[string]int{}
	for _, h := range m.fleet {
		if m.cfg.HiddenHost(h.Name, h.Hidden) && !m.showHidden {
			continue
		}
		perHost[h.Name] += len(h.Sessions)
		all += len(h.Sessions)
	}
	return all, perHost
}

// cycleTab moves the open tab by delta (wrapping through "all").
func (m *Model) cycleTab(delta int) {
	tabs := append([]string{allTab}, m.visibleHosts()...)
	cur := 0
	for i, t := range tabs {
		if t == m.tabHost {
			cur = i
			break
		}
	}
	cur = (cur + delta + len(tabs)) % len(tabs)
	m.setTab(tabs[cur])
}

// setTab opens a host tab ("" = all) and re-scopes the visible rows.
func (m *Model) setTab(host string) {
	m.tabHost = host
	m.cur = 0
	m.applyFilterSort()
}

func (m *Model) applyFilterSort() {
	f := strings.ToLower(m.filter)
	var rows []row
	for _, r := range m.allRows {
		if m.tabHost != allTab && r.Host != m.tabHost {
			continue
		}
		if f == "" ||
			strings.Contains(strings.ToLower(r.Name), f) ||
			strings.Contains(strings.ToLower(r.Host), f) ||
			strings.Contains(strings.ToLower(r.Tag), f) {
			rows = append(rows, r)
		}
	}
	// Colors view: the order is manual. Groups follow the palette's display
	// order; inside a group, sessions you placed by hand come first in that
	// order, then everything else alphabetically — so a partly-ordered group
	// still reads predictably. Sorting keys do not apply here.
	if m.grouped {
		ord := m.ordMap()
		rank := m.groupRank()
		sort.SliceStable(rows, func(i, j int) bool {
			return m.groupLess(rows[i], rows[j], rank, ord)
		})
		m.rows = rows
		return
	}
	less := m.less()
	if m.desc {
		sort.SliceStable(rows, func(i, j int) bool { return less(rows[j], rows[i]) })
	} else {
		sort.SliceStable(rows, func(i, j int) bool { return less(rows[i], rows[j]) })
	}
	m.rows = rows
}

// groupLess is the colors-view ordering: groups by the palette's display rank,
// and inside a group by the manual position, then by name. openGroupCmd uses it
// too, so the tabs it opens appear in the same order the group is listed —
// opening a color should read down the list you are looking at.
func (m Model) groupLess(a, b row, rank, ord map[string]int) bool {
	ra, okA := rank[a.groupKey()]
	rb, okB := rank[b.groupKey()]
	if !okA {
		ra = len(colors.Palette)
	}
	if !okB {
		rb = len(colors.Palette)
	}
	if ra != rb {
		return ra < rb
	}
	oa, ob := ord[a.key()], ord[b.key()]
	switch {
	case oa > 0 && ob > 0:
		return oa < ob
	case oa > 0:
		return true
	case ob > 0:
		return false
	}
	return a.Name < b.Name
}

// groupRank is the palette display order as a rank map, with anything not in
// the palette (uncolored included) last.
func (m Model) groupRank() map[string]int {
	rank := map[string]int{}
	for i, k := range m.paletteKeys() {
		rank[k] = i
	}
	return rank
}

func (m Model) less() func(a, b row) bool {
	byName := func(a, b row) bool { return a.Name < b.Name }
	switch m.sort {
	case 1: // host — local machine first, then alphabetical
		rank := func(h string) int {
			if m.cfg.IsLocal(h) {
				return 0
			}
			return 1
		}
		return func(a, b row) bool {
			if ra, rb := rank(a.Host), rank(b.Host); ra != rb {
				return ra < rb
			}
			if a.Host != b.Host {
				return a.Host < b.Host
			}
			return byName(a, b)
		}
	case 2: // most recently active first
		return func(a, b row) bool { return a.Activity > b.Activity }
	case 3: // newest first
		return func(a, b row) bool { return a.Created > b.Created }
	case 4:
		return byName
	default: // uncolored first, then colors in palette order
		return func(a, b row) bool {
			if a.def.Name != b.def.Name {
				return m.colorRank(a.def.Name) < m.colorRank(b.def.Name)
			}
			return byName(a, b)
		}
	}
}

// moveGroupTo reorders the palette so `key` sits at display position `to`, and
// persists the new order. The palette is fleet-wide, so this changes how every
// dashboard groups colors.
func (m Model) moveGroupTo(key string, to int) tea.Cmd {
	keys := m.palKeys()
	from := -1
	for i, k := range keys {
		if k == key {
			from = i
			break
		}
	}
	if from < 0 || to < 0 || to >= len(keys) || to == from {
		return nil
	}
	moved := keys[from]
	// Remove it, then insert it at the target index.
	seq := append([]string(nil), keys[:from]...)
	seq = append(seq, keys[from+1:]...)
	seq = append(seq[:to], append([]string{moved}, seq[to:]...)...)

	entries := make([]model.PaletteEntry, 0, len(seq))
	for _, k := range seq {
		entries = append(entries, model.PaletteEntry{Name: k, Label: colors.LabelOf(m.palette, k)})
	}
	return func() tea.Msg {
		err := client.PutPalette(m.cfg, entries)
		note := "moved " + m.colorLabel(moved) + " to position " + strconv.Itoa(to+1)
		if err != nil {
			note = ""
		}
		return actionMsg{err: err, note: note}
	}
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
	names := []string{"color", "host", "activity", "created", "name"}
	label := names[m.sort]
	// natural direction: activity/created are newest-first (↓), the rest ascending
	natural := map[int]bool{2: true, 3: true}
	arrow := "↑"
	if natural[m.sort] != m.desc {
		arrow = "↓"
	}
	return label + " " + arrow
}

func (m Model) viewList() string {
	var b strings.Builder

	// ---- header: title chip + hub state + stats ----
	hub := sOK.Render("● hub ok")
	if !m.hubOK {
		hub = sErr.Render("● hub unreachable — local only")
	}
	age := ""
	if !m.fetched.IsZero() {
		if d := time.Since(m.fetched); d < time.Minute {
			age = sDim.Render("  refreshed just now")
		} else {
			age = sDim.Render("  refreshed " + rel(d) + " ago")
		}
	}
	sessions, attached, hostsTotal, hostsFresh := m.tabScope()
	b.WriteString(sTitle.Render("🧭 dtc") + sDim.Render(" distributed tmux controller  ") + hub + age + "\n")

	ctx := sDim.Render("sort ") + sHead.Render(m.sortLabel()) +
		sDim.Render(fmt.Sprintf("  ·  %d sessions", sessions))
	if m.tabHost != allTab {
		ctx += sDim.Render(" on ") + sHead.Render(m.tabHost)
	}
	if attached > 0 {
		ctx += sDim.Render("  ·  ") + sAttached.Render(fmt.Sprintf("%d attached", attached))
	}
	if hostsTotal > 0 {
		ctx += sDim.Render(fmt.Sprintf("  ·  %d/%d hosts up", hostsFresh, hostsTotal))
	}
	if m.hiddenCnt > 0 && !m.showHidden {
		ctx += sDim.Render(fmt.Sprintf("  ·  %d hidden (H shows them)", m.hiddenCnt))
	}
	if m.filter != "" {
		ctx += sDim.Render("  ·  filter ") + sOK.Render("/"+m.filter+"/")
	}
	b.WriteString(ctx + "\n")
	b.WriteString(m.tabBar() + "\n")
	b.WriteString(sRule.Render(strings.Repeat("─", max(20, m.width-1))) + "\n")

	// ---- column header ----
	// On a single-host tab the HOST column is dropped and its width goes to
	// the preview, so each row carries more content.
	single := m.tabHost != allTab
	hostW := colHost
	if single {
		hostW = 0
	}
	used := 1 + colName + hostW + colAtt + colAct + colTag + 1
	prevW := max(0, m.width-used)
	hdr := " " + pad("", 1) + pad("SESSION", colName)
	if !single {
		hdr += pad("HOST", colHost)
	}
	hdr += pad("ATT", colAtt) + pad("ACT", colAct) + pad("TAG", colTag) + "PREVIEW"
	b.WriteString(sHead.Render(hdr) + "\n")

	// ---- group headers ----
	// The colors view always groups. The flat list only groups for host and
	// color sort keys — and on a single-host tab the host is already named, so
	// host groups are dropped there.
	var groupOf func(row) string
	var groupHead func(key string, n, fresh int) string
	hostHead := func(key string, n, fresh int) string {
		dot := sDim.Render("○")
		if fresh > 0 {
			dot = sOK.Render("●")
		}
		name := key
		if m.cfg.IsLocal(key) {
			name += " (you)"
		}
		return sGroup.Render(pad("  "+dot+" ▣ "+name, 28)) +
			sDim.Render(fmt.Sprintf("%d session%s", n, plural(n)))
	}
	colorHead := func(key string, n, _ int) string {
		d := colors.For("", key)
		dot := "○"
		if d.Colored() {
			dot = lipgloss.NewStyle().Foreground(lipgloss.Color(strconv.Itoa(d.ANSI))).Render("●")
		}
		head := sGroup.Render(pad("  "+dot+" "+m.colorLabel(key), 28)) +
			sDim.Render(fmt.Sprintf("%d session%s", n, plural(n)))
		if m.cur >= 0 && m.cur < len(m.rows) && m.rows[m.cur].groupKey() == key {
			head += sDim.Render("   ·  " + m.groupHint())
		}
		return head
	}
	switch {
	case m.grouped:
		groupOf = func(r row) string { return r.groupKey() }
		groupHead = colorHead
	case m.sort == 1 && !single:
		groupOf = func(r row) string { return r.Host }
		groupHead = hostHead
	case m.sort == 0:
		groupOf = func(r row) string { return r.groupKey() }
		groupHead = colorHead
	}
	groups := map[string][2]int{}
	if groupOf != nil {
		for _, r := range m.rows {
			g := groups[groupOf(r)]
			g[0]++
			if !r.stale {
				g[1]++
			}
			groups[groupOf(r)] = g
		}
		// A single group needs no heading — it would just repeat the sort key
		// above a list that is entirely that one thing. This is what makes the
		// out-of-the-box state (every session uncolored) read as a plain list.
		if len(groups) < 2 {
			groupOf = nil
		}
	}

	// ---- build display lines (rows + group headers), then window ----
	lines := make([]string, 0, len(m.rows)+8)
	selLine := 0
	prevGroup := "\x00"
	for i, r := range m.rows {
		if groupOf != nil {
			gk := groupOf(r)
			if gk != prevGroup {
				g := groups[gk]
				lines = append(lines, groupHead(gk, g[0], g[1]))
				prevGroup = gk
			}
		}
		if i == m.cur {
			selLine = len(lines)
		}
		lines = append(lines, m.renderRow(r, i == m.cur, prevW, single))
	}
	if len(lines) == 0 {
		lines = append(lines, sDim.Render("  no sessions"+filterHint(m.filter)))
	}

	visible := m.height - 7
	if visible < 3 {
		visible = 3
	}
	start := selLine - visible/2
	if start > len(lines)-visible {
		start = len(lines) - visible
	}
	if start < 0 {
		start = 0
	}
	end := start + visible
	if end > len(lines) {
		end = len(lines)
	}
	for _, ln := range lines[start:end] {
		b.WriteString(ln + "\n")
	}

	// ---- footer ----
	footer := m.footer()
	b.WriteString("\n" + footer)
	pos := fmt.Sprintf("  %d–%d/%d", start+1, end, len(lines))
	if lipgloss.Width(footer)+lipgloss.Width(pos) <= m.width {
		b.WriteString(sDim.Render(pos))
	}
	return b.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// tabBar renders the host tabs: one per machine plus an "all" tab, with the
// per-tab session counts. The open tab is boxed; the local machine is marked.
func (m Model) tabBar() string {
	all, per := m.tabCounts()
	sTabOn := lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(lipgloss.Color("153")).Bold(true).Padding(0, 1)
	sTabOff := lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Background(lipgloss.Color("236")).Padding(0, 1)
	sTabMe := lipgloss.NewStyle().Foreground(lipgloss.Color("110")).Background(lipgloss.Color("236")).Padding(0, 1)
	sTabMeOn := lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(lipgloss.Color("157")).Bold(true).Padding(0, 1)

	var b strings.Builder
	b.WriteString(" ")
	add := func(st lipgloss.Style, label string) {
		b.WriteString(st.Render(label))
		b.WriteString(" ")
	}
	if m.tabHost == allTab {
		add(sTabOn, fmt.Sprintf("all %d", all))
	} else {
		add(sTabOff, fmt.Sprintf("all %d", all))
	}
	for _, h := range m.visibleHosts() {
		label := fmt.Sprintf("%s %d", h, per[h])
		me := m.cfg.IsLocal(h)
		if h == m.tabHost {
			if me {
				add(sTabMeOn, label)
			} else {
				add(sTabOn, label)
			}
		} else {
			if me {
				add(sTabMe, label)
			} else {
				add(sTabOff, label)
			}
		}
	}
	if m.showHidden {
		add(sDim, "· hidden shown")
	}
	return b.String()
}

// tabScope reports the session count, attached count and fresh-host count for
// the open tab, ignoring the filter. The stats line describes the tab you are
// looking at; the filter indicator is reported separately, so a narrowed view
// never looks like a smaller fleet.
func (m *Model) tabScope() (sessions, attached, hostsTotal, hostsFresh int) {
	inTab := map[string]bool{}
	for _, h := range m.fleet {
		if m.cfg.HiddenHost(h.Name, h.Hidden) && !m.showHidden {
			continue
		}
		if m.tabHost != allTab && h.Name != m.tabHost {
			continue
		}
		inTab[h.Name] = true
		sessions += len(h.Sessions)
		for _, s := range h.Sessions {
			if s.Attached {
				attached++
			}
		}
	}
	hostsTotal = len(inTab)
	fresh := map[string]bool{}
	for _, r := range m.allRows {
		if inTab[r.Host] && !r.stale {
			fresh[r.Host] = true
		}
	}
	return sessions, attached, hostsTotal, len(fresh)
}

// renderRow renders one session row; when selected every cell gets the
// selection background so the highlight spans the full line width.
func (m Model) renderRow(r row, sel bool, prevW int, single bool) string {
	dimBase := lipgloss.NewStyle()
	if r.stale {
		dimBase = sDim
	}
	cell := func(st lipgloss.Style, s string, w int) string {
		st = dimBase.Inherit(st)
		if sel {
			st = st.Background(selBg)
		}
		return st.Render(pad(trunc(s, w), w))
	}
	gutter := " "
	if sel {
		gst := lipgloss.NewStyle().Foreground(lipgloss.Color(strconv.Itoa(r.def.ANSI))).Background(selBg)
		gutter = gst.Render("▌")
	}
	dotSt := lipgloss.NewStyle().Foreground(lipgloss.Color(strconv.Itoa(r.def.ANSI)))
	if sel {
		dotSt = dotSt.Background(selBg)
	}
	dot := dotSt.Render("●")
	name := cell(lipgloss.NewStyle(), r.Name, colName)
	host := ""
	if !single {
		host = cell(lipgloss.NewStyle(), r.Host, colHost)
	}
	att := cell(lipgloss.NewStyle(), "·", colAtt)
	if r.Attached {
		att = cell(sAttached, "✓", colAtt)
	}
	act := cell(actStyle(time.Since(time.Unix(r.Activity, 0))), rel(time.Since(time.Unix(r.Activity, 0))), colAct)
	tag := cell(lipgloss.NewStyle(), r.Tag, colTag)
	prev := cell(sDim, strings.ReplaceAll(r.Preview, "\n", " "), prevW)
	return gutter + dot + name + host + att + act + tag + prev
}

func (m Model) footer() string {
	switch {
	case m.err != "":
		return sErr.Render(" ✗ " + trunc(m.err, m.width-6))
	case m.status != "":
		return sOK.Render(" ✓ " + m.status)
	case m.inputMode == "filter":
		return sChipKey.Render(" filter ") + " " + m.input.View()
	case m.inputMode != "":
		return sChipKey.Render(" "+m.inputMode+" ") + " " + m.input.View()
	}
	caps := [][2]string{
		{"enter", "attach"}, {"p", "preview"}, {"⇥", "host"}, {"V", "view"},
		{"c", "color"}, {"n", "new"}, {"o", "open group"}, {"t", "tag"},
		{"r", "rename"}, {"K", "kill"}, {"/", "filter"},
		{"1-5", "sort"}, {"S", "rev"}, {"P", "colors"}, {"?", "help"}, {"q", "quit"},
	}
	var out strings.Builder
	out.WriteString(" ")
	for _, c := range caps {
		seg := keycap(c[0], c[1])
		if lipgloss.Width(out.String())+lipgloss.Width(seg) > m.width-1 {
			break
		}
		out.WriteString(seg)
	}
	return out.String()
}

func filterHint(f string) string {
	if f != "" {
		return " (filter: " + f + " — esc clears)"
	}
	return " — press n to create one"
}

func (m Model) viewPreview() string {
	title := sBar.Render("📄 "+m.vpRow.Name) +
		sDim.Render("  @ "+m.vpRow.Host+" · last activity "+rel(time.Since(time.Unix(m.vpRow.Activity, 0)))+" ago") +
		sDim.Render("  ·  esc close · j/k scroll")
	body := title + "\n\n" + m.vp.View() + "\n" + sDim.Render("scroll: j/k/up/down/pgup/pgdn")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(strconv.Itoa(m.vpRow.def.ANSI))).
		Padding(0, 1).
		Render(body)
}

func (m Model) viewHelp() string {
	head := func(s string) string { return sHead.Render(s) }
	lines := []string{
		sTitle.Render("🧭 dtc") + sDim.Render(" distributed tmux controller — help"),
		"",
		head("navigation"),
		"  j/k or ↑/↓     move           g/G  top/bottom    pgup/pgdn  page",
		"  enter          attach (local, or ssh to the owning host)",
		"",
		head("view"),
		"  ⇥ / ⇧⇥         next / previous host tab (0 = all hosts)",
		"  6…9            jump straight to a host tab (6 = first host)",
		"  p              live pane preview (last 3000 lines)",
		"  /              filter by name/host/tag (esc clears)",
		"  1…5            sort: 1 color · 2 host · 3 activity · 4 created · 5 name",
		"  s              cycle sort        S  reverse        R  force refresh",
		"  H              show/hide client machines (hidden hosts)",
		"",
		head("two views — V switches"),
		"  colors view    sessions under a heading per color, manual order only.",
		"                 sorting keys do nothing here; arrange by hand.",
		"  flat list      one list you can sort (1…5). The color is only the dot",
		"                 on the left, not a sub-heading.",
		"",
		head("working with color groups"),
		"  c              set the selected session's color (also its tab emoji)",
		"  n              new session — it starts in the color you are standing on",
		"  ⇧↑ / ⇧↓        move the session one place inside its group",
		"  o              open every session in this group as tabs in this tmux",
		"                 session (dtc's own window plus one per session)",
		"  P              manage colors: ⇧↑/⇧↓ reorder the groups, r renames one",
		"",
		head("actions on the selected session"),
		"  t              set tag (empty clears)      r  rename",
		"  K              kill session (confirm with y)",
		"",
		head("the dot colors"),
		"  Nine pastels: 🔴red 🟠orange 🟡yellow 🟢green 🔷cyan 🔵blue 🟣purple",
		"  🌸pink ⚪gray. Nothing is colored until you say so — a new session is",
		"  plain, and c gives it meaning. Colors and their group order are fleet",
		"  state on the hub, so every machine groups them the same way, and a",
		"  rename never rewrites a session. The dot also drives the Ghostty tab",
		"  emoji on the owning host.",
		"",
		head("the color column values"),
		sDim.Render("  ACT is colored by freshness: green < 5 min · amber < 1 h ·"),
		sDim.Render("  gray < 1 day · dim older. It colors the age, not the session."),
		"",
		head("misc"),
		"  ?              this help       q  quit",
		"",
		sDim.Render("colors/tags/title are stored as tmux user options (@dtc-*) on the owning"),
		sDim.Render("host and synced to the hub by the agent, so every machine sees them."),
	}
	body := lipgloss.NewStyle().Padding(1, 2).Border(lipgloss.RoundedBorder()).BorderForeground(accent).Render(strings.Join(lines, "\n"))
	return body + sDim.Render("\n  esc back")
}

func (m Model) pickerOverlay() string {
	if m.palPick {
		var lines []string
		keys := m.palKeys()
		for i, k := range keys {
			cur := "  "
			if i == m.palPickCur {
				cur = "▌ "
			}
			d, _ := colors.ByName(k)
			dot := lipgloss.NewStyle().Foreground(lipgloss.Color(strconv.Itoa(d.ANSI))).Render("●")
			label := m.colorLabel(k)
			extra := ""
			if label != k {
				extra = sDim.Render("  (" + k + ")")
			}
			lines = append(lines, cur+dot+" "+pad(label, 20)+extra)
		}
		lines = append(lines, "", sDim.Render("  ⇧↑/⇧↓ move · r rename · esc done"),
			sDim.Render("  uncolored sessions always sit below the colors"))
		return overlay("colors — order and names", strings.Join(lines, "\n"))
	}
	if m.colorPick {
		var lines []string
		for i, k := range m.pickerColorKeys() {
			cur := "  "
			if i == m.colorPickCur {
				cur = "▌ "
			}
			if k == "" {
				lines = append(lines, cur+"○ no color")
				continue
			}
			d, _ := colors.ByName(k)
			dot := lipgloss.NewStyle().Foreground(lipgloss.Color(strconv.Itoa(d.ANSI))).Render("●")
			lines = append(lines, cur+dot+" "+m.colorLabel(k))
		}
		return overlay("set color — "+m.rows[m.cur].Name+" (P manages the palette)", strings.Join(lines, "\n"))
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
