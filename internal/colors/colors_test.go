package colors

import (
	"strings"
	"testing"

	"github.com/ilmal/distrobuted-tmux-controller/internal/model"
)

// Uncolored's ANSI is -1, and -1 is not a valid SGR parameter: a caller that
// builds "38;5;<ANSI>" without checking Colored first prints literal garbage in
// a text UI, and a filled dot in the default foreground in a terminal. Pin the
// gate instead of trusting every caller to ask.
func TestColoredIsFalseOnlyForUncolored(t *testing.T) {
	if Uncolored.Colored() {
		t.Fatal("Uncolored reports itself as colored")
	}
	if Uncolored.ANSI != -1 {
		t.Fatalf("Uncolored.ANSI = %d, want -1 (no valid SGR parameter to emit)", Uncolored.ANSI)
	}
	for _, d := range Palette {
		if !d.Colored() {
			t.Errorf("palette color %q does not report itself as colored", d.Name)
		}
	}
}

// The dot is the one thing every surface draws per session, so it has to carry
// the colored/uncolored distinction rather than each caller repeating it.
func TestDotPairsWithColored(t *testing.T) {
	if got := Uncolored.Dot(); got != HollowDot {
		t.Errorf("uncolored dot = %q, want %q", got, HollowDot)
	}
	for _, d := range Palette {
		if got := d.Dot(); got != FilledDot {
			t.Errorf("%s dot = %q, want %q", d.Name, got, FilledDot)
		}
	}
}

// Nothing is colored by default: an unset or unknown key must resolve to
// uncolored rather than being invented into a real palette color.
func TestForNeverInventsAColor(t *testing.T) {
	if got := For("anything", ""); got.Colored() {
		t.Errorf("unset color resolved to %q", got.Name)
	}
	if got := For("anything", "chartreuse"); got.Colored() {
		t.Errorf("unknown key resolved to %q", got.Name)
	}
	if got := For("anything", "green"); got.Name != "green" {
		t.Errorf("green resolved to %q", got.Name)
	}
}

func TestTitleDropsTheEmojiWhenUncolored(t *testing.T) {
	if got := Uncolored.Title("probe"); got != "probe" {
		t.Errorf("uncolored title = %q, want the bare session name", got)
	}
	d, _ := ByName("green")
	if got := d.Title("probe"); !strings.HasSuffix(got, " probe") || got == " probe" {
		t.Errorf("green title = %q", got)
	}
}

// A rename changes a label only. If a palette could change a key, a stored
// @dtc-color would stop resolving and the session would silently lose its group.
func TestNormalizePaletteRequiresEachKeyExactlyOnce(t *testing.T) {
	if _, ok := NormalizePalette(DefaultPalette()); !ok {
		t.Fatal("the default palette does not validate")
	}

	short := DefaultPalette()[:8]
	if _, ok := NormalizePalette(short); ok {
		t.Error("a palette missing a color was accepted")
	}

	dup := DefaultPalette()
	dup[1].Name = dup[0].Name
	if _, ok := NormalizePalette(dup); ok {
		t.Error("a palette naming a color twice was accepted")
	}

	unknown := DefaultPalette()
	unknown[0].Name = "chartreuse"
	if _, ok := NormalizePalette(unknown); ok {
		t.Error("a palette with an unknown key was accepted")
	}
}

// A blank label would render a nameless group heading, so it falls back to the
// key rather than to nothing.
func TestNormalizePaletteFillsInABlankLabel(t *testing.T) {
	entries := DefaultPalette()
	entries[3].Label = "   "
	got, ok := NormalizePalette(entries)
	if !ok {
		t.Fatal("palette rejected")
	}
	if got[3].Label != got[3].Name {
		t.Errorf("blank label became %q, want the key %q", got[3].Label, got[3].Name)
	}
}

func TestNormalizeLabelMakesALabelSafeToRender(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"  wip\t\n  stuff  ", "wip stuff"},
		{"", ""},
		{"   ", ""},
	} {
		if got := NormalizeLabel(tc.in); got != tc.want {
			t.Errorf("NormalizeLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := NormalizeLabel("\x1b[31mred"); strings.ContainsRune(got, 0x1b) {
		t.Errorf("a control character survived into the label: %q", got)
	}
	if got := NormalizeLabel(strings.Repeat("x", 100)); len(got) > maxLabelBytes {
		t.Errorf("label not clipped to %d bytes (got %d)", maxLabelBytes, len(got))
	}
}

// A partial palette must still name every color, or a group would vanish from
// the view entirely.
func TestOrderOfKeepsEveryColor(t *testing.T) {
	got := OrderOf([]model.PaletteEntry{{Name: "green"}, {Name: "red"}})
	if len(got) != len(Palette) {
		t.Fatalf("OrderOf returned %d colors, want %d", len(got), len(Palette))
	}
	if got[0] != "green" || got[1] != "red" {
		t.Errorf("palette order not honoured: %v", got[:2])
	}
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n] {
			t.Errorf("duplicate color %q in the order", n)
		}
		seen[n] = true
	}
}

func TestLabelOfFallsBackToTheKey(t *testing.T) {
	pal := []model.PaletteEntry{{Name: "green", Label: "wip"}}
	if got := LabelOf(pal, "green"); got != "wip" {
		t.Errorf("LabelOf(green) = %q, want wip", got)
	}
	if got := LabelOf(pal, "red"); got != "red" {
		t.Errorf("LabelOf(red) = %q, want the key itself", got)
	}
}
