package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ilmal/distrobuted-tmux-controller/internal/colors"
	"github.com/ilmal/distrobuted-tmux-controller/internal/config"
	"github.com/ilmal/distrobuted-tmux-controller/internal/model"
)

// fakeTmux puts a stand-in tmux first on PATH that records the argv of every
// call and answers each new-window with a fresh window id, so openGroupCmd can
// be exercised without a tmux server. It returns the recording's path.
//
// Everything except the window ids is canned: `display-message` reports the
// session dtc is "running in", so the group opens into a name the test knows.
func fakeTmux(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	count := filepath.Join(dir, "count")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = display-message ]; then echo uitest; exit 0; fi\n" +
		"printf '%s\\n' \"$@\" >> " + log + "\n" +
		"n=$(cat " + count + " 2>/dev/null || echo 0); n=$((n + 1)); echo $n > " + count + "\n" +
		"printf '@%s\\n' \"$n\"\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func recorded(t *testing.T, log string) string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// testModel builds a Model with a canned fleet: a local machine (cn1), another
// server (main), and a client machine (laptop) that declares itself hidden.
func testModel() *Model {
	cfg := &config.Config{
		Hostname: "cn1",
		Hosts: map[string]config.HostCfg{
			"cn1":    {},
			"main":   {SSH: "main"},
			"laptop": {SSH: "laptop"},
		},
	}
	m := &Model{cfg: cfg, confirmRow: -1, grouped: true, palette: colors.DefaultPalette()}
	now := time.Now()
	m.fleet = []model.Host{
		{Name: "cn1", LastSeen: now, Sessions: []model.Session{
			{Name: "alpha", Host: "cn1", Activity: now.Unix()},
			{Name: "beta", Host: "cn1", Activity: now.Unix()},
		}},
		{Name: "main", LastSeen: now, Sessions: []model.Session{
			{Name: "gamma", Host: "main", Activity: now.Unix()},
		}},
		// Declares itself a client machine via its heartbeat.
		{Name: "laptop", LastSeen: now, Hidden: true, Sessions: []model.Session{
			{Name: "ghostty-1", Host: "laptop", Activity: now.Unix()},
			{Name: "ghostty-2", Host: "laptop", Activity: now.Unix()},
		}},
	}
	m.buildRows()
	return m
}

func TestSelfDeclaredHiddenHostExcluded(t *testing.T) {
	m := testModel()
	if m.totalAll != 3 {
		t.Fatalf("expected 3 visible sessions, got %d", m.totalAll)
	}
	if m.hiddenCnt != 2 {
		t.Fatalf("expected 2 hidden sessions counted, got %d", m.hiddenCnt)
	}
	for _, r := range m.allRows {
		if r.Host == "laptop" {
			t.Fatalf("client machine leaked into visible rows: %s", r.Name)
		}
	}
}

func TestRevealShowsClientMachine(t *testing.T) {
	m := testModel()
	m.showHidden = true
	m.buildRows()
	if m.totalAll != 5 {
		t.Fatalf("expected 5 sessions with hidden revealed, got %d", m.totalAll)
	}
}

func TestClientMachineHidesFromItself(t *testing.T) {
	// A client machine's own sessions are scratch terminal windows; hiding the
	// host means hiding them on the client too, not only on the servers.
	cfg := &config.Config{
		Hostname: "laptop",
		Hosts:    map[string]config.HostCfg{"laptop": {Hidden: true}},
	}
	m := &Model{cfg: cfg, confirmRow: -1}
	m.fleet = []model.Host{{Name: "laptop", LastSeen: time.Now(), Hidden: true,
		Sessions: []model.Session{{Name: "ghostty-1", Host: "laptop"}}}}
	m.buildRows()
	if m.totalAll != 0 {
		t.Fatalf("client machine still listed its own sessions: visible=%d", m.totalAll)
	}
	if m.hiddenCnt != 1 {
		t.Fatalf("expected the hidden count to include itself, got %d", m.hiddenCnt)
	}
	// ...but it is always one keypress away.
	m.showHidden = true
	m.buildRows()
	if m.totalAll != 1 {
		t.Fatalf("reveal should bring the client's own sessions back, got %d", m.totalAll)
	}
}

