package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// Modal size limits.
const (
	// modalMaxWidth is the widest a modal grows. Past this the eye has to travel
	// too far from the line it opened on to the answer it was asking for.
	modalMaxWidth = 88
	// modalMargin is the gap kept between a modal and the terminal's edge.
	modalMargin = 4
	// modalContext is how many journal lines are shown around the opened one.
	modalContext = 3
	// modalLabelWidth is the column the key list aligns its descriptions to. The
	// widest key the monitor declares is `⇧tab h ←`, which is eight cells, so this
	// is a gutter and not a fit: it is what makes two columns of keys possible
	// inside one modal.
	modalLabelWidth = 14
)

// modalWidth is the modal's width for the current terminal.
func (m Model) modalWidth() int {
	return max(20, min(modalMaxWidth, m.geom.Width-modalMargin))
}

// modalRoom is how many lines a modal may occupy.
func (m Model) modalRoom() int {
	return max(4, m.geom.Height-modalMargin)
}

// modal renders the open overlay, or the empty string when none is open.
func (m Model) modal() string {
	if m.overlay == nil {
		return ""
	}
	switch m.overlay.kind {
	case overlayAxes:
		return m.axesModal()
	case overlayHelp:
		return m.helpModal()
	default:
		return m.detailsModal()
	}
}

// modalBox wraps content in the opaque modal frame.
//
// The lines are padded to the content width before they are handed to the style:
// the frame's background is what makes the modal opaque, and a line that stopped
// short would leave the frame underneath showing through.
func (m Model) modalBox(title, meta string, lines []string) string {
	st := m.pal.Modal
	w := m.modalWidth()
	inner := max(1, w-st.GetHorizontalFrameSize())

	head := m.pal.accentStyle().Render(title)
	if meta != "" {
		tail := m.pal.Dim.Render(meta)
		if gap := inner - lipgloss.Width(head) - lipgloss.Width(tail); gap > 0 {
			head += repeat(" ", gap) + tail
		}
	}

	// The room left for content once the frame, the heading and its rule are
	// paid for. Overflow is announced rather than dropped: a modal that quietly
	// stopped at the bottom would read as the whole list.
	room := m.modalRoom() - st.GetVerticalFrameSize() - 2
	if room < 1 {
		room = 1
	}
	if len(lines) > room {
		kept := max(0, room-1)
		elided := len(lines) - kept
		lines = append(lines[:kept], m.pal.Dim.Render(fmt.Sprintf("… %d more lines", elided)))
	}

	body := make([]string, 0, len(lines)+2)
	body = append(body, head, m.pal.Rule.Render(repeat("─", inner)))
	for _, l := range lines {
		body = append(body, padTo(clip(l, inner), inner))
	}
	return st.Width(w).Render(strings.Join(body, "\n"))
}

// detailsModal shows the selected journal line in full, with its neighbours.
//
// CONTEXT, NOT JUST THE LINE. A single journal line rarely says what happened —
// the one above carries the request and the one below carries the outcome. The
// reader stopped here for a reason, and the modal shows what they stopped in.
// This is also the only place a line is read whole: the journal clips and wraps
// to fit its section, which is right for scanning and wrong for the one line the
// reader is studying.
func (m Model) detailsModal() string {
	logs := m.obs.Logs
	if len(logs) == 0 {
		return m.modalBox("journal", "", []string{m.pal.Dim.Render("the journal is empty")})
	}

	idx := m.overlay.index
	if idx < 0 || idx >= len(logs) {
		return m.modalBox("journal", "", []string{m.pal.Dim.Render("the line is no longer in the tail")})
	}

	inner := max(1, m.modalWidth()-m.pal.Modal.GetHorizontalFrameSize())
	needle := strings.ToLower(m.filter)

	from := max(0, idx-modalContext)
	to := min(len(logs), idx+modalContext+1)

	lines := make([]string, 0, to-from)
	for i := from; i < to; i++ {
		body := m.journalLine(logs[i], needle)
		for _, part := range strings.Split(wrapLine(body, inner-2), "\n") {
			if i == idx {
				lines = append(lines, m.pal.accentStyle().Render("▌ ")+part)
				continue
			}
			lines = append(lines, "  "+m.pal.Dim.Render(part))
		}
	}

	return m.modalBox("journal", fmt.Sprintf("line %d of %d", idx+1, len(logs)), lines)
}

