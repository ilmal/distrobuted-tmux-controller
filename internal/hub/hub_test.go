package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ilmal/distrobuted-tmux-controller/internal/colors"
	"github.com/ilmal/distrobuted-tmux-controller/internal/model"
)

func testServer(t *testing.T) (*Server, *Store) {
	t.Helper()
	st, err := NewStore(filepath.Join(t.TempDir(), "fleet.json"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return &Server{Token: "secret", Store: st}, st
}

func beat(host string, hidden bool, names ...string) model.Heartbeat {
	hb := model.Heartbeat{Host: host, TmuxVersion: "3.4", Hidden: hidden}
	for _, n := range names {
		hb.Sessions = append(hb.Sessions, model.Session{Name: n, Activity: time.Now().Unix()})
	}
	return hb
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestPageHidesClientMachine(t *testing.T) {
	s, st := testServer(t)
	st.Heartbeat(beat("cn1", false, "build", "logs"))
	st.Heartbeat(beat("laptop", true, "ghostty-1", "ghostty-2", "ghostty-3"))

	body := get(t, s.Router(), "/").Body.String()
	for _, leak := range []string{"ghostty-1", "ghostty-2", "ghostty-3"} {
		if strings.Contains(body, leak) {
			t.Fatalf("client session %q leaked onto the hub page", leak)
		}
	}
	if !strings.Contains(body, "build") {
		t.Fatal("server sessions should still render")
	}
	if !strings.Contains(body, "3 sessions on 1 client machine hidden") {
		t.Fatalf("missing the hidden-sessions note; page said:\n%s", body)
	}
}

func TestPageRevealsWithAllParam(t *testing.T) {
	s, st := testServer(t)
	st.Heartbeat(beat("cn1", false, "build"))
	st.Heartbeat(beat("laptop", true, "ghostty-1"))

	body := get(t, s.Router(), "/?all=1").Body.String()
	if !strings.Contains(body, "ghostty-1") {
		t.Fatal("?all=1 should reveal client sessions")
	}
	if !strings.Contains(body, "hide again") {
		t.Fatal("the reveal link should offer to hide again")
	}
}

func TestHeartbeatPersistsHiddenFlag(t *testing.T) {
	_, st := testServer(t)
	st.Heartbeat(beat("laptop", true, "ghostty-1"))
	if h := st.Hosts["laptop"]; h == nil || !h.Hidden {
		t.Fatal("store dropped the self-declared hidden flag")
	}
	// A later beat that clears the flag must clear it in the store too.
	st.Heartbeat(beat("laptop", false, "ghostty-1"))
	if st.Hosts["laptop"].Hidden {
		t.Fatal("store kept a cleared hidden flag")
	}
}

func TestHeartbeatRequiresAuth(t *testing.T) {
	s, _ := testServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/heartbeat", strings.NewReader(`{"host":"x"}`))
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated heartbeat got %d, want 401", rec.Code)
	}
}

func TestSessionsEndpoint(t *testing.T) {
	s, st := testServer(t)
	st.Heartbeat(beat("cn1", false, "build"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer secret")
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("sessions got %d, want 200", rec.Code)
	}
	var resp model.FleetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(resp.Host) != 1 || resp.Host[0].Name != "cn1" {
		t.Fatalf("unexpected fleet response: %+v", resp.Host)
	}
}

func str(s string) *string { return &s }

func TestPinnedMetaSurvivesStaleAgent(t *testing.T) {
	_, st := testServer(t)
	st.Heartbeat(beat("cn1", false, "build"))
	if err := st.Meta(model.MetaPatch{Host: "cn1", Name: "build", Color: str("purple"), Tag: str("infra")}); err != nil {
		t.Fatalf("meta: %v", err)
	}
	// The agent's next beat carries no meta (it does not know about the patch).
	st.Heartbeat(beat("cn1", false, "build"))
	got := st.Hosts["cn1"].Sessions[0]
	if got.Color != "purple" || got.Tag != "infra" {
		t.Fatalf("pin let the agent clobber dashboard meta: color=%q tag=%q", got.Color, got.Tag)
	}
}

func TestMetaOnUnknownSession(t *testing.T) {
	_, st := testServer(t)
	if err := st.Meta(model.MetaPatch{Host: "cn1", Name: "nope", Color: str("red")}); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// ---- palette and manual ordering ----

func put(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}

func TestPaletteDefaultsToCanonical(t *testing.T) {
	s, _ := testServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/palette", nil)
	req.Header.Set("Authorization", "Bearer secret")
	s.Router().ServeHTTP(rec, req)
	var p model.Palette
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(p.Colors) != 9 {
		t.Fatalf("default palette has %d colors, want 9", len(p.Colors))
	}
	if p.Colors[0].Name != "red" || p.Colors[0].Label != "red" {
		t.Fatalf("unexpected first entry: %+v", p.Colors[0])
	}
}

func TestPaletteRenameAndReorderRoundTrip(t *testing.T) {
	s, st := testServer(t)
	// Move green to the front and rename it.
	entries := []model.PaletteEntry{{Name: "green", Label: "  wip  "}}
	for _, e := range []string{"red", "orange", "yellow", "cyan", "blue", "purple", "pink", "gray"} {
		entries = append(entries, model.PaletteEntry{Name: e, Label: e})
	}
	body, _ := json.Marshal(model.Palette{Colors: entries})
	rec := put(t, s.Router(), "/api/v1/palette", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("put palette got %d: %s", rec.Code, rec.Body.String())
	}
	got := st.Palette()
	if got[0].Name != "green" || got[0].Label != "wip" {
		t.Fatalf("palette not stored: %+v", got[0])
	}
	// Order is display order, so the reorder survives a restart of the reader.
	if order := orderOf(got); order[0] != "green" {
		t.Fatalf("display order = %v, want green first", order)
	}
}

func orderOf(p []model.PaletteEntry) []string {
	out := make([]string, 0, len(p))
	for _, e := range p {
		out = append(out, e.Name)
	}
	return out
}

// reversePalette is the canonical palette back to front, a valid payload that
// is obviously not the default.
func reversePalette() []model.PaletteEntry {
	def := colors.DefaultPalette()
	out := make([]model.PaletteEntry, 0, len(def))
	for i := len(def) - 1; i >= 0; i-- {
		out = append(out, def[i])
	}
	return out
}

func TestPaletteRejectsIncompletePayload(t *testing.T) {
	s, st := testServer(t)
	rec := put(t, s.Router(), "/api/v1/palette", `{"colors":[{"name":"red","label":"red"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a partial palette got %d, want 400", rec.Code)
	}
	if len(st.Palette()) != 9 {
		t.Fatal("a rejected palette must not change the stored one")
	}
}

func TestPaletteRejectsUnknownColor(t *testing.T) {
	s, _ := testServer(t)
	var entries []model.PaletteEntry
	for _, n := range []string{"red", "orange", "yellow", "green", "cyan", "blue", "purple", "pink", "chartreuse"} {
		entries = append(entries, model.PaletteEntry{Name: n, Label: n})
	}
	body, _ := json.Marshal(model.Palette{Colors: entries})
	rec := put(t, s.Router(), "/api/v1/palette", string(body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown color key got %d, want 400", rec.Code)
	}
}

func TestGroupOrderRoundTrip(t *testing.T) {
	s, st := testServer(t)
	st.Heartbeat(beat("cn1", false, "alpha", "beta", "gamma"))
	for _, n := range []string{"alpha", "beta", "gamma"} {
		if err := st.Meta(model.MetaPatch{Host: "cn1", Name: n, Color: str("green")}); err != nil {
			t.Fatalf("meta %s: %v", n, err)
		}
	}
	body, _ := json.Marshal(model.GroupOrder{Group: "green", Items: []model.OrderedItem{
		{Host: "cn1", Name: "gamma"},
		{Host: "cn1", Name: "alpha"},
	}})
	if rec := put(t, s.Router(), "/api/v1/order", string(body)); rec.Code != http.StatusOK {
		t.Fatalf("put order got %d: %s", rec.Code, rec.Body.String())
	}
	// Fleet() resolves each session's manual position.
	hb := st.Fleet()[0]
	pos := map[string]int{}
	for _, sess := range hb.Sessions {
		pos[sess.Name] = sess.Ord
	}
	if pos["gamma"] != 1 || pos["alpha"] != 2 {
		t.Fatalf("manual positions = %v, want gamma=1 alpha=2", pos)
	}
	if pos["beta"] != 0 {
		t.Fatalf("unplaced session should have Ord 0, got %d", pos["beta"])
	}
}

func TestGroupOrderDropsUnknownAndMisplacedSessions(t *testing.T) {
	s, st := testServer(t)
	st.Heartbeat(beat("cn1", false, "alpha", "mover"))
	if err := st.Meta(model.MetaPatch{Host: "cn1", Name: "alpha", Color: str("green")}); err != nil {
		t.Fatalf("meta: %v", err)
	}
	// "mover" is uncolored, so it does not belong in the green group.
	body, _ := json.Marshal(model.GroupOrder{Group: "green", Items: []model.OrderedItem{
		{Host: "cn1", Name: "alpha"},
		{Host: "cn1", Name: "mover"},
		{Host: "cn1", Name: "ghost"},
		{Host: "", Name: "nameless"},
	}})
	put(t, s.Router(), "/api/v1/order", string(body))
	items := st.GroupOrder("green")
	if len(items) != 1 || items[0].Name != "alpha" {
		t.Fatalf("stale or misplaced order entries were kept: %+v", items)
	}
}

// A session that leaves its group must not keep a position in the old one.
func TestColorChangeDropsOldGroupPosition(t *testing.T) {
	_, st := testServer(t)
	st.Heartbeat(beat("cn1", false, "alpha", "beta"))
	st.Meta(model.MetaPatch{Host: "cn1", Name: "alpha", Color: str("green")})
	st.SetGroupOrder("green", []model.OrderedItem{{Host: "cn1", Name: "alpha"}})
	if len(st.GroupOrder("green")) != 1 {
		t.Fatal("order was not stored")
	}
	if err := st.Meta(model.MetaPatch{Host: "cn1", Name: "alpha", Color: str("blue")}); err != nil {
		t.Fatalf("meta: %v", err)
	}
	if got := st.GroupOrder("green"); len(got) != 0 {
		t.Fatalf("recolored session kept its old group slot: %+v", got)
	}
}

// A heartbeat that no longer reports a session prunes its manual position.
func TestHeartbeatPrunesOrderForDeadSession(t *testing.T) {
	_, st := testServer(t)
	st.Heartbeat(beat("main", false, "gamma"))
	st.Meta(model.MetaPatch{Host: "main", Name: "gamma", Color: str("green")})
	st.SetGroupOrder("green", []model.OrderedItem{{Host: "main", Name: "gamma"}})

	st.Heartbeat(beat("cn1", false, "alpha", "beta"))
	for _, n := range []string{"alpha", "beta"} {
		st.Meta(model.MetaPatch{Host: "cn1", Name: n, Color: str("green")})
	}
	st.SetGroupOrder("green", []model.OrderedItem{
		{Host: "cn1", Name: "alpha"},
		{Host: "cn1", Name: "beta"},
		{Host: "main", Name: "gamma"},
	})

	// cn1 drops beta: its slot goes, cn1's other slot and main's stay.
	st.Heartbeat(beat("cn1", false, "alpha"))
	items := st.GroupOrder("green")
	if len(items) != 2 || items[0].Name != "alpha" || items[1].Name != "gamma" {
		t.Fatalf("pruning removed the wrong slots: %+v", items)
	}
}

// The sessions endpoint carries the palette and the order, so a dashboard needs
// only one fetch to render the fleet exactly as every other dashboard does.
func TestSessionsCarriesPaletteAndOrder(t *testing.T) {
	s, st := testServer(t)
	st.Heartbeat(beat("cn1", false, "alpha"))
	st.Meta(model.MetaPatch{Host: "cn1", Name: "alpha", Color: str("green")})
	st.SetGroupOrder("green", []model.OrderedItem{{Host: "cn1", Name: "alpha"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer secret")
	s.Router().ServeHTTP(rec, req)
	var resp model.FleetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(resp.Colors) != 9 {
		t.Fatalf("fleet response carried %d colors, want 9", len(resp.Colors))
	}
	if len(resp.Order) != 1 || resp.Order[0].Group != "green" {
		t.Fatalf("fleet response lost the group order: %+v", resp.Order)
	}
}

func TestOrdersAreInPaletteDisplayOrder(t *testing.T) {
	_, st := testServer(t)
	st.Heartbeat(beat("cn1", false, "a", "b", "c"))
	st.Meta(model.MetaPatch{Host: "cn1", Name: "a", Color: str("blue")})
	st.Meta(model.MetaPatch{Host: "cn1", Name: "b", Color: str("red")})
	st.SetGroupOrder("blue", []model.OrderedItem{{Host: "cn1", Name: "a"}})
	st.SetGroupOrder("red", []model.OrderedItem{{Host: "cn1", Name: "b"}})
	st.SetGroupOrder("", []model.OrderedItem{{Host: "cn1", Name: "c"}})
	got := st.Orders()
	want := []string{"red", "blue", ""} // palette order, uncolored last
	if len(got) != len(want) {
		t.Fatalf("got %d groups, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Group != want[i] {
			t.Fatalf("group %d = %q, want %q", i, got[i].Group, want[i])
		}
	}
}

func TestPaletteSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.json")
	st, err := NewStore(path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := st.SetPalette(reversePalette()); err != nil {
		t.Fatalf("set palette: %v", err)
	}
	again, err := NewStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if again.Palette()[0].Name != "gray" {
		t.Fatalf("palette order lost across reload: %+v", again.Palette()[0])
	}
}