func TestTabScopesRows(t *testing.T) {
	m := testModel()
	if len(m.visibleHosts()) != 2 {
		t.Fatalf("expected 2 host tabs (client hidden), got %d", len(m.visibleHosts()))
	}
	// Tabs are ordered local-first.
	if got := m.visibleHosts()[0]; got != "cn1" {
		t.Fatalf("expected local host first, got %q", got)
	}
	m.setTab("main")
	if len(m.rows) != 1 || m.rows[0].Host != "main" {
		t.Fatalf("main tab should hold only main sessions, got %d rows", len(m.rows))
	}
	m.setTab(allTab)
	if len(m.rows) != 3 {
		t.Fatalf("all tab should hold every visible session, got %d", len(m.rows))
	}
}

func TestTabSurvivesFilter(t *testing.T) {
	m := testModel()
	m.setTab("cn1")
	m.filter = "alpha"
	m.applyFilterSort()
	if len(m.rows) != 1 || m.rows[0].Name != "alpha" {
		t.Fatalf("filter within a tab lost its scope: %+v", m.rows)
	}
	m.filter = ""
	m.applyFilterSort()
	if len(m.rows) != 2 {
		t.Fatalf("clearing the filter should restore the tab's rows, got %d", len(m.rows))
	}
}

func TestCycleTabWraps(t *testing.T) {
	m := testModel()
	// all -> cn1 -> main -> all
	m.cycleTab(1)
	if m.tabHost != "cn1" {
		t.Fatalf("expected cn1, got %q", m.tabHost)
	}
	m.cycleTab(1)
	if m.tabHost != "main" {
		t.Fatalf("expected main, got %q", m.tabHost)
	}
	m.cycleTab(1)
	if m.tabHost != allTab {
		t.Fatalf("expected wrap to all, got %q", m.tabHost)
	}
	m.cycleTab(-1)
	if m.tabHost != "main" {
		t.Fatalf("expected backward wrap to main, got %q", m.tabHost)
	}
}

func TestTabFallsBackWhenHostVanishes(t *testing.T) {
	m := testModel()
	m.setTab("main")
	// main drops out of the fleet entirely.
	m.fleet = m.fleet[:1]
	m.buildRows()
	if m.tabHost != allTab {
		t.Fatalf("stale tab should fall back to all, got %q", m.tabHost)
	}
}

func TestTabCountsIgnoreFilter(t *testing.T) {
	m := testModel()
	m.filter = "alpha"
	m.applyFilterSort()
	all, per := m.tabCounts()
	if all != 3 || per["cn1"] != 2 || per["main"] != 1 {
		t.Fatalf("tab counts should be filter-independent, got all=%d per=%v", all, per)
	}
}

func TestPaletteIsPastelAndComplete(t *testing.T) {
	if len(colors.Palette) != 9 {
		t.Fatalf("expected 9 palette colors, got %d", len(colors.Palette))
	}
	seen := map[string]bool{}
	for _, d := range colors.Palette {
		if seen[d.Name] {
			t.Fatalf("duplicate palette color %q", d.Name)
		}
		seen[d.Name] = true
		if d.ANSI < 0 || d.ANSI > 255 {
			t.Fatalf("color %q has out-of-range ANSI %d", d.Name, d.ANSI)
		}
		if len(d.Hex) != 7 || d.Hex[0] != '#' {
			t.Fatalf("color %q has malformed hex %q", d.Name, d.Hex)
		}
		if d.Emoji == "" {
			t.Fatalf("color %q has no emoji", d.Name)
		}
	}
}

// A session with no color is the default, and an unknown key is not invented
// into a real color: the fleet starts as one plain list.
func TestUncoloredByDefault(t *testing.T) {
	for _, in := range []string{"", "bogus", "red-ish"} {
		d := colors.For("lawcrawl", in)
		if d.Colored() {
			t.Fatalf("color %q resolved to %q, want uncolored", in, d.Name)
		}
		if got := d.Title("lawcrawl"); got != "lawcrawl" {
			t.Fatalf("uncolored title should be the bare name, got %q", got)
		}
	}
	if got := colors.For("x", "green").Title("x"); got != "🟢 x" {
		t.Fatalf("colored title wrong: %q", got)
	}
}

