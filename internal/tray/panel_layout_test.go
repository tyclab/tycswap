package tray

import (
	"math"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tyclab/tycswap/internal/brand"
)

// fakeMeasure is a fixed-pitch font per role, so every width below is a
// count of runes times the pitch.
type fakeMeasure struct{}

func (fakeMeasure) textWidth(font panelFont, text string) int {
	return utf8.RuneCountInString(text) * [...]int{7, 8, 6, 9}[font]
}
func (fakeMeasure) lineHeight(font panelFont) int { return [...]int{16, 16, 13, 20}[font] }

func panelFixture() Panel {
	return Panel{Title: "tycswap",
		Columns: []Column{{ID: "account", Label: "Account"}, {ID: "fiveHour", Label: "5-hour"}, {ID: "sevenDay", Label: "Weekly"}, {ID: "model:Fable", Label: "Fable"}},
		Rows: []PanelRow{
			{ID: "claude:1", Name: "#1 xxxxxxxxxxxxxxx", Plan: "Ultra", Tag: "not rotated", Cells: []PanelCell{{Absent: true}, {Share: "54%", Pct: 54, Reset: "Sat 10:00 · 2d 22h"}, {Share: "50%", Pct: 50, Reset: "Sat 10:00 · 2d 22h"}}},
			{ID: "claude:2", Name: "#2 xxxxxxxxxxxxxxxxxxxx", Plan: "Pro", Warning: "stale", Active: true, Cells: []PanelCell{{Share: "0%"}, {Share: "100%", Pct: 100, Limit: true, Reset: "Today 02:52 · 12h 22m"}, {Absent: true}}},
			{ID: "codex:1", Name: "Codex #1 xxxxxxxxxxxxxxx", Disabled: true, Cells: []PanelCell{{Absent: true}, {Share: "7%", Pct: 7}, {Absent: true}}},
		},
		Actions: []Item{{ID: "quit", Title: "Quit"}, {ID: "auto", Title: "Auto-switch", Kind: KindToggle, Checked: true},
			{ID: "autostart", Title: "Start at login", Kind: KindToggle}, {ID: "open", Title: "Open dashboard"}}}
}

// Each column is as wide as its widest content: the account column its
// widest name or second line, a limit column its label, a share with a
// minimal bar, or its widest reset text; the action column a Switch button.
func TestPanelColumnWidthsFitTheirContent(t *testing.T) {
	m := fakeMeasure{}
	metrics := panelMetricsOf(m)
	if metrics.unit != 8 || metrics.inset != 12 || metrics.control != 26 {
		t.Fatalf("metrics %+v", metrics)
	}
	widths := panelColumnWidths(panelFixture(), m, metrics)
	want := []int{
		24 * 8,        // "Codex #1 xxxxxxxxxxxxxxx" in the strong font
		4*7 + 4 + 3*8, // "5-hour" is narrower than "100%" and a minimal bar
		21 * 6,        // "Today 02:52 · 12h 22m" in the small font
		18 * 6,        // "Sat 10:00 · 2d 22h"
		6*8 + 2*12,    // "In use" in the strong font, inset both sides
	}
	if !slices.Equal(widths, want) {
		t.Fatalf("widths %v, want %v", widths, want)
	}
	long := panelFixture()
	long.Rows[0].Name = "#1 " + strings.Repeat("x", 80)
	if got := panelColumnWidths(long, m, metrics)[0]; got != panelNameCap*metrics.unit {
		t.Fatalf("a long name widens the account column to %d, cap %d", got, panelNameCap*metrics.unit)
	}
	tagged := panelFixture()
	tagged.Rows = tagged.Rows[:1]
	tagged.Rows[0].Name = "#1 x"
	// "Ultra" in an outlined label, then "not rotated".
	if got := panelColumnWidths(tagged, m, metrics)[0]; got != 5*6+2*4+2+4+11*6 {
		t.Fatalf("second line width %d", got)
	}
}

// Quit sits at the left; the switches and Open dashboard end at the right
// edge on one row while it holds them, and wrap, still right-aligned, when
// it does not.
func TestPanelFooterPlacement(t *testing.T) {
	widths := []int{40, 60, 70, 50}
	right := []bool{false, true, true, true}
	for _, check := range []struct {
		width int
		want  []panelPlace
	}{
		{300, []panelPlace{{0, 0}, {100, 0}, {170, 0}, {250, 0}}},
		{200, []panelPlace{{0, 0}, {0, 1}, {70, 1}, {150, 1}}},
		{150, []panelPlace{{0, 0}, {10, 1}, {80, 1}, {100, 2}}},
	} {
		if got := panelFooterPlace(widths, right, check.width, 10, 20); !slices.Equal(got, check.want) {
			t.Errorf("width %d: %v, want %v", check.width, got, check.want)
		}
	}
	if got := panelFooterPlace([]int{40, 50, 60}, []bool{false, false, true}, 300, 10, 20); !slices.Equal(got, []panelPlace{{0, 0}, {50, 0}, {240, 0}}) {
		t.Errorf("two left buttons: %v", got)
	}
}

