package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Rect is a rectangle of terminal cells: a column, a row, a width and a height.
//
// WHY A TYPE RATHER THAN FOUR NUMBERS. The frame is assembled by drawing and
// then hit-tested by arithmetic, and the two used to be kept in step by hand:
// blockAt recomputed "the banner plus the axes panel plus two newlines" and
// subtracted block heights from it. Every new element above the blocks moved
// that baseline, and the click handler was the last place to find out. Here the
// rectangles are computed ONCE from the layout, the frame is assembled from
// them, and a click is a containment test — the two cannot disagree, because
// there is only one description of where anything is.
type Rect struct {
	X, Y, W, H int
}

// Contains reports whether a screen cell is inside the rectangle.
func (r Rect) Contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// Empty reports whether the rectangle has no area, which is how a section that
// is not drawn at this terminal size is represented.
func (r Rect) Empty() bool { return r.W <= 0 || r.H <= 0 }

// Geometry is where every part of the frame sits at the current size.
//
// The heights are the layout's answer to "how much room does each part get",
// and the widths follow from the terminal. Nothing here knows what is drawn
// inside a part: that is the sections' business.
type Geometry struct {
	// Width and Height are the terminal size the frame is fitted to.
	Width, Height int

	// Banner is the title strip.
	Banner Rect
	// Notes is the warning strip under the banner. It has no height unless the
	// reading carried notes.
	Notes Rect
	// Tabs is the section strip.
	Tabs Rect
	// Body is the active section.
	Body Rect
	// Status is the metrics line above the key bar.
	Status Rect
	// Keys is the help bar.
	Keys Rect
}

// geometry computes the frame's parts for a terminal size.
//
// THE FRAME IS A COLUMN OF BANDS, and every band is claimed in this order:
// banner, notes, tabs, status, keys. The body is what is left. That order is the
// priority: when the terminal is short the bands above and below the body are
// the ones that survive, because a section with no title, no metrics and no way
// to quit is not a monitor — it is a wall of text.
//
// The banner collapses from two lines to one before anything else gives way: it
// is a title, and a title that costs a log line on a 20-row terminal is not
// worth its second row. The notes band is not part of that trade — it appears
// only when the reading carries something the reader must not miss, and it takes
// its line from the body like every other band.
func geometry(width, height int, notes bool) Geometry {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}

	g := Geometry{Width: width, Height: height}

	// THE ORDER OF SACRIFICE is fixed and written down once: the key bar and the
	// status line are one line each and are never dropped — they are how the
	// reader learns what is available and what is happening. The tabs are one
	// line and are never dropped either: they are how the reader knows there is
	// anywhere else to go. The banner is the only band that shrinks.
	bannerH := bannerLines
	if height < shortTerminal {
		bannerH = 1
	}
	bannerH = min(bannerH, max(0, height-3))

	notesH := 0
	if notes {
		notesH = notesLines
	}

	bands := bannerH + notesH + tabsLines + statusLines + keysLines
	bodyH := height - bands
	if bodyH < minBodyLines {
		// A terminal too short even for the bands: the body keeps a floor and the
		// frame is allowed to be taller than the screen. The terminal clips the
		// overflow at the bottom, which costs the key bar — and that is the
		// honest outcome: a frame that dropped the body instead would show
		// nothing at all.
		bodyH = minBodyLines
	}

	y := 0
	g.Banner = Rect{X: 0, Y: y, W: width, H: bannerH}
	y += bannerH

	g.Notes = Rect{X: 0, Y: y, W: width, H: notesH}
	y += notesH

	g.Tabs = Rect{X: 0, Y: y, W: width, H: tabsLines}
	y += tabsLines

	g.Body = Rect{X: 0, Y: y, W: width, H: bodyH}
	y += bodyH

	g.Status = Rect{X: 0, Y: y, W: width, H: statusLines}
	y += statusLines

	g.Keys = Rect{X: 0, Y: y, W: width, H: keysLines}

	return g
}