// The colors view groups by color in the palette's display order, uncolored
// first, and inside a group the manual order wins over the name.
func TestGroupedOrderUsesManualThenName(t *testing.T) {
	m := testModel()
	m.palette = colors.DefaultPalette()
	m.order = []model.GroupOrder{{Group: "green", Items: []model.OrderedItem{
		{Host: "cn1", Name: "zeta"},
	}}}
	m.fleet = []model.Host{{Name: "cn1", LastSeen: time.Now(), Sessions: []model.Session{
		{Name: "plain", Host: "cn1", Color: ""},
		{Name: "beta", Host: "cn1", Color: "green"},
		{Name: "alpha", Host: "cn1", Color: "green"},
		{Name: "zeta", Host: "cn1", Color: "green"},
		{Name: "red1", Host: "cn1", Color: "red"},
	}}}
	m.buildRows()
	var got []string
	for _, r := range m.rows {
		got = append(got, r.Name)
	}
	// colors in palette order first (red before green), then the uncolored
	// group last; inside green the hand-placed zeta leads, the rest follow
	// alphabetically.
	want := []string{"red1", "zeta", "alpha", "beta", "plain"}
	if len(got) != len(want) {
		t.Fatalf("row count %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("grouped order = %v, want %v", got, want)
		}
	}
}

// The flat list sorts by activity and leaves the color as a leading dot rather
// than a group heading.
func TestFlatListSortsAndDropsColorHeadings(t *testing.T) {
	m := testModel()
	m.grouped = false
	now := time.Now()
	m.fleet = []model.Host{{Name: "cn1", LastSeen: now, Sessions: []model.Session{
		{Name: "old", Host: "cn1", Color: "green", Activity: now.Add(-time.Hour).Unix()},
		{Name: "new", Host: "cn1", Color: "red", Activity: now.Unix()},
	}}}
	m.buildRows()
	m.sort = 2 // activity, newest first
	m.applyFilterSort()
	if m.rows[0].Name != "new" {
		t.Fatalf("flat sort put %q first, want the most recent", m.rows[0].Name)
	}
	m.width, m.height = 140, 40
	view := m.viewList()
	if containsStr(view, "1 session") {
		t.Fatal("flat list rendered a color group heading")
	}
}

// Switching back to the colors view re-sorts by the palette, so the same model
// is never left in a half-sorted state.
func TestViewToggleKeepsBothOrders(t *testing.T) {
	m := testModel()
	m.fleet = []model.Host{{Name: "cn1", LastSeen: time.Now(), Sessions: []model.Session{
		{Name: "zulu", Host: "cn1", Color: ""},
		{Name: "alpha", Host: "cn1", Color: "blue"},
	}}}
	m.grouped = false
	m.buildRows()
	m.sort = 4 // by name: alpha, zulu
	if m.rows[0].Name != "alpha" {
		t.Fatalf("name sort wrong: %q first", m.rows[0].Name)
	}
	m.grouped = true
	m.applyFilterSort()
	if m.rows[0].Name != "alpha" {
		t.Fatalf("colors view should lead with the colored group, got %q", m.rows[0].Name)
	}
	if m.rows[1].Name != "zulu" {
		t.Fatalf("uncolored group should come last, got %q second", m.rows[1].Name)
	}
}

// `n` on a colored row starts the new session in that color.
func TestNewInheritsCursorColor(t *testing.T) {
	m := testModel()
	if len(m.rows) == 0 {
		t.Fatal("no rows to stand on")
	}
	m.rows[0].def = colors.For("x", "purple")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	got, ok := nm.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", nm)
	}
	if !got.hostPick {
		t.Fatal("n did not open the host picker")
	}
	if got.inputColor != "purple" {
		t.Fatalf("new session color = %q, want purple", got.inputColor)
	}
}

