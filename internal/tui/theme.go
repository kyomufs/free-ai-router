package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/charmtone"

	"free-ai-router/internal/tui/presets"
)

// palette holds the styles in use.
//
// THE ROLES COME FROM THE PRESET THEME, not from a list of colours kept here.
// The preset separates the palette (accent, success, danger, muted, border,
// surface) from the styles built on it, which is the difference between changing
// a colour in one place and hunting for a literal. It also resolves light and
// dark through lipgloss.LightDark, so the monitor is readable on both without
// this file knowing which one it is on.
//
// THE ACCENT FAMILY IS OVERRIDDEN, and it is the only thing overridden. The
// preset's own accent is a violet, and a router wants a cooler, more electric
// hue: the frame reads as wiring rather than as a document. Everything else
// stays as the preset set it, so the semantic colours keep the contrast they
// were chosen for.
//
// THE TWO ACCENTS ARE ONE FAMILY, deliberately: a petrol teal on a light
// terminal and a bright cyan on a dark one, with an azure as the second step of
// the same gradient. A monitor needs its chrome to recede, and a second,
// unrelated hue in the chrome would compete with the counters — which are the
// only thing on the screen allowed to raise their voice.
//
// CYAN IS KEPT CLEAR OF THE SEMANTIC HUES. Success is a green, danger a coral,
// warning a yellow: none of them is a shade of the accent, so "this went
// through" and "this is the frame" are never the same colour at a glance.
//
// Nothing is signalled by colour alone. Every state the palette distinguishes
// also carries a word — OK, ERROR, ABORT, on, off, live — because a monitor read
// over a screenshot or a monochrome terminal must not lose its meaning.
type palette struct {
	presets.Theme

	// TabActivePill and TabIdlePill are the section strip's two states.
	//
	// Rebuilt here rather than taken from the preset because the preset's tab
	// styles pad with one cell on each side, and the strip is hit-tested by
	// column: a padded label would report a click on its padding as a click on
	// the section. These carry the accent as a background and no padding, and the
	// strip adds the gap itself, where it is accounted for.
	TabActivePill lipgloss.Style
	TabIdlePill   lipgloss.Style

	// Toast is the transient confirmation line.
	Toast lipgloss.Style
	// ToastWarn is the same line for a reading that failed.
	ToastWarn lipgloss.Style

	// TileFrame is the border of a dashboard tile, and TileTitle its heading.
	//
	// Tiles are drawn INSIDE the section frame, so they are deliberately the
	// quieter of the two: the outer frame carries the accent and names the
	// section, the tiles inside it are separated by the neutral border and their
	// headings are neither bold nor coloured. Two levels of accent inside one
	// another would read as four frames rather than as one screen with tiles.
	TileFrame lipgloss.Style
	TileTitle lipgloss.Style
}

// newPalette builds the styles for a background.
func newPalette(dark bool) palette {
	t := presets.Charm(dark)
	pick := lipgloss.LightDark(dark)

	// The accent family: petrol teal in the light, cyan in the dark, with the
	// azure of the same ramp as the second accent.
	t.Accent = pick(charmtone.Damson, charmtone.Turtle)
	t.AccentAlt = pick(charmtone.Oceania, charmtone.Malibu)
	t.Info = t.AccentAlt
	t.BorderFocus = t.Accent

	// Everything derived from the accent is rebuilt rather than left at the
	// preset's, which still holds the old colour.
	t.AccentText = t.AccentText.Foreground(t.Accent)
	t.Title = t.Title.Foreground(t.Accent)
	t.Selected = t.Selected.Background(t.Accent)
	t.TabActive = t.TabActive.Background(t.Accent)
	t.FilterMatch = t.FilterMatch.Foreground(t.Accent)

	plain := lipgloss.NewStyle()

	return palette{
		Theme: t,
		// The strip's own cells: the active one is an accent background with the
		// on-accent ink, the idle one is plain muted text. No padding — the strip
		// spaces them.
		TabActivePill: plain.Bold(true).Foreground(t.OnAccent).Background(t.Accent),
		TabIdlePill:   plain.Foreground(t.Muted),
		Toast:         plain.Bold(true).Foreground(t.Success),
		ToastWarn:     plain.Bold(true).Foreground(t.Warning),
		TileFrame:     plain.Foreground(t.Border),
		TileTitle:     plain.Foreground(t.Muted),
	}
}

// accentStyle is the bold accent used for titles and the active marker.
func (p palette) accentStyle() lipgloss.Style {
	return p.AccentText.Bold(true)
}

// borderColor is the frame colour of an inactive block.
func (p palette) borderColor() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(p.Border)
}

// toneColor returns the palette colour of a semantic tone.
//
// The preset keeps the tone table private, and the sections need the colour
// itself — for a progress bar, for a sparkline — not only a rendered style.
func (p palette) toneColor(tone presets.Tone) color.Color {
	switch tone {
	case presets.ToneSuccess:
		return p.Success
	case presets.ToneWarning:
		return p.Warning
	case presets.ToneDanger:
		return p.Danger
	default:
		return p.Info
	}
}
