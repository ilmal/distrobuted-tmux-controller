// Package hub is the central registry: agents push heartbeats, dashboards
// pull fleet state. Auth is a single bearer token; the network edge is the
// Tailscale interface, so the token mainly keeps casual LAN eyes out.
package hub

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"html/template"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/ilmal/distrobuted-tmux-controller/internal/colors"
	"github.com/ilmal/distrobuted-tmux-controller/internal/model"
)

//go:embed page.html
var pageFS embed.FS

type Server struct {
	Token string
	Store *Store
}

func (s *Server) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /{$}", s.page)
	mux.Handle("POST /api/v1/heartbeat", s.auth(http.HandlerFunc(s.heartbeat)))
	mux.Handle("GET /api/v1/sessions", s.auth(http.HandlerFunc(s.sessions)))
	mux.Handle("PATCH /api/v1/meta", s.auth(http.HandlerFunc(s.meta)))
	return mux
}

func (s *Server) ListenAndServe(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Router(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Printf("hub listening on %s", addr)
	return srv.Serve(ln)
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, tok, _ := splitBearer(r.Header.Get("Authorization"))
		if tok == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func splitBearer(v string) (bool, string, bool) {
	const p = "Bearer "
	if len(v) > len(p) && v[:len(p)] == p {
		return true, v[len(p):], true
	}
	return false, "", false
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var hb model.Heartbeat
	if err := json.NewDecoder(r.Body).Decode(&hb); err != nil || hb.Host == "" {
		http.Error(w, "bad heartbeat", http.StatusBadRequest)
		return
	}
	s.Store.Heartbeat(hb)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "now": time.Now()})
}

func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, model.FleetResponse{Now: time.Now(), Host: s.Store.Fleet()})
}

func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	var p model.MetaPatch
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.Host == "" || p.Name == "" {
		http.Error(w, "bad patch", http.StatusBadRequest)
		return
	}
	if err := s.Store.Meta(p); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- status page ----

type pageSession struct {
	model.Session
	Dot  string
	Act  string
	Prev string
}

type pageHost struct {
	model.Host
	Seen     string
	Stale    bool
	Sessions []pageSession
}

type pageData struct {
	Hosts      []pageHost
	Total      int
	Attached   int
	Now        string
	Hidden     int  // sessions on client machines left out of this view
	HiddenHost int  // the machines themselves
	ShowAll    bool // ?all=1 revealed them
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

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	tpl, err := template.ParseFS(pageFS, "page.html")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	showAll := r.URL.Query().Get("all") != ""
	now := time.Now()
	data := pageData{Now: now.Format("15:04:05"), ShowAll: showAll}
	for _, h := range s.Store.Fleet() {
		// A machine that declares itself a client (heartbeat Hidden) stays out
		// of the fleet view unless explicitly revealed with ?all=1. Its sessions
		// are counted either way so the note stays visible after a reveal, which
		// is the only way back to the hidden view.
		if h.Hidden {
			data.Hidden += len(h.Sessions)
			data.HiddenHost++
			if !showAll {
				continue
			}
		}
		ph := pageHost{Host: h, Seen: rel(now.Sub(h.LastSeen)), Stale: h.Stale()}
		for _, sess := range h.Sessions {
			ps := pageSession{Session: sess,
				Dot:  colors.For(sess.Name, sess.Color).Hex,
				Act:  rel(now.Sub(time.Unix(sess.Activity, 0))),
				Prev: firstLine(sess.Preview),
			}
			ph.Sessions = append(ph.Sessions, ps)
			data.Total++
			if sess.Attached {
				data.Attached++
			}
		}
		data.Hosts = append(data.Hosts, ph)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tpl.Execute(w, data)
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