func TestPanelUpdateCommandsWrapWithoutWideningAccounts(t *testing.T) {
	p := panelFixture()
	base := layoutPanel(p, fakeMeasure{}, 4000, 4000, 17)
	commands := []Item{{ID: "updates-open", Title: "Check for updates…"}, {ID: "install-update", Title: "Update tycswap"}, {ID: "claude-code", Title: "Update Claude"}}
	for count := 1; count <= len(commands); count++ {
		p.Actions = append(append(append([]Item{}, panelFixture().Actions[:1]...), commands[:count]...), panelFixture().Actions[1:]...)
		g := layoutPanel(p, fakeMeasure{}, 4000, 4000, 17)
		if g.width != base.width || !slices.Equal(g.columns, base.columns) {
			t.Fatalf("%d update commands moved the account columns: %+v", count, g)
		}
		for i, button := range g.buttons {
			if button.x < g.metrics.pad || button.x+button.width > g.width-g.metrics.pad || button.y < g.footer || button.y+button.height > g.height {
				t.Fatalf("%d update commands: button outside the footer: %+v", count, button)
			}
			for _, other := range g.buttons[:i] {
				if button.y == other.y && button.x < other.x+other.width && other.x < button.x+button.width {
					t.Fatalf("overlapping footer controls: %+v and %+v", button, other)
				}
			}
		}
		last := len(g.buttons) - 1
		if g.buttons[last].y != g.buttons[last-1].y || g.buttons[last].y != g.buttons[last-2].y {
			t.Fatal("the toggles and dashboard must stay together")
		}
	}
}

// The panel is as wide as its content and its footer's one row, as tall as
// its rows; past the work area the list scrolls and everything else stays.
func TestPanelGeometryFitsAndScrolls(t *testing.T) {
	m := fakeMeasure{}
	p := panelFixture()
	g := layoutPanel(p, m, 4000, 4000, 17)
	widths := panelColumnWidths(p, m, g.metrics)
	list := 0
	for _, w := range widths {
		list += w
	}
	list += (len(widths) - 1) * g.metrics.gap
	if g.scroll || g.list.height != 3*g.row || g.content != 3*g.row || g.width != list+2*g.metrics.pad {
		t.Fatalf("geometry %+v, list %d", g, list)
	}
	if last := g.columns[len(g.columns)-1]; last.x+last.width != g.width-g.metrics.pad {
		t.Fatalf("action column ends at %d of %d", last.x+last.width, g.width)
	}
	tops := map[int]bool{}
	for _, button := range g.buttons {
		tops[button.y] = true
		if button.y < g.footer || button.y+button.height > g.height {
			t.Fatalf("footer button %+v outside the footer from %d to %d", button, g.footer, g.height)
		}
	}
	if len(tops) != 1 || g.buttons[3].x+g.buttons[3].width != g.width-g.metrics.pad || g.buttons[0].x != g.metrics.pad {
		t.Fatalf("footer not one row from edge to edge: %+v", g.buttons)
	}
	if g.title.y+g.title.height > g.header.y || g.header.y+g.header.height > g.list.y || g.list.y+g.list.height > g.note.y || g.note.y+g.note.height > g.footer {
		t.Fatalf("bands overlap: %+v", g)
	}

	short := layoutPanel(p, m, 4000, g.height-g.row, 17)
	if !short.scroll || short.height > g.height-g.row || short.list.height != g.list.height-g.row || short.content != g.content {
		t.Fatalf("short geometry %+v", short)
	}
	if short.width != g.width+17 {
		t.Fatalf("the scroll bar did not widen the panel: %d vs %d", short.width, g.width)
	}
	wide := p
	wide.Actions = append([]Item{{ID: "install-update", Title: strings.Repeat("u", 100)}}, p.Actions...)
	if w := layoutPanel(wide, m, 4000, 4000, 17); w.width <= g.width || w.columns[4].x+w.columns[4].width != w.width-w.metrics.pad || w.columns[0].width != g.columns[0].width+w.width-g.width {
		t.Fatalf("a wider footer left the Switch column off the right edge: %+v", w.columns)
	}
	narrow := layoutPanel(p, m, g.width-40, 4000, 17)
	if narrow.width > g.width-40 || narrow.columns[0].width != g.columns[0].width-40 {
		t.Fatalf("narrow panel %d px, account column %d", narrow.width, narrow.columns[0].width)
	}
}