// The palette manager indexes colors only — uncolored is not renamable.
func TestPaletteManagerExcludesUncolored(t *testing.T) {
	m := testModel()
	for _, k := range m.palKeys() {
		if k == "" {
			t.Fatal("palette manager listed the uncolored group")
		}
	}
	if len(m.palKeys()) != len(colors.Palette) {
		t.Fatalf("palette manager lists %d entries, want %d", len(m.palKeys()), len(colors.Palette))
	}
}

// A rename changes the label only; the key every session stores is untouched.
func TestColorLabelRenameKeepsKey(t *testing.T) {
	m := testModel()
	m.palette = colors.DefaultPalette()
	for i, e := range m.palette {
		if e.Name == "red" {
			m.palette[i].Label = "urgent"
		}
	}
	if got := m.colorLabel("red"); got != "urgent" {
		t.Fatalf("label = %q, want urgent", got)
	}
	if _, ok := colors.ByName("red"); !ok {
		t.Fatal("renaming changed the color key")
	}
	if got := m.colorLabel(""); got != "no color" {
		t.Fatalf("uncolored label = %q", got)
	}
}

func TestSingleHostTabDropsHostColumn(t *testing.T) {
	m := testModel()
	m.width, m.height = 140, 40
	m.setTab("cn1")
	view := m.viewList()
	if !containsStr(view, "SESSION") {
		t.Fatal("view missing the session header")
	}
	// The header line on a single-host tab must not carry HOST.
	for _, line := range splitLines(view) {
		if containsStr(line, "SESSION") && containsStr(line, "HOST") {
			t.Fatal("single-host tab still renders the HOST column")
		}
	}
}

func TestNoWindowColumn(t *testing.T) {
	m := testModel()
	m.width, m.height = 140, 40
	for _, tab := range []string{allTab, "cn1"} {
		m.setTab(tab)
		for _, line := range splitLines(m.viewList()) {
			// The header and the rows must not carry a window count. "W" as a
			// standalone header cell is what we removed; the substring check
			// is scoped to the header line to avoid matching session names.
			if containsStr(line, "ATT") && containsStr(line, "W ") {
				t.Fatalf("window column still in the header: %q", line)
			}
		}
	}
}

func TestTabBarShowsCounts(t *testing.T) {
	m := testModel()
	bar := m.tabBar()
	for _, want := range []string{"all 3", "cn1 2", "main 1"} {
		if !containsStr(bar, want) {
			t.Fatalf("tab bar missing %q in %q", want, bar)
		}
	}
}

func containsStr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	out = append(out, cur)
	return out
}

// openGroupCmd is the "open a whole color" key: every session in the group
// becomes a window in the tmux session dtc is running in. This pins the two
// things that silently break it — the TMUX clearing that keeps the pane's
// nested attach alive, and the anchor chain that keeps the tabs in listed order.
func TestOpenGroupOpensEachMemberInOrder(t *testing.T) {
	log := fakeTmux(t)
	t.Setenv("TMUX", "/tmp/probe.sock,4242,0")

	m := testModel()
	m.fleet = []model.Host{{Name: "cn1", LastSeen: time.Now(), Sessions: []model.Session{
		{Name: "alpha", Host: "cn1", Color: "green"},
		{Name: "beta", Host: "cn1", Color: "green"},
		{Name: "solo", Host: "cn1", Color: "red"},
	}}}
	m.buildRows()

	cmd := m.openGroupCmd("green")
	if cmd == nil {
		t.Fatal("no command for a non-empty group")
	}
	msg, ok := cmd().(actionMsg)
	if !ok {
		t.Fatalf("expected an actionMsg, got %T", msg)
	}
	if msg.err != nil {
		t.Fatalf("unexpected error: %v", msg.err)
	}
	if !strings.Contains(msg.note, "2") {
		t.Errorf("note should report both sessions: %q", msg.note)
	}

	got := recorded(t, log)
	// Both windows are created on the socket dtc is running on, and both clear
	// TMUX for their pane — without either, the window dies as it opens.
	if n := strings.Count(got, "-S\n/tmp/probe.sock\n"); n != 2 {
		t.Errorf("expected 2 socket-scoped windows, saw %d:\n%s", n, got)
	}
	if n := strings.Count(got, "env -u TMUX tmux new-session -A -s "); n != 2 {
		t.Errorf("expected 2 TMUX-clearing attaches, saw %d:\n%s", n, got)
	}
	// The first anchors to the session, the second to the first window, so the
	// tabs come out alpha then beta rather than reversed.
	if !strings.Contains(got, "-a\n-t\nuitest\n") {
		t.Errorf("first window should anchor to the session:\n%s", got)
	}
	if !strings.Contains(got, "-a\n-t\n@1\n") {
		t.Errorf("second window should anchor to the first window:\n%s", got)
	}
	// The session outside the group is left alone.
	if strings.Contains(got, "solo") {
		t.Errorf("session outside the group was opened:\n%s", got)
	}
}

