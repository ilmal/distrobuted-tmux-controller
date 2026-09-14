package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/ilmal/distrobuted-tmux-controller/internal/colors"
	"github.com/ilmal/distrobuted-tmux-controller/internal/model"
)

var ErrNotFound = errors.New("session not found")

// maxOrderItems bounds one group's manual ordering. Sessions are pruned as they
// disappear, but a hostile or buggy client should not be able to grow the file
// without limit.
const maxOrderItems = 2000

// Store is the fleet state: one JSON file, atomic rename writes. Fleet scale
// (a handful of hosts, tens of sessions) makes a real database unnecessary.
type Store struct {
	mu    sync.Mutex
	path  string
	Hosts map[string]*model.Host `json:"hosts"`
	Pins  map[string]time.Time   `json:"pins"`

	// Palette is the fleet's colors in display order; empty means the built-in
	// one. Kept fleet-wide because every dashboard should group colors the same
	// way — a per-machine order would make the same fleet look different
	// depending on where you read it.
	Colors []model.PaletteEntry `json:"palette,omitempty"`
	// Order holds each color group's manual session ordering, keyed by color key
	// ("" = the uncolored group). It cannot live in a tmux option because a
	// group spans hosts: one is on cn1, the next on the laptop.
	Order map[string][]model.OrderedItem `json:"order,omitempty"`
}

func NewStore(path string) (*Store, error) {
	s := &Store{
		path:  path,
		Hosts: map[string]*model.Host{},
		Pins:  map[string]time.Time{},
		Order: map[string][]model.OrderedItem{},
	}
	b, err := os.ReadFile(path)
	if err == nil {
		if jerr := json.Unmarshal(b, s); jerr == nil {
			if s.Hosts == nil {
				s.Hosts = map[string]*model.Host{}
			}
			if s.Pins == nil {
				s.Pins = map[string]time.Time{}
			}
			if s.Order == nil {
				s.Order = map[string][]model.OrderedItem{}
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
	h.Hidden = hb.Hidden

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

	// Drop manual positions for sessions this host just told us are gone, so a
	// long-lived store does not accumulate entries for dead sessions.
	for group, items := range s.Order {
		alive := items[:0]
		for _, it := range items {
			if it.Host != hb.Host || seen[it.Name] {
				alive = append(alive, it)
			}
		}
		if len(alive) == 0 {
			delete(s.Order, group)
			continue
		}
		s.Order[group] = alive
	}

	for k, until := range s.Pins {
		if now.After(until) {
			delete(s.Pins, k)
		}
	}
	s.saveLocked()
}

// Fleet returns hosts sorted by name, with each session's manual position
// resolved from the stored ordering. Sessions are sorted by name within a host;
// the color-grouped order is the dashboard's business, and it re-sorts anyway.
func (s *Store) Fleet() []model.Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	hosts := make([]model.Host, 0, len(s.Hosts))
	for _, h := range s.Hosts {
		cp := *h
		sessions := append([]model.Session(nil), h.Sessions...)
		sort.Slice(sessions, func(i, j int) bool { return sessions[i].Name < sessions[j].Name })
		for i := range sessions {
			sessions[i].Ord = s.ordLocked(sessions[i].Color, sessions[i].Host, sessions[i].Name)
		}
		cp.Sessions = sessions
		hosts = append(hosts, cp)
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Name < hosts[j].Name })
	return hosts
}

// ordLocked is a session's 1-based manual position inside its color group, or 0
// when it has never been placed by hand.
func (s *Store) ordLocked(group, host, name string) int {
	for i, it := range s.Order[group] {
		if it.Host == host && it.Name == name {
			return i + 1
		}
	}
	return 0
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
			if p.Color != nil && *p.Color != h.Sessions[i].Color {
				h.Sessions[i].Color = *p.Color
				// It left its old group, so its position there is meaningless.
				s.dropFromOrdersLocked(p.Host, p.Name)
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

func (s *Store) dropFromOrdersLocked(host, name string) {
	for group, items := range s.Order {
		kept := items[:0]
		for _, it := range items {
			if it.Host != host || it.Name != name {
				kept = append(kept, it)
			}
		}
		if len(kept) == 0 {
			delete(s.Order, group)
			continue
		}
		s.Order[group] = kept
	}
}

// Palette returns the fleet's colors in display order.
func (s *Store) Palette() []model.PaletteEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paletteLocked()
}

func (s *Store) paletteLocked() []model.PaletteEntry {
	if len(s.Colors) == 0 {
		return colors.DefaultPalette()
	}
	return append([]model.PaletteEntry(nil), s.Colors...)
}

// SetPalette replaces the fleet palette (display order + labels).
func (s *Store) SetPalette(entries []model.PaletteEntry) ([]model.PaletteEntry, error) {
	norm, ok := colors.NormalizePalette(entries)
	if !ok {
		return nil, fmt.Errorf("palette must name each of the %d colors exactly once", len(colors.Palette))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Colors = norm
	s.saveLocked()
	return append([]model.PaletteEntry(nil), norm...), nil
}

// Orders returns every non-empty group ordering, in palette display order.
func (s *Store) Orders() []model.GroupOrder {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.GroupOrder, 0, len(s.Order))
	add := func(group string) {
		if items := s.Order[group]; len(items) > 0 {
			out = append(out, model.GroupOrder{Group: group, Items: append([]model.OrderedItem(nil), items...)})
		}
	}
	for _, name := range colors.OrderOf(s.paletteLocked()) {
		add(name)
	}
	add("") // the uncolored group last
	return out
}

// GroupOrder returns one group's manual ordering.
func (s *Store) GroupOrder(group string) []model.OrderedItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]model.OrderedItem(nil), s.Order[group]...)
}

// SetGroupOrder replaces the manual ordering of one color group. Items are kept
// only when they name a session the fleet knows about *and* that session is
// still in this group, so the store never holds an entry ordLocked cannot
// resolve — a stale dashboard, or one that read the fleet before a recolor,
// cannot park a session in the wrong group.
func (s *Store) SetGroupOrder(group string, items []model.OrderedItem) []model.OrderedItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(items) > maxOrderItems {
		items = items[:maxOrderItems]
	}
	kept := make([]model.OrderedItem, 0, len(items))
	seen := map[string]bool{}
	for _, it := range items {
		if it.Host == "" || it.Name == "" || seen[key(it.Host, it.Name)] {
			continue
		}
		sess := s.findLocked(it.Host, it.Name)
		if sess == nil || sess.Color != group {
			continue
		}
		seen[key(it.Host, it.Name)] = true
		kept = append(kept, it)
	}
	if len(kept) == 0 {
		delete(s.Order, group)
	} else {
		s.Order[group] = kept
	}
	s.saveLocked()
	return append([]model.OrderedItem(nil), kept...)
}

func (s *Store) findLocked(host, name string) *model.Session {
	h, ok := s.Hosts[host]
	if !ok {
		return nil
	}
	for i := range h.Sessions {
		if h.Sessions[i].Name == name {
			return &h.Sessions[i]
		}
	}
	return nil
}
