// Package model holds the wire types shared by hub, agent, client and TUI.
package model

import "time"

// PinDuration is how long a hub-side meta patch (set from a dashboard action)
// blocks agent heartbeats from overwriting color/tag with a stale local view.
const PinDuration = 90 * time.Second

type Session struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Attached bool   `json:"attached"`
	Created  int64  `json:"created"`
	Activity int64  `json:"activity"`
	// Color is a palette key ("" = no color). It is a label someone assigned,
	// never something derived from the session name.
	Color string `json:"color,omitempty"`
	Tag   string `json:"tag,omitempty"`
	// Ord is the manual position of this session inside its color group,
	// resolved by the hub from the fleet's stored ordering. 1-based; 0 means the
	// session was never placed by hand and sorts after every placed one.
	Ord     int    `json:"ord,omitempty"`
	Preview string `json:"preview,omitempty"`

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
	// duplicating the setting — including on the client machine's own view.
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
// Manual ordering is not here: it spans hosts, so it is set per group instead
// (GroupOrder).
type MetaPatch struct {
	Host  string  `json:"host"`
	Name  string  `json:"name"`
	Color *string `json:"color,omitempty"`
	Tag   *string `json:"tag,omitempty"`
}

// PaletteEntry is one color: a stable key plus the label the fleet displays for
// it. Renaming changes only the label, so no session is ever rewritten because
// a name changed.
type PaletteEntry struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

// Palette is the fleet's colors in display order. The slice order *is* the
// grouping order in every view.
type Palette struct {
	Colors []PaletteEntry `json:"colors"`
}

// GroupOrder is the manual ordering of the sessions inside one color group.
// Sessions live on different machines, so their relative position is
// necessarily fleet-wide state and lives on the hub rather than in a tmux
// option: one write, and it still works when a host is offline.
type GroupOrder struct {
	// Group is the color key this ordering belongs to ("" = the uncolored
	// group), so reordering under one color cannot disturb another's.
	Group string        `json:"group"`
	Items []OrderedItem `json:"items"`
}

type OrderedItem struct {
	Host string `json:"host"`
	Name string `json:"name"`
}

// FleetResponse is the GET /api/v1/sessions payload.
type FleetResponse struct {
	Now    time.Time      `json:"now"`
	Host   []Host         `json:"hosts"`
	Order  []GroupOrder   `json:"order,omitempty"`
	Colors []PaletteEntry `json:"colors,omitempty"`
}