// A group on another machine still opens one window per session, as an ssh
// command: that session has no local tmux to attach to.
func TestOpenGroupShellsOutForRemoteHosts(t *testing.T) {
	log := fakeTmux(t)
	t.Setenv("TMUX", "/tmp/probe.sock,4242,0")

	m := testModel()
	m.fleet = []model.Host{{Name: "main", LastSeen: time.Now(), Sessions: []model.Session{
		{Name: "gamma", Host: "main", Color: "green"},
	}}}
	m.buildRows()

	msg := m.openGroupCmd("green")().(actionMsg)
	if msg.err != nil {
		t.Fatalf("unexpected error: %v", msg.err)
	}
	if !strings.Contains(msg.note, "gamma") && !strings.Contains(msg.note, "1") {
		t.Errorf("note should report the opened session: %q", msg.note)
	}
	got := recorded(t, log)
	// The remote attach goes over ssh, with the remote command handed to the
	// remote shell as one quoted argument.
	if !strings.Contains(got, "ssh -t -o ConnectTimeout=8 main 'tmux new-session -A -s gamma'") {
		t.Fatalf("remote member not opened over ssh:\n%s", got)
	}
	// A foreign command carries no Env, so nothing may clear TMUX for it.
	if strings.Contains(got, "env -u TMUX") {
		t.Errorf("ssh window should not be wrapped:\n%s", got)
	}
}

// Outside tmux there is no window to open into; say so rather than do nothing.
func TestOpenGroupOutsideTmuxExplainsItself(t *testing.T) {
	fakeTmux(t)
	t.Setenv("TMUX", "")

	m := testModel()
	msg := m.openGroupCmd("")().(actionMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "tmux") {
		t.Fatalf("want a message about needing tmux, got %v", msg.err)
	}
}

// The tabs open in the order the group is listed — including a manual order.
// Opening a color should read down the list you are looking at, not the raw
// order the hub happened to return.
func TestOpenGroupFollowsTheManualGroupOrder(t *testing.T) {
	log := fakeTmux(t)
	t.Setenv("TMUX", "/tmp/probe.sock,4242,0")

	m := testModel()
	m.fleet = []model.Host{{Name: "cn1", LastSeen: time.Now(), Sessions: []model.Session{
		{Name: "alpha", Host: "cn1", Color: "green"},
		{Name: "beta", Host: "cn1", Color: "green"},
	}}}
	// beta was dragged above alpha.
	m.order = []model.GroupOrder{{Group: "green", Items: []model.OrderedItem{
		{Host: "cn1", Name: "beta"},
		{Host: "cn1", Name: "alpha"},
	}}}
	m.buildRows()

	// The view shows beta first...
	if m.rows[0].Name != "beta" {
		t.Fatalf("view order not manual: %s first", m.rows[0].Name)
	}
	if msg := m.openGroupCmd("green")().(actionMsg); msg.err != nil {
		t.Fatal(msg.err)
	}
	// ...and so do the windows.
	got := recorded(t, log)
	betaAt := strings.Index(got, "-s beta")
	alphaAt := strings.Index(got, "-s alpha")
	if betaAt < 0 || alphaAt < 0 {
		t.Fatalf("both members should open:\n%s", got)
	}
	if betaAt > alphaAt {
		t.Errorf("windows opened in hub order, not the listed order:\n%s", got)
	}
}
