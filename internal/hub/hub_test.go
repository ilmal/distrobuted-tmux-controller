package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		hb.Sessions = append(hb.Sessions, model.Session{Name: n, Activity: time.Now().Unix(), Windows: 1})
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
