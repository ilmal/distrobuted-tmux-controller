// Package colors defines the session palette shared by the TUI, `ls` output,
// the hub HTML page and the tab-title emoji that tmux displays.
package colors

import (
	"strings"

	"github.com/ilmal/distrobuted-tmux-controller/internal/model"
)

type Def struct {
	// Name is the stable key written to the `@dtc-color` tmux option and sent
	// over the wire. Renaming a color in the dashboard changes only its Label,
	// never this — so no session ever has to be rewritten because a name
	// changed.
	Name  string
	ANSI  int
	Hex   string
	Emoji string
}

// Uncolored is what a session with no `@dtc-color` resolves to, and it is the
// default: nothing assigns a color on your behalf any more. The fleet starts
// as one plain list, and a color means exactly what you decide it means.
var Uncolored = Def{ANSI: -1}

// Colored reports whether this definition is an actual palette color (as
// opposed to Uncolored). Rendering code needs it because Uncolored carries no
// ANSI value, no hex and no emoji to draw.
func (d Def) Colored() bool { return d.Name != "" }

// Title is the terminal tab title for a session: "<emoji> <name>" when it has a
// color, and just the name when it does not.
func (d Def) Title(session string) string {
	if !d.Colored() {
		return session
	}
	return d.Emoji + " " + session
}

// Palette is the canonical set of colors: their ANSI tone, hex and emoji are
// fixed here, because a dashboard should never be able to break how a color
// renders. Only the display label and the order are user-editable (see
// DefaultPalette and model.PaletteEntry).
//
// The order below is the default grouping order in the colors view. Tones are
// deliberately pastel (soft, low-saturation) so a full column of dots stays
// calm on a dark terminal.
var Palette = []Def{
	{"red", 217, "#ffafaf", "🔴"},
	{"orange", 216, "#ffaf87", "🟠"},
	{"yellow", 222, "#ffd787", "🟡"},
	{"green", 157, "#afffaf", "🟢"},
	{"cyan", 159, "#afffff", "🔷"},
	{"blue", 153, "#afd7ff", "🔵"},
	{"purple", 183, "#d7afff", "🟣"},
	{"pink", 218, "#ffafd7", "🌸"},
	{"gray", 250, "#bcbcbc", "⚪"},
}

func ByName(name string) (Def, bool) {
	for _, d := range Palette {
		if d.Name == name {
			return d, true
		}
	}
	return Def{}, false
}

// PaletteIndex is the key's position in the canonical palette, or -1. It is the
// stable tiebreak when two colors somehow report the same display position.
func PaletteIndex(name string) int {
	for i, d := range Palette {
		if d.Name == name {
			return i
		}
	}
	return -1
}

// LabelOf returns the display label for a color key from a palette, falling
// back to the key itself when the palette does not mention it.
func LabelOf(colors []model.PaletteEntry, name string) string {
	for _, e := range colors {
		if e.Name == name {
			return e.Label
		}
	}
	return name
}

// OrderOf returns the color keys in display order for a palette. Unknown keys
// are dropped and missing ones appended in canonical order, so a partially
// stale palette still renders every group.
func OrderOf(pal []model.PaletteEntry) []string {
	var out []string
	seen := map[string]bool{}
	for _, e := range pal {
		if _, ok := ByName(e.Name); ok && !seen[e.Name] {
			seen[e.Name] = true
			out = append(out, e.Name)
		}
	}
	for _, d := range Palette {
		if !seen[d.Name] {
			out = append(out, d.Name)
		}
	}
	return out
}

// For resolves a session's color. An unset or unknown color resolves to
// Uncolored rather than being invented — a color is something you assign.
func For(sessionName, color string) Def {
	if d, ok := ByName(color); ok {
		return d
	}
	return Uncolored
}

// DefaultPalette is the palette a fresh fleet starts with: the canonical order,
// each color labelled with its own key.
func DefaultPalette() []model.PaletteEntry {
	out := make([]model.PaletteEntry, 0, len(Palette))
	for _, d := range Palette {
		out = append(out, model.PaletteEntry{Name: d.Name, Label: d.Name})
	}
	return out
}

// NormalizePalette validates a palette payload and returns it with labels
// trimmed and clipped, or ok=false. A palette must be exactly the canonical
// colors — each once, in the order the caller wants them displayed — so a bad
// dashboard payload can never leave the fleet without a color it relies on, or
// with an unknown key no session can resolve.
func NormalizePalette(entries []model.PaletteEntry) ([]model.PaletteEntry, bool) {
	if len(entries) != len(Palette) {
		return nil, false
	}
	out := make([]model.PaletteEntry, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		def, ok := ByName(e.Name)
		if !ok || seen[e.Name] {
			return nil, false
		}
		label := NormalizeLabel(e.Label)
		if label == "" {
			label = def.Name // an unlabelled color falls back to its key
		}
		seen[e.Name] = true
		out = append(out, model.PaletteEntry{Name: e.Name, Label: label})
	}
	return out, true
}

// NormalizeLabel makes a user-supplied label safe to render: it strips control
// characters, collapses whitespace runs, trims, and clips to a width that still
// fits a group heading.
func NormalizeLabel(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			r = ' '
		}
		if r == ' ' {
			if space || b.Len() == 0 {
				continue
			}
			space = true
		} else {
			space = false
		}
		b.WriteRune(r)
		if b.Len() > maxLabelBytes {
			break
		}
	}
	out := strings.TrimRight(b.String(), " ")
	for len(out) > maxLabelBytes {
		out = out[:len(out)-1]
	}
	return out
}

// maxLabelBytes bounds a label so a group heading cannot be pushed off screen.
const maxLabelBytes = 32
