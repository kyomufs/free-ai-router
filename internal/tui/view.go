package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"free-ai-router/internal/tui/presets"
)

// View implements tea.Model.
//
// It returns a tea.View rather than a string: in v2 the terminal's own state —
// the alternate screen, the title, the mouse mode — is part of the view, and the
// monitor sets all three.
//
// THE ALTERNATE SCREEN IS ENTERED UNLESS Scrollback IS SET. Full screen is what
// makes the monitor an application rather than a printout: the frame keeps its
// shape as readings arrive, nothing scrolls it out from under the cursor, and
// the terminal is restored on exit. --scrollback is the other half of the
// choice, for a run whose output is meant to be read afterwards.
//
// The title is set because a full-screen application that does not name itself
// leaves the operator guessing which window this is. It costs nothing and is
// restored on exit along with the screen.
func (m Model) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}

	frame := m.frame()

	// A MODAL IS COMPOSITED OVER THE FRAME, NOT DRAWN INTO IT. The reader must
	// still see the row the modal is about — a modal that displaced the frame
	// would take away the context exactly when it was asked for.
	if m.overlay != nil && m.monitoring() {
		frame, _ = m.pal.Overlay(frame, m.modal(), m.geom.Width, m.geom.Height)
	}

	// THE FRAME IS FITTED TO THE TERMINAL, not assembled and hoped for. Fit clips
	// and pads to exactly width×height, which is what makes the monitor read as
	// an application: on the alternate screen a band of empty rows under the
	// footer is the difference between a window and a printout.
	v := tea.NewView(presets.Fit(frame, m.geom.Width, m.geom.Height))
	v.AltScreen = !m.Scrollback
	v.WindowTitle = "free-ai-router · monitor"
	// THE MOUSE IS OPTIONAL AND NEVER THE ONLY PATH. Every action here is
	// reachable from the keyboard; the wheel and the click only spare the reader
	// from reaching for the arrow keys while reading.
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// monitoring reports whether a reading has arrived and nothing is wrong with it.
//
// It is the condition for everything that needs the daemon's data to mean
// anything: the sections, the modals, the hit tests. One predicate rather than
// the same three-term test spelled out at each of them.
func (m Model) monitoring() bool { return m.loaded && m.err == nil }

// barren reports whether the reading carries nothing to show at all: a daemon
// that answered, with no providers, no traffic and no journal.
//
// It is different from a reading that failed. A failure is the monitor's problem
// and is the monitor's to report; a barren reading is the deployment's state,
// and what the reader needs is the command that changes it.
func (m Model) barren() bool {
	return len(m.obs.Providers) == 0 && len(m.obs.Addresses) == 0 && len(m.obs.Logs) == 0
}

// frame assembles the whole monitor: the bands, in the order the geometry
// allocated them.
//
// THIS IS THE SINGLE DESCRIPTION OF THE LAYOUT. The click handler reads the same
// geometry, so a band that appears or disappears moves both the drawing and the
// hit test together. The version this replaced measured the frame by drawing it
// with empty blocks and recomputed the block offsets by hand in the update loop;
// adding anything above the blocks silently moved what a click landed on.
func (m Model) frame() string {
	bands := make([]string, 0, 6)

	if !m.geom.Banner.Empty() {
		bands = append(bands, m.banner())
	}
	if !m.geom.Notes.Empty() {
		bands = append(bands, m.notesLine())
	}
	if !m.geom.Tabs.Empty() {
		bands = append(bands, m.tabsStrip(m.geom.Tabs.W))
	}
	if !m.geom.Body.Empty() {
		bands = append(bands, m.bodyFrame())
	}
	if !m.geom.Status.Empty() {
		bands = append(bands, m.statusLine(m.geom.Status.W))
	}
	if !m.geom.Keys.Empty() {
		bands = append(bands, m.keyBar(m.geom.Keys.W))
	}

	return strings.Join(bands, "\n")
}

// banner is the title strip: the product on the left, the daemon's pulse on the
// right.
//
// THE RIGHT CELL IS THE ONLY PLACE THE MONITOR ANSWERS "IS THIS CURRENT". It says
// whether the reading is live, how long the daemon has been up, and nothing else:
// the age of the reading is already the difference between the clock in that cell
// and the clock on the wall, and a caption spelling it out ("just now", "3s ago")
// would be a number that changes on every redraw and pushes the frame around with
// it.
//
// THE LEFT CELL IS ONLY THE TITLE. What the frame is and that it changes nothing
// are said once, in the key bar along the bottom, where they are read while
// learning the keys rather than skipped as part of a heading.
func (m Model) banner() string {
	w := m.geom.Banner.W
	title := m.pal.GradientTitle("free-ai-router")
	right := m.liveCell()

	// THE PULSE KEEPS ITS CELLS, the title gives way: the pulse is fixed-width by
	// construction and a shortened one would stop being a clock, while a shortened
	// title still names the program.
	left := w - lipgloss.Width(right)
	line := right
	if left > 0 {
		line = padTo(clip(title, left-1), left-1) + " " + right
	}
	if m.geom.Banner.H < bannerLines {
		return line
	}
	return line + "\n" + m.pal.Separator(w)
}