// Frame band heights.
const (
	// bannerLines is the title strip: the title line and the rule under it.
	bannerLines = 2
	// notesLines is the warning strip, drawn only when the reading carries notes.
	notesLines = 1
	// tabsLines is the section strip.
	tabsLines = 1
	// statusLines is the metrics line.
	statusLines = 1
	// keysLines is the help bar.
	keysLines = 1
	// minBodyLines is the smallest body the layout will produce. Below this the
	// frame is taller than the terminal and the overflow is clipped.
	minBodyLines = 3
	// shortTerminal is the height under which the banner loses its second line.
	shortTerminal = 18
	// narrowTerminal is the width under which the frame switches to its compact
	// form: tab labels shorten, matrix columns drop, status text is trimmed.
	narrowTerminal = 80
)

// bodyInner is the width available to text inside the body, borders and
// horizontal padding excluded.
func (g Geometry) bodyInner() int {
	return contentWidth(g.Body.W)
}

// bodyPage is how many content lines the body shows, its frame excluded.
func (g Geometry) bodyPage() int { return max(1, g.Body.H-2) }

// contentWidth is the width available to text inside a framed block of the
// given outer width.
//
// Four cells: two for the border glyphs and one of padding on each side. The
// floor of one rather than a comfortable minimum: a floor above what the
// terminal can give would make the content wider than its frame, and the frame
// is the one thing the layout promises to keep exact. A single cell of content
// is ugly, and it is still a monitor.
func contentWidth(width int) int {
	return max(1, width-4)
}

// narrow reports whether the frame is in its compact form.
func (g Geometry) narrow() bool { return g.Width < narrowTerminal }

// fitBlock clips and pads a block to exactly width×height cells.
//
// MaxWidth/MaxHeight clip the box and Width/Height pad it, and both are needed:
// with only the padding a long line wraps and the block grows taller than asked
// for, and with only the clipping a short block comes back narrower than the
// frame and the background stops mid-screen.
func fitBlock(block string, width, height int) string {
	if width < 1 || height < 1 {
		return ""
	}
	return lipgloss.NewStyle().
		MaxWidth(width).MaxHeight(height).
		Width(width).Height(height).
		Render(block)
}

// hRule is a horizontal rule of exactly width cells.
func hRule(style lipgloss.Style, width int) string {
	if width < 1 {
		return ""
	}
	return style.Render(repeat("─", width))
}

// repeat is strings.Repeat with the negative case folded away.
//
// strings.Repeat panics on a negative count, and every width in this package is
// derived by subtraction from a terminal size that may be smaller than the
// chrome around it.
func repeat(s string, n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(s, n)
}

// justify lays cells out across exactly width cells, spreading the free space
// evenly between them.
//
// WHAT IT IS FOR. The frame's own bars are the only full-width lines on the
// screen, and a row of short cells gathered at the left of one reads as a line
// that stopped rendering. Spread out, the same cells read as a bar, and the
// first and last land on the frame's own edges — which is also how the reader
// can tell the frame is exact at a glance.
//
// THE REMAINDER GOES TO THE LEFTMOST GAPS. Dividing the free space evenly leaves
// up to gaps-1 cells over, and putting them in the last gap would leave the
// right edge a cell or two short of the frame — the one thing the layout
// promises to keep exact. Cells are laid out at their own widths, so nothing
// here needs to know what is in them: an empty cell is dropped and the rest
// close ranks.
func justify(cells []string, width int) string {
	kept := make([]string, 0, len(cells))
	for _, c := range cells {
		if c != "" {
			kept = append(kept, c)
		}
	}
	if width < 1 {
		return ""
	}
	if len(kept) == 0 {
		return repeat(" ", width)
	}

	used := 0
	for _, c := range kept {
		used += lipgloss.Width(c)
	}
	if len(kept) == 1 {
		return kept[0]
	}

	free := width - used
	if free < len(kept)-1 {
		// No room to space them: the cells would have to be cut to be spread,
		// and a cut counter is worse than a crowded bar.
		return clip(strings.Join(kept, " "), width)
	}

	base, extra := free/(len(kept)-1), free%(len(kept)-1)
	var b strings.Builder
	for i, c := range kept {
		b.WriteString(c)
		if i == len(kept)-1 {
			break
		}
		gap := base
		if i < extra {
			gap++
		}
		b.WriteString(repeat(" ", gap))
	}
	return b.String()
}
