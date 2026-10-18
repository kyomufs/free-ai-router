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
	// modalLabelWidth is the column the key list aligns its descriptions to.
	modalLabelWidth = 16
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
func (m Model) helpModal() string {
	groups := m.keys.groups()
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

	return m.modalBox("keys", "read-only · ? closes", lines)
}
