package tui

import (
	"fmt"
	"strings"
)

// statusLine is the metrics line between the body and the key bar.
//
// WHAT IT IS FOR. The reader watching the monitor has two questions that the
// sections do not answer at a glance: "is this reading live" and "how much
// traffic was there today". The counters are in the address section, but only
// when that section is the one on screen, and the age of the reading is nowhere.
// One line answers both, on every section, and it is the line the eye lands on
// when something starts moving.
//
// THE LINE IS BUILT FROM CELLS THAT DROP WHOLE. On a narrow terminal the cells
// are dropped from the right — the deployment axes first, then the counters —
// because the leftmost cell is the freshness indicator, and a monitor that
// cannot say whether it is live is worse than one that cannot say what mode the
// daemon runs in.
func (m Model) statusLine(width int) string {
	if m.toast != nil {
		return m.toastLine(width)
	}

	cells := []string{m.liveCell(), m.countersCell()}
	if !m.geom.narrow() {
		cells = append(cells, m.axesCell()...)
	}
	return m.statusBar(width, cells...)
}

// statusBar joins cells with a separator and paints the whole width.
//
// The background is the point: a line of loose cells above the key bar reads as
// another line of content, and the bar is what makes it read as chrome.
func (m Model) statusBar(width int, cells ...string) string {
	kept := make([]string, 0, len(cells))
	for _, c := range cells {
		if c != "" {
			kept = append(kept, c)
		}
	}
	line := strings.Join(kept, m.pal.Dim.Render("  │  "))
	if width < 1 {
		return m.pal.StatusBar.Render(line)
	}
	return m.pal.StatusBar.MaxWidth(width).Width(width).Render(clip(line, width))
}

// liveCell is the freshness indicator.
//
// A dot AND a word, never a colour alone: the dot is what the eye catches, and
// "live" is what a monochrome terminal and a screenshot keep. The age follows in
// seconds, because "live" without a number is a claim, and the reader watching a
// daemon that has stopped answering needs to see the number grow.
func (m Model) liveCell() string {
	dot := m.pal.SuccessText.Render("●")
	word := m.pal.SuccessText.Render("live")

	if m.err != nil {
		dot = m.pal.ErrorText.Render("●")
		word = m.pal.ErrorText.Render("stale")
	} else if !m.loaded {
		dot = m.pal.WarningText.Render("●")
		word = m.pal.WarningText.Render("reading")
	}

	cell := dot + " " + word
	if m.loaded && !m.obs.At.IsZero() {
		cell += m.pal.Dim.Render(" " + since(m.obs.At))
	}
	return cell
}

// countersCell is the day's totals.
//
// It repeats what the address section's last line shows, and that repetition is
// deliberate: the totals are the answer to "how much traffic was there", which
// is asked while looking at any of the three sections.
func (m Model) countersCell() string {
	if !m.loaded || m.obs.Totals.Day == "" {
		return m.pal.Dim.Render("no counters for today")
	}
	t := m.obs.Totals
	cell := m.pal.SuccessText.Render(fmt.Sprintf("%d OK", t.OK))
	cell += " " + m.pal.ErrorText.Render(fmt.Sprintf("%d ERR", t.Refused))
	if t.Aborted > 0 {
		cell += " " + m.pal.WarningText.Render(fmt.Sprintf("%d ABT", t.Aborted))
	}
	return cell
}

// axesCell is the deployment's axes, compacted.
//
// A FEW AXES, NOT ALL OF THEM. The full set is seven pairs plus the origin of
// each, which is two lines on a wide terminal and three on a narrow one — the
// cost that made the old axes panel the wrong shape for the frame. The three
// that decide where traffic goes and under whose name are here; everything is
// one key away under `i`.
func (m Model) axesCell() []string {
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

// keyBar is the help bar at the bottom of the frame.
//
// IT IS THE PRESET'S BAR, driven by the same key map the handlers use. The
// earlier version declared the bindings in the view and acted on them in the
// update loop with a bare string switch, which is two lists that agree until one
// of them is edited — and the bar is exactly the place where the disagreement
// would show, because it is the list the reader trusts.
//
// The full list is not shown here. It is a modal under `?`: the bar is one line
// and therefore has to choose what to leave out, and the keys it would leave out
// are the ones a reader opens the help to find.
func (m Model) keyBar(width int) string {
	return m.pal.HelpBar(width, m.keys.shortHints(m.geom.narrow())...)
}
