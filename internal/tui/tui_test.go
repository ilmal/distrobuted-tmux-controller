package tui

import (
	"testing"
	"time"

	"github.com/ilmal/distrobuted-tmux-controller/internal/colors"
	"github.com/ilmal/distrobuted-tmux-controller/internal/config"
	"github.com/ilmal/distrobuted-tmux-controller/internal/model"
)

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
	m := &Model{cfg: cfg, confirmRow: -1}
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

func TestAutoColorIsDeterministic(t *testing.T) {
	a := colors.Auto("lawcrawl")
	b := colors.Auto("lawcrawl")
	if a.Name != b.Name {
		t.Fatalf("auto color not stable: %q vs %q", a.Name, b.Name)
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
