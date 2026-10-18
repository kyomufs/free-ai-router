package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"

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
// THE ACCENT IS OVERRIDDEN, and it is the only colour overridden. A monitor has
// to look like the thing it monitors, and the router's own accent is what the
// rest of the project already uses; every other role stays as the preset set it.
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
}

// newPalette builds the styles for a background.
func newPalette(dark bool) palette {
	t := presets.Charm(dark)
	pick := lipgloss.LightDark(dark)

	// The accent. A monitor has to look like the thing it monitors.
	t.Accent = pick(lipgloss.Color("#0B5FFF"), lipgloss.Color("#7AA2F7"))
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
