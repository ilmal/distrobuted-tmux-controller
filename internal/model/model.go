// Package model holds the wire types shared by hub, agent, client and TUI.
package model

import "time"

// PinDuration is how long a hub-side meta patch (set from a dashboard action)
// blocks agent heartbeats from overwriting color/tag with a stale local view.
const PinDuration = 90 * time.Second

type Session struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Windows  int    `json:"windows"`
	Attached bool   `json:"attached"`
	Created  int64  `json:"created"`
	Activity int64  `json:"activity"`
	Color    string `json:"color,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Preview  string `json:"preview,omitempty"`

	// Title is the local @dtc-title user option ("<emoji> <name>"); never
	// sent over the wire, used by the agent to keep tmux titles in sync.
	Title string `json:"-"`
}

type Host struct {
	Name        string    `json:"name"`
	LastSeen    time.Time `json:"last_seen"`
	TmuxVersion string    `json:"tmux_version,omitempty"`
	OS          string    `json:"os,omitempty"`
	Arch        string    `json:"arch,omitempty"`
	// Hidden is the host's own declaration that it is a client machine, sent in
	// its heartbeat. The hub honours it so every dashboard agrees without
	// duplicating the setting; the machine itself is never hidden from itself.
	Hidden   bool      `json:"hidden,omitempty"`
	Sessions []Session `json:"sessions"`
}

func (h *Host) Stale() bool { return time.Since(h.LastSeen) > 3*time.Minute }

type Heartbeat struct {
	Host        string    `json:"host"`
	TmuxVersion string    `json:"tmux_version,omitempty"`
	OS          string    `json:"os,omitempty"`
	Arch        string    `json:"arch,omitempty"`
	Hidden      bool      `json:"hidden,omitempty"`
	Sessions    []Session `json:"sessions"`
}

// MetaPatch is an optimistic dashboard-side change to a session's metadata.
type MetaPatch struct {
	Host  string  `json:"host"`
	Name  string  `json:"name"`
	Color *string `json:"color,omitempty"`
	Tag   *string `json:"tag,omitempty"`
}

// FleetResponse is the GET /api/v1/sessions payload.
type FleetResponse struct {
	Now  time.Time `json:"now"`
	Host []Host    `json:"hosts"`
}