// A row's Switch sits in the action column, centred in the row, moved by
// the scroll offset; revealing a row scrolls the least that shows it whole.
func TestPanelRowHitAreasAndReveal(t *testing.T) {
	g := layoutPanel(panelFixture(), fakeMeasure{}, 4000, 4000, 17)
	action := g.columns[len(g.columns)-1]
	for i := range 3 {
		r := g.switchRect(i, 10)
		if r.x != action.x || r.width != action.width || r.height != g.metrics.control || r.y != i*g.row+(g.row-g.metrics.control)/2-10 {
			t.Fatalf("row %d switch %+v", i, r)
		}
	}
	g.list.height = g.row + g.row/2
	if got := g.scrollToShow(2, 0); got != 3*g.row-g.list.height {
		t.Fatalf("reveal last row from the top: %d", got)
	}
	if got := g.scrollToShow(0, g.row); got != 0 {
		t.Fatalf("reveal first row: %d", got)
	}
	if got := g.scrollToShow(1, g.row); got != g.row {
		t.Fatalf("a visible row moved the list: %d", got)
	}
}

// Painted text is invisible to a screen reader: each Switch button carries
// the row as words, and the switches their state.
func TestPanelAccessibleTexts(t *testing.T) {
	p := panelFixture()
	if got := panelSummary(p.Columns, p.Rows[0]); got != "Switch to #1 xxxxxxxxxxxxxxx, Ultra, not rotated, Weekly 54%, Fable 50%" {
		t.Fatalf("summary %q", got)
	}
	if got := panelSummary(p.Columns, p.Rows[1]); got != "#2 xxxxxxxxxxxxxxxxxxxx, in use, Pro, stale, 5-hour 0%, Weekly 100%" {
		t.Fatalf("active summary %q", got)
	}
	if panelToggleText(p.Actions[1]) != "Auto-switch, on" || panelToggleText(p.Actions[2]) != "Start at login, off" {
		t.Fatal("switch texts")
	}
	m, metrics := fakeMeasure{}, panelMetricsOf(fakeMeasure{})
	if got := panelFooterWidth(p.Actions[3], m, metrics); got != m.textWidth(panelBody, "Open dashboard")+2*metrics.inset {
		t.Fatalf("Open dashboard is %d px, not a plain button", got)
	}
	if !panelSwitchEnabled(p, p.Rows[0]) || panelSwitchEnabled(p, p.Rows[1]) || panelSwitchEnabled(p, p.Rows[2]) {
		t.Fatal("switch availability")
	}
	p.Offline = true
	if panelSwitchEnabled(p, p.Rows[0]) || panelCommandEnabled(p, p.Actions[1]) ||
		!panelCommandEnabled(p, p.Actions[0]) || !panelCommandEnabled(p, p.Actions[2]) || !panelCommandEnabled(p, p.Actions[3]) {
		t.Fatal("offline: Switch and Auto-switch must be off limits; Quit, Start at login and Open dashboard not")
	}
}

func TestPanelCopyIsIndependentOfProducer(t *testing.T) {
	p := panelFixture()
	c := clonePanel(p)
	p.Rows[0].Cells[1].Share = "99%"
	p.Columns[1].Label = "changed"
	p.Actions[0].Title = "changed"
	if c.Rows[0].Cells[1].Share != "54%" || c.Columns[1].Label != "5-hour" || c.Actions[0].Title != "Quit" {
		t.Fatal("panel retained mutable producer data")
	}
}

func relativeLuminance(c uintptr) float64 {
	channel := func(v uintptr) float64 {
		s := float64(v&0xff) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(c) + 0.7152*channel(c>>8) + 0.0722*channel(c>>16)
}

func contrast(a, b uintptr) float64 {
	x, y := relativeLuminance(a), relativeLuminance(b)
	return (max(x, y) + 0.05) / (min(x, y) + 0.05)
}

// Every text colour keeps 4.5:1 against the panel background in both
// themes, the accent included.
func TestPanelPaletteContrast(t *testing.T) {
	for _, dark := range []bool{false, true} {
		accent := brand.Sanitized().AccentColor
		if !dark {
			accent = brand.LightAccent(accent)
		}
		c := panelPalette(dark, colorRef(accent))
		for name, color := range map[string]uintptr{"text": c.text, "muted": c.muted, "accent": c.accent, "limit": c.limit} {
			if ratio := contrast(color, c.background); ratio < 4.5 {
				t.Errorf("dark=%v %s %#06x on %#06x: %.2f:1", dark, name, color, c.background, ratio)
			}
		}
		if c.track == c.background || c.line == c.background {
			t.Errorf("dark=%v: bar track or rule invisible", dark)
		}
		if c.disabledAccent == c.accent || c.disabledAccent == c.muted || contrast(c.disabledAccent, c.background) < 3 {
			t.Errorf("dark=%v: disabled on state must retain a distinct, visible accent", dark)
		}
	}
	// The approved picture's orange.
	if panelPalette(true, 0).limit != colorRef("#ffb454") || panelPalette(false, 0).limit != colorRef("#a84300") {
		t.Fatalf("limit colours %#06x / %#06x", panelPalette(true, 0).limit, panelPalette(false, 0).limit)
	}
	if colorRef("#5aa2ff") != 0xffa25a {
		t.Fatalf("colorRef %#x", colorRef("#5aa2ff"))
	}
}
