package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// statusLine is the metrics bar between the body and the key bar.
//
// WHAT IT IS FOR. The reader watching the monitor has questions the sections do
// not answer at a glance: how much traffic the day has carried, and in which
// shape the daemon is running. The counters repeat the dashboard's first tile
// and the axes repeat what the journal never says — deliberately: both are asked
// while looking at the journal, and a reader who has to leave the screen they
// are reading to check a number will stop checking it.
//
// THE CELLS ARE SPREAD ACROSS THE WHOLE BAR rather than gathered on the left.
// The bar is one line of a frame that is otherwise full, and a cluster of four
// short cells at its left edge reads as a line that failed to render the rest.
// Spread out, the same cells read as what they are: the frame's own summary,
// from the day's totals to the axes the traffic is leaving through.
//
// THE CELLS DROP WHOLE, from the right, as the bar narrows. The counters are the
// number the reader came for; the axes are one key away under `i` in any case.
func (m Model) statusLine(width int) string {
	if m.toast != nil {
		return m.toastLine(width)
	}
	return m.statusBar(width, m.statusCells(width)...)
}

// statusCells is the metrics bar's cells, as many as fit.
//
// A CELL IS NEVER CLIPPED. Half a value ("credential anonym…") is
// indistinguishable from a rendering fault, and the reader has no way to tell
// that the value continues — so a cell that does not fit is left out and the
// ones before it take the room.
func (m Model) statusCells(width int) []string {
	inner := max(0, width-m.pal.StatusBar.GetHorizontalFrameSize())
	cells := append([]string{m.countersCell()}, m.axesCells()...)

	for len(cells) > 1 && cellsWidth(cells) > inner {
		cells = cells[:len(cells)-1]
	}
	return cells
}

// cellsWidth is the word for the line a set of spread cells takes.
func cellsWidth(cells []string) int {
	w := max(0, len(cells)-1)
	for _, c := range cells {
		w += lipgloss.Width(c)
	}
	return w
}

// statusBar spreads cells across the bar and paints the whole width.
//
// The background is the point: a line of loose cells above the key bar reads as
// another line of content, and the bar is what makes it read as chrome.
func (m Model) statusBar(width int, cells ...string) string {
	inner := max(0, width-m.pal.StatusBar.GetHorizontalFrameSize())
	line := justify(cells, inner)
	if width < 1 {
		return m.pal.StatusBar.Render(line)
	}
	return m.pal.StatusBar.MaxWidth(width).Width(width).Render(line)
}

// liveCell is the freshness and uptime indicator, and it is the banner's.
//
// A DOT AND A WORD, never a colour alone: the dot is what the eye catches and
// "live" is what a monochrome terminal and a screenshot keep. The two states it
// distinguishes are "the daemon answered" and "it did not", and the second one
// is the whole reason the cell exists — a screen that has stopped being fed must
// not keep claiming to be current.
//
// THE COUNTER IS THE DAEMON'S UPTIME, not the age of the reading. "Live for
// eight hours" is a fact about the deployment that a reader can act on; "the
// reading is four seconds old" is a fact about the monitor, and on a frame that
// refreshes every two seconds it is the same string every time it is read — a
// caption that the eye learns to skip, which is exactly what the earlier
// "just now" became.
//
// THE COUNTER IS FIXED WIDTH, which is what lets it sit at the right edge of the
// banner: a counter that grew a cell when it passed ten seconds would shift the
// whole line under the reader's eye. The clock is padded to eight cells, and a
// deployment that has been up for more than four days is allowed one more rather
// than being rounded into a lie.
func (m Model) liveCell() string {
	dot := m.pal.SuccessText.Render("●")
	word := m.pal.SuccessText.Render("live")

	switch {
	case m.err != nil:
		dot = m.pal.ErrorText.Render("●")
		word = m.pal.ErrorText.Render("stale")
	case !m.loaded:
		dot = m.pal.WarningText.Render("●")
		word = m.pal.WarningText.Render("reading")
	}

	return dot + " " + word + " " + m.pal.Dim.Render(padLeft(uptimeClock(m.obs.Uptime), uptimeWidth))
}

// uptimeWidth is the cells the uptime counter occupies: eight, the width of
// HH:MM:SS, so that the banner never reflows as the clock runs.
const uptimeWidth = 8

// uptimeClock renders an uptime as HH:MM:SS, or as a dash of the same width when
// there is no daemon to measure.
//
// The placeholder is dashes rather than a zero: "00:00:00" is a daemon that has
// just started, and a stopped deployment must not be readable as a fresh one.
func uptimeClock(d time.Duration) string {
	if d <= 0 {
		return strings.Repeat("-", uptimeWidth)
	}

	total := int64(d / time.Second)
	return fmt.Sprintf("%02d:%02d:%02d", total/3600, total/60%60, total%60)
}

