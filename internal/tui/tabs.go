package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// tabGap is the space between two tab labels.
//
// One cell, painted by the strip rather than by the label styles: the label
// width is what the hit test compares against, and padding baked into the style
// would make the strip report a click on its own gap as a click on a section.
const tabGap = 2

// tabsLine is the section strip.
//
// THE STRIP IS THE ONLY NAVIGATION, and it is hit-tested by column: a click on a
// label selects that section, exactly as tab does. The labels are short — one
// word each — because the strip shares the width with the banner's rule and a
// strip that wrapped would push the body down by a line.
//
// On a narrow terminal the labels shorten to their first letter rather than
// being clipped: a clipped strip loses the section that fell off the edge, and
// the section that falls off is always the last one — the journal, which is what
// most readers came for.
func (m Model) tabsStrip(width int) string {
	labels := m.tabLabels(width)

	parts := make([]string, 0, len(labels)*2)
	offsets := make([]int, len(labels))

	// The offsets are accumulated with lipgloss.Width rather than by adding the
	// label lengths: the labels carry styling, and a styled string is longer in
	// bytes than it is in cells.
	x := 0
	for i, label := range labels {
		offsets[i] = x
		style := m.pal.TabIdlePill
		if Block(i) == m.Focus {
			style = m.pal.TabActivePill
		}
		rendered := style.Render(label)
		parts = append(parts, rendered)
		x += lipgloss.Width(rendered)
		if i < len(labels)-1 {
			parts = append(parts, repeat(" ", tabGap))
			x += tabGap
		}
	}

	strip := lipgloss.JoinHorizontal(lipgloss.Top, parts...)
	if width > 0 {
		strip = fitBlock(strip, width, tabsLines)
	}
	return strip
}

// tabLabels returns the labels, shortened when the terminal is narrow.
//
// The short form is the first letter of the name, upper-cased, and it is only
// used when the full names would not fit: the tab strip is one line and there is
// nothing to scroll, so a set of labels wider than the terminal simply loses its
// tail.
func (m Model) tabLabels(width int) []string {
	full := make([]string, blockCount)
	short := make([]string, blockCount)
	need := 0
	for i := range full {
		full[i] = Block(i).String()
		short[i] = strings.ToUpper(string([]rune(full[i])[0]))
		need += lipgloss.Width(full[i])
	}
	need += tabGap * (blockCount - 1)

	if need > width {
		return short
	}
	return full
}

// tabAt maps a screen cell to the section whose label is drawn there.
//
// The strip's own rectangle is checked first: a click on the row below the
// labels belongs to the body, not to the section whose column it happens to
// share.
func (m Model) tabAt(x, y int) (Block, bool) {
	if !m.geom.Tabs.Contains(x, y) {
		return 0, false
	}

	labels := m.tabLabels(m.geom.Tabs.W)
	cursor := 0
	for i, label := range labels {
		w := lipgloss.Width(m.pal.TabIdlePill.Render(label))
		if x >= cursor && x < cursor+w {
			return Block(i), true
		}
		cursor += w + tabGap
	}
	return 0, false
}
