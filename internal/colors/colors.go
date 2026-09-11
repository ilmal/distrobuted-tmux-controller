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

// Palette order doubles as the default "color sort" group order.
var Palette = []Def{
	{"red", 203, "#ff5f5f", "🔴"},
	{"orange", 208, "#ff8700", "🟠"},
	{"yellow", 220, "#ffd700", "🟡"},
	{"green", 114, "#87d787", "🟢"},
	{"cyan", 80, "#5fd7d7", "🔷"},
	{"blue", 75, "#5fafff", "🔵"},
	{"purple", 141, "#af87ff", "🟣"},
	{"pink", 218, "#ffafd7", "🌸"},
	{"gray", 245, "#8a8a8a", "⚪"},
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