// countersCell is the day's totals.
//
// The label names the day rather than saying "today", because the cell is the one
// place on every screen that carries the counters: a reader who moved to a past
// day and looked away from the dashboard would otherwise read the day's numbers
// as the current ones. The day is also the position in the recorded days — "3/7"
// — so that the reader can see how far back they are and that the keys have
// somewhere left to go.
func (m Model) countersCell() string {
	if !m.loaded || m.obs.Day == "" {
		return m.pal.Dim.Render("no counters")
	}
	t := m.obs.Totals
	label := m.dayName()
	if pos, count, ok := m.dayPlace(); ok && count > 1 {
		label += fmt.Sprintf(" %d/%d", pos, count)
	}
	cell := m.pal.Label.Render(label) + " " + m.pal.SuccessText.Render(fmt.Sprintf("%d OK", t.OK))
	cell += " " + m.pal.ErrorText.Render(fmt.Sprintf("%d ERR", t.Refused))
	if t.Aborted > 0 {
		cell += " " + m.pal.WarningText.Render(fmt.Sprintf("%d ABT", t.Aborted))
	}
	return cell
}

// axesCells is the deployment's axes, compacted, one cell each.
//
// A FEW AXES, NOT ALL OF THEM. The full set is seven pairs plus the origin of
// each, which is two lines on a wide terminal — the cost that made the old axes
// panel the wrong shape for the frame, and the reason the dashboard carries them
// as a tile instead. The three that decide where traffic goes and under whose
// name are here, one key away from all of them.
func (m Model) axesCells() []string {
	want := map[string]bool{"mode": true, "credential": true, "family": true}
	out := make([]string, 0, len(want))

	for _, row := range m.obs.Daemon {
		if !want[row.Name] {
			continue
		}
		out = append(out, m.pal.Label.Render(row.Name)+" "+m.pal.Value.Render(row.Value))
	}
	return out
}

// toastLine is the transient confirmation that replaces the metrics.
//
// It takes the whole line rather than sharing it: a confirmation that has to
// compete for width with three counters is read as another counter. It is
// dismissed by its own timer, so it never becomes a permanent fixture.
func (m Model) toastLine(width int) string {
	tone, icon := m.pal.Toast, "✓"
	if m.toast.err {
		tone, icon = m.pal.ToastWarn, "!"
	}
	return m.statusBar(width, tone.Render(icon+" "+m.toast.text))
}

// readOnlyTag is the promise the monitor makes, and the last cell of its bar.
//
// It is stated rather than implied. A reader looking at a frame full of settings
// will wonder, sooner or later, whether one of them can be changed from here.
// The answer is on every screen, in the corner they will look for it in, and it
// is why no widget on this screen accepts input.
const readOnlyTag = "read-only · no settings are changed here"

// readOnlyTagShort is the same promise when the bar cannot afford the sentence.
//
// A SECOND LENGTH, NOT A TRUNCATION: "read-only · no setting…" reads as a line
// that broke, and this is the one cell of the frame that must never look like a
// fault. Where even two words do not fit, the promise is left out entirely — the
// hints that say how to move matter more, and the help modal repeats it.
const readOnlyTagShort = "read-only"

// keyBar is the help bar at the bottom of the frame.
//
// IT IS DRIVEN BY THE SAME KEY MAP the handlers use, and the hints are spread
// across the bar like the metrics above them. The full list is not shown here:
// it is a modal under `?`, because the bar is one line and has to choose what to
// leave out — and what it leaves out is what the reader opens the help to find.
func (m Model) keyBar(width int) string {
	hints := m.keys.shortHints(m.geom.narrow())
	cells := m.pal.HintCells(hints...)
	// The promise is dropped before the hints are: the hints say how to move,
	// and the reader who has lost them can still find every one of them in the
	// help — the promise is not in the help, it is the answer to a question they
	// would otherwise have to guess at.
	if len(cells) < len(hints) {
		return m.statusBar(width, cells...)
	}

	inner := max(0, width-m.pal.StatusBar.GetHorizontalFrameSize())
	for _, tag := range []string{readOnlyTag, readOnlyTagShort} {
		if cellsWidth(cells)+lipgloss.Width(tag)+1 > inner {
			continue
		}
		cells = append(cells, m.pal.Dim.Render(tag))
		break
	}
	return m.statusBar(width, cells...)
}