// axesModal lists every axis of the deployment with where its value came from.
//
// THIS IS WHAT REPLACED THE AXES PANEL. The panel sat above the body on every
// screen and cost two or three rows of the journal to answer a question that is
// asked rarely; the full answer lives here, one key away, while the three axes
// that decide where traffic goes stayed on the status line where they are
// actually watched.
//
// The source is shown because it is the question that follows the value: an axis
// set by a flag overrides the environment, which overrides the file, and a
// reader who cannot see which one won cannot answer "why is it like this".
func (m Model) axesModal() string {
	if len(m.obs.Daemon) == 0 {
		return m.modalBox("axes", "", []string{m.pal.Dim.Render("the daemon reported no axes")})
	}

	nameW, valueW := 0, 0
	for _, r := range m.obs.Daemon {
		nameW = max(nameW, lipgloss.Width(r.Name))
		valueW = max(valueW, lipgloss.Width(r.Value))
	}

	lines := make([]string, 0, len(m.obs.Daemon))
	for _, r := range m.obs.Daemon {
		lines = append(lines,
			padTo(m.pal.Label.Render(r.Name), nameW)+
				"  "+padTo(m.pal.Value.Render(r.Value), valueW)+
				"  "+m.pal.Dim.Render("("+r.Source+")"))
	}

	return m.modalBox("axes", "i closes", lines)
}

// helpModal lists every binding the monitor answers to.
//
// The full list lives here rather than in the key bar. The bar is one line and
// therefore has to choose, and the keys it would leave out are exactly the ones
// a reader opens the help to find.
//
// THE READ-ONLY PROMISE IS IN THE HEADING, not in a line at the bottom. It is the
// one thing a reader must not have to scroll for — "can this program be made to
// change my daemon" is the question they opened the help with — and a line at the
// bottom is the first thing dropped when the list runs past the terminal.
//
// TWO COLUMNS ON A WIDE MODAL. The list grows with the keys, and this is the one
// place every one of them has to be visible at once: a help that runs past the
// bottom is elided, and an elided help answers "there is a key for that,
// somewhere". The groups are split in their own order — first half down the left
// column, second half down the right — which keeps every group together and
// halves the height. A modal too narrow to hold two columns keeps the single
// column and the elision note.
func (m Model) helpModal() string {
	groups := m.keys.groups()
	inner := max(1, m.modalWidth()-m.pal.Modal.GetHorizontalFrameSize())
	meta := "read-only · ? closes"

	if split, ok := m.helpSplit(groups, inner); ok {
		return m.modalBox("keys", meta, joinHelpColumns(
			m.helpColumn(groups[:split]), m.helpColumn(groups[split:])))
	}
	return m.modalBox("keys", meta, m.helpColumn(groups))
}

// helpTwoColumns is the inner width below which the list is never split: two
// columns need at least the width of a key description each.
const helpTwoColumns = 56

// helpColumnGap separates the two columns of the help.
const helpColumnGap = 3

// helpSplit is where the groups divide in two, and whether any split fits.
//
// EVERY SPLIT IS TRIED, because the columns' widths come from the length of the
// descriptions and only laying them out says whether two of them fit side by
// side. Among the splits that fit, the one with the shortest taller column wins:
// the height is what the reader pays for, and an uneven pair wastes room the
// longest description may need next month.
func (m Model) helpSplit(groups []keyGroup, inner int) (int, bool) {
	if inner < helpTwoColumns {
		return 0, false
	}

	best, bestHeight := 0, 0
	for split := 1; split < len(groups); split++ {
		left, right := m.helpColumn(groups[:split]), m.helpColumn(groups[split:])
		if joinHelpWidth(left, right) > inner {
			continue
		}
		if h := max(len(left), len(right)); best == 0 || h < bestHeight {
			best, bestHeight = split, h
		}
	}
	return best, best != 0
}

// helpColumn renders a run of groups, each titled, as lines.
func (m Model) helpColumn(groups []keyGroup) []string {
	lines := make([]string, 0, len(groups)*4)
	for i, g := range groups {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, m.pal.SectionTitle.Render(g.name))
		for _, b := range g.bindings {
			lines = append(lines, "  "+
				padTo(m.pal.Key.Render(b.Help().Key), modalLabelWidth)+
				m.pal.Value.Render(b.Help().Desc))
		}
	}
	return lines
}

// joinHelpColumns puts two runs of lines side by side.
//
// The left column is padded to its own widest line and the shorter run with empty
// lines, so the rows of the two columns stay aligned with each other: a column
// that drifted by a row would read as if the group opposite it were its parent.
func joinHelpColumns(left, right []string) []string {
	width := helpColumnGap + columnWidth(left)

	out := make([]string, 0, max(len(left), len(right)))
	for i := range max(len(left), len(right)) {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out = append(out, padTo(l, width)+r)
	}
	return out
}

// joinHelpWidth is how wide the joined columns would be.
func joinHelpWidth(left, right []string) int {
	return columnWidth(left) + helpColumnGap + columnWidth(right)
}

// columnWidth is the widest line of a column.
func columnWidth(lines []string) int {
	width := 0
	for _, l := range lines {
		width = max(width, lipgloss.Width(l))
	}
	return width
}
