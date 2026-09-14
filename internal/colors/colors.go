// Package colors defines the session palette shared by the TUI, `ls` output,
// the hub HTML page and the tab-title emoji that tmux displays.
package colors

import "hash/fnv"

type Def struct {
	Name  string
	ANSI  int
	Hex   string
	Emoji string
}

// Palette order doubles as the default "color sort" group order. Colors are
// deliberately pastel (soft, low-saturation ANSI 256 tones) so a full column
// of dots stays calm on a dark terminal.
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

// Auto deterministically assigns a palette color from the session name so
// untagged sessions still group and render consistently everywhere.
func Auto(name string) Def {
	h := fnv.New32a()
	h.Write([]byte(name))
	return Palette[int(h.Sum32())%len(Palette)]
}

// For resolves a session's color: explicit color name if valid, else auto.
func For(name, color string) Def {
	if d, ok := ByName(color); ok {
		return d
	}
	return Auto(name)
}
