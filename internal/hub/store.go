package hub

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/ilmal/distrobuted-tmux-controller/internal/model"
)

var ErrNotFound = errors.New("session not found")

// Store is the fleet state: one JSON file, atomic rename writes. Fleet scale
// (a handful of hosts, tens of sessions) makes a real database unnecessary.
type Store struct {
	mu    sync.Mutex
	path  string
	Hosts map[string]*model.Host `json:"hosts"`
	Pins  map[string]time.Time   `json:"pins"`
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path, Hosts: map[string]*model.Host{}, Pins: map[string]time.Time{}}
	b, err := os.ReadFile(path)
	if err == nil {
		if jerr := json.Unmarshal(b, s); jerr == nil {
			if s.Hosts == nil {
				s.Hosts = map[string]*model.Host{}
			}
			if s.Pins == nil {
				s.Pins = map[string]time.Time{}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

func (s *Store) saveLocked() {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

func key(host, name string) string { return host + "\x1f" + name }

// Heartbeat upserts a host and replaces its session list. Dashboard meta
// patches are pinned: while pinned, a stale agent view cannot overwrite them.
func (s *Store) Heartbeat(hb model.Heartbeat) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()

	h, ok := s.Hosts[hb.Host]
	if !ok {
		h = &model.Host{Name: hb.Host}
		s.Hosts[hb.Host] = h
	}
	h.LastSeen, h.TmuxVersion, h.OS, h.Arch = now, hb.TmuxVersion, hb.OS, hb.Arch

	seen := map[string]bool{}
	for _, sess := range hb.Sessions {
		seen[sess.Name] = true
		ns := sess
		ns.Host = hb.Host
		if until, pinned := s.Pins[key(hb.Host, sess.Name)]; pinned && now.Before(until) {
			for _, old := range h.Sessions {
				if old.Name == sess.Name {
					ns.Color, ns.Tag = old.Color, old.Tag
					break
				}
			}
		}
		replaced := false
		for i := range h.Sessions {
			if h.Sessions[i].Name == sess.Name {
				h.Sessions[i] = ns
				replaced = true
				break
			}
		}
		if !replaced {
			h.Sessions = append(h.Sessions, ns)
		}
	}
	kept := h.Sessions[:0]
	for _, sess := range h.Sessions {
		if seen[sess.Name] {
			kept = append(kept, sess)
		}
	}
	h.Sessions = kept

	for k, until := range s.Pins {
		if now.After(until) {
			delete(s.Pins, k)
		}
	}
	s.saveLocked()
}

// Fleet returns hosts sorted by name, sessions sorted within each host.
func (s *Store) Fleet() []model.Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	hosts := make([]model.Host, 0, len(s.Hosts))
	for _, h := range s.Hosts {
		cp := *h
		sessions := append([]model.Session(nil), h.Sessions...)
		sort.Slice(sessions, func(i, j int) bool { return sessions[i].Name < sessions[j].Name })
		cp.Sessions = sessions
		hosts = append(hosts, cp)
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Name < hosts[j].Name })
	return hosts
}

// Meta applies a dashboard-side color/tag patch and pins it.
func (s *Store) Meta(p model.MetaPatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.Hosts[p.Host]
	if !ok {
		return ErrNotFound
	}
	for i := range h.Sessions {
		if h.Sessions[i].Name == p.Name {
			if p.Color != nil {
				h.Sessions[i].Color = *p.Color
			}
			if p.Tag != nil {
				h.Sessions[i].Tag = *p.Tag
			}
			s.Pins[key(p.Host, p.Name)] = time.Now().Add(model.PinDuration)
			s.saveLocked()
			return nil
		}
	}
	return ErrNotFound
}