// notesLine is the warning strip: what the reading says the reader must not have
// to infer for themselves.
//
// It takes a row from the body only when there is something to say, which is why
// it is a band of its own rather than a corner of the status line: a note is
// about the whole reading — the daemon is unreachable, the journal is missing —
// and hiding it behind a busy line would be how it gets missed.
func (m Model) notesLine() string {
	if len(m.obs.Notes) == 0 {
		return ""
	}
	return clip(m.pal.Notice(presets.ToneWarning, strings.Join(m.obs.Notes, "  ·  ")), m.geom.Notes.W)
}

// bodyFrame draws the active section between its borders.
func (m Model) bodyFrame() string {
	w, h := m.geom.Body.W, m.geom.Body.H
	if w < 1 || h < 1 {
		return ""
	}
	if h < 2 {
		return clip(m.sectionTitle(), w)
	}

	rows := m.bodyRows(h - 2)
	lines := make([]string, 0, h)
	lines = append(lines, m.boxTop(m.sectionTitle(), m.sectionMeta(), w))
	for _, r := range rows {
		lines = append(lines, m.boxRow(r, w))
	}
	lines = append(lines, m.boxBottom(w))
	return strings.Join(lines, "\n")
}

// bodyRows is the content of the body, exactly n lines.
//
// THE STATES COME FIRST. A frame that drew the sections while the daemon was
// unreachable would show the last reading as if it were current, and the reader
// would have to notice the failure in the status line to know better. Drawing
// the state instead makes the frame unable to lie.
func (m Model) bodyRows(n int) []string {
	if n <= 0 {
		return nil
	}

	switch {
	case m.err != nil:
		return centredGroup(m.pal.StateError("the daemon could not be read", m.err.Error()), n)
	case !m.loaded:
		return centredGroup(m.pal.StateLoading(m.spinner.View(), "reading the daemon's state…"), n)
	case m.barren():
		return centredGroup(m.pal.StateEmpty("nothing to show yet",
			"start the daemon, then press r to read again"), n)
	}

	inner := contentWidth(m.geom.Body.W)
	body := m.sectionOf(inner)

	off := clampScroll(m.Scroll[m.Focus], len(body), n)
	end := min(len(body), off+n)

	rows := make([]string, 0, n)
	rows = append(rows, body[off:end]...)
	for len(rows) < n {
		rows = append(rows, "")
	}
	return rows
}

// centredGroup places a short state block in the middle of n rows.
//
// Centred rather than pinned to the top: a state message at the top of a tall
// empty frame reads as the first line of content, and it is not content — it is
// what the frame has instead of content.
func centredGroup(block string, n int) []string {
	lines := strings.Split(block, "\n")
	if len(lines) > n {
		return lines[:n]
	}

	rows := make([]string, 0, n)
	top := (n - len(lines)) / 2
	for i := 0; i < top; i++ {
		rows = append(rows, "")
	}
	rows = append(rows, lines...)
	for len(rows) < n {
		rows = append(rows, "")
	}
	return rows
}

// clampScroll keeps a scroll offset inside the content.
//
// It is called by the view as well as by the handlers: the body can shrink under
// a scroll offset when a note appears or the terminal is resized, and an offset
// past the end would show a blank section with no way to tell why.
func clampScroll(off, total, page int) int {
	return min(max(0, off), max(0, total-page))
}

// sectionTitle is the active section, as the frame's own title.
//
// It repeats the tab strip's active label on purpose: the frame's top border is
// what survives a screenshot, a scroll back through the terminal's history, or a
// reader who has been looking at the journal long enough to forget which tab
// they are on.
func (m Model) sectionTitle() string { return m.Focus.String() }

// sectionMeta is the count that belongs to the section frame.
//
// It sits in the border rather than on a line of its own: one line of every
// section spent on a count is one line of the thing being counted, and the
// border was already there.
//
// The sort mode is NOT repeated here — it belongs to the matrix tile's own
// heading, next to the table it orders. The count of what is on the screen is
// about the frame, the sort is about one tile inside it.
func (m Model) sectionMeta() string {
	if m.Focus != BlockLogs {
		return plural(len(m.obs.Providers), "provider", "providers") +
			" · " + plural(len(m.obs.Addresses), "address", "addresses")
	}

	if m.editing {
		return "filter " + m.filterDraft + "▏"
	}
	meta := plural(len(m.filtered), "line", "lines")
	if n := len(m.obs.Logs); n != len(m.filtered) {
		meta = itoa(len(m.filtered)) + " of " + itoa(n) + " lines"
	}
	// The day is named whenever the journal is filtered to one: the meta line is
	// what says the block is not showing everything the file holds, and on a past
	// day it is showing a selection of a tail rather than a tail.
	if day := m.logDay(); day != "" {
		meta += " · " + day
	}
	if m.filter != "" {
		meta += " · filter " + m.filter
	}
	return meta + " · follow " + onOff(m.Follow) + " · wrap " + onOff(m.Wrap)
}

// plural renders a count with its noun.
//
// Both forms are passed rather than an "s" being appended: "addresss" is what
// appending produces, and a frame that misspells a word reads as a frame whose
// arithmetic is wrong too.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoa(n) + " " + many
}
