package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// ---------------------------------------------------------------------------
// text helpers
// ---------------------------------------------------------------------------

// clip shortens a line to a width, aware of the styling escape sequences in it.
//
// The ellipsis is the point: a silently truncated line reads as a complete one,
// and a monitor must not lie about what it is not showing.
func clip(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}

// wrapLine breaks a line to a width, aware of the styling escape sequences in
// it.
//
// Paths and URLs are breakpoints as well as spaces: a journal line about a
// request carries both, and a line that cannot be broken there would be clipped
// instead — losing exactly the part that identifies the request.
func wrapLine(s string, width int) string {
	if width <= 0 {
		return s
	}
	return lipgloss.Wrap(s, width, "/-")
}

// padTo widens a rendered line to exactly width cells.
//
// Measured with lipgloss.Width, which ignores escape sequences: a styled string
// is longer in bytes than in cells, and padding by byte count is how a coloured
// line ends up with a ragged right edge.
func padTo(s string, width int) string {
	gap := width - lipgloss.Width(s)
	if gap <= 0 {
		return s
	}
	return s + repeat(" ", gap)
}

// onOff renders a boolean as a word. Never a symbol alone: the state has to
// survive being read on a terminal without colour.
func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// since renders a moment as an age, relative to now.
//
// A relative age rather than a timestamp: the reader is asking "is this
// happening now", and a wall clock would make them do the subtraction.
func since(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("2006-01-02")
	}
}

// itoa is strconv.Itoa under a name that reads better in a cell list.
// itoa renders a counter.
//
// Generic over the two integer widths the reading uses — the per-key counters are
// int, the per-address ones are int64 — so that a counter is never formatted by
// one helper and clipped by another.
func itoa[T int | int64](v T) string { return strconv.FormatInt(int64(v), 10) }

// ---------------------------------------------------------------------------
// journal line rendering
// ---------------------------------------------------------------------------

// Journal text styles, as indices into a table.
//
// The line is rendered in ONE pass over its runes rather than by wrapping a
// token in styling and gluing it back: every Render call emits its own escape
// sequences, and a line assembled from styled fragments measures correctly but
// is full of resets that break the selection background underneath it. One pass
// means one contiguous run of styled cells per fragment.
const (
	jBase = iota
	jError
	jWarn
	jMuted
	jMatch
)

// journalLine renders one raw journal line: the severity token in its level
// colour, every occurrence of the filter in the match colour, the rest plain.
//
// THE LEVEL IS RECOGNISED, NOT PARSED. The journal's format belongs to the
// logger, and a monitor that insisted on a schema would show nothing at all on
// the day the logger changed. An unrecognised line is shown plain, which is the
// honest outcome.
//
// ONE PASS, ONE STYLE PER RUN. The line is classified rune by rune into a table
// of style indices and then emitted as runs of equal style. Building it by
// concatenating styled fragments instead would put a reset between every two
// cells, and a reset in the middle of a line is what breaks the selection or the
// modal background painted underneath it.
func (m Model) journalLine(raw, needle string) string {
	styles := []lipgloss.Style{
		lipgloss.NewStyle(),
		m.pal.ErrorText,
		m.pal.WarningText,
		m.pal.Dim,
		m.pal.FilterMatch,
	}
	runes := []rune(raw)
	idx := make([]int, len(runes))

	// The severity token, if the line opens with one.
	for _, lv := range []struct {
		word string
		code int
	}{
		{"ERROR", jError}, {"ERR", jError},
		{"ABORT", jWarn}, {"WARN", jWarn},
		{"INFO", jMuted}, {"DEBUG", jMuted},
	} {
		if strings.HasPrefix(raw, lv.word) {
			for i := range len(lv.word) {
				if i < len(idx) {
					idx[i] = lv.code
				}
			}
			break
		}
	}

	// The filter's occurrences, which win over the level colour: the reader
	// asked for exactly these and they must not be the cells that stay quiet.
	if needle != "" {
		lower := []rune(strings.ToLower(raw))
		n := []rune(needle)
		for i := 0; i+len(n) <= len(lower); {
			if equalRunes(lower[i:i+len(n)], n) {
				for j := i; j < i+len(n); j++ {
					idx[j] = jMatch
				}
				i += len(n)
				continue
			}
			i++
		}
	}

	// Emit runs of equal style.
	var b strings.Builder
	for i := 0; i < len(runes); {
		code := idx[i]
		j := i
		for j < len(runes) && idx[j] == code {
			j++
		}
		b.WriteString(styles[code].Render(string(runes[i:j])))
		i = j
	}
	return b.String()
}

// equalRunes compares two equal-length rune slices.
func equalRunes(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// the section frame
// ---------------------------------------------------------------------------

// boxTop builds the top border of a section: the corner, the title, a run of
// dashes, the summary, and the closing corner — exactly width cells.
//
// THE SUMMARY IS DROPPED BEFORE THE TITLE. The title says which section this is,
// and a frame whose sections are indistinguishable is worse than one that has
// stopped reporting its line count. Dropping the title to keep a count would be
// the wrong trade in the same way.
func (m Model) boxTop(title, summary string, width int) string {
	if width < 4 {
		return clip(title, width)
	}

	inner := width - 2
	titleCell := " " + m.pal.accentStyle().Render(title) + " "

	summaryCell := ""
	if summary != "" {
		summaryCell = " " + m.pal.Dim.Render(summary) + " "
	}

	// fill = width - corner(1) - dash(1) - title - fill - summary - corner(1)
	fill := width - 3 - lipgloss.Width(titleCell) - lipgloss.Width(summaryCell)
	if fill < 1 && summaryCell != "" {
		summaryCell = ""
		fill = width - 3 - lipgloss.Width(titleCell)
	}
	if fill < 0 {
		titleCell = clip(titleCell, max(0, inner-1))
		fill = max(0, width-3-lipgloss.Width(titleCell))
	}

	return m.pal.borderColor().Render("╭─") +
		titleCell +
		m.pal.borderColor().Render(repeat("─", fill)) +
		summaryCell +
		m.pal.borderColor().Render("╮")
}

// boxBottom is the closing border of a section.
func (m Model) boxBottom(width int) string {
	if width < 2 {
		return ""
	}
	return m.pal.borderColor().Render("╰" + repeat("─", width-2) + "╯")
}

// boxRow is one content row of a section, clipped and padded to the frame.
func (m Model) boxRow(line string, width int) string {
	inner := width - 4
	if inner < 1 {
		return m.pal.borderColor().Render("│") + clip(line, max(0, width-1))
	}
	return m.pal.borderColor().Render("│") + " " +
		padTo(clip(line, inner), inner) + " " +
		m.pal.borderColor().Render("│")
}

// ---------------------------------------------------------------------------
// sections
// ---------------------------------------------------------------------------

// sectionView is what a section contributes to the frame.
//
// The head is fixed and the body scrolls: a table's column titles that scrolled
// away with the data would leave the reader counting columns, and the journal
// has no head at all. Splitting them here is what lets one scroll offset serve
// both without either knowing which section is on screen.
type sectionView struct {
	// head is drawn above the body and never scrolls.
	head []string
	// body is the scrollable content, one entry per rendered line.
	body []string
}

// sectionOf builds the active section.
//
// The page height is not passed in: a section reports all of its content and the
// frame does the windowing. A section that could only produce a screenful would
// have to know the frame's height, and then the scroll offset and the page would
// be two numbers describing the same view from two places.
func (m Model) sectionOf(inner int) sectionView {
	switch m.Focus {
	case BlockKeys:
		return sectionView{body: m.keysSection(inner)}
	case BlockMatrix:
		return m.addressesSection(inner)
	default:
		return m.journalSection(inner)
	}
}

// tableHeadLines is how many fixed lines a table draws before its first row: the
// column titles and the rule under them.
//
// It is a constant because the scroll handlers need the number without building
// the table. Both table sections construct their head with exactly these two
// entries, and the two are next to each other in this file on purpose: a third
// line added to one of them has to be added here as well, and this is where
// someone will be reading when they do it.
const tableHeadLines = 2

// --- keys -------------------------------------------------------------------

// keysSection renders the providers and their key pools.
//
// ONE CARD PER PROVIDER, separated by a blank line rather than by a rule: a rule
// between every provider on a screen with four of them is more horizontal ink
// than content, and the card's own marker already says where one begins.
func (m Model) keysSection(inner int) []string {
	if len(m.obs.Providers) == 0 {
		return []string{
			m.pal.Dim.Render("no providers are configured"),
			m.pal.Hint.Render("the daemon serves nothing until one is declared"),
		}
	}

	out := make([]string, 0, len(m.obs.Providers)*6)
	for i, p := range m.obs.Providers {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, m.providerCard(p, inner)...)
	}
	return out
}

// providerCard is one provider: its header, its state, and its keys.
func (m Model) providerCard(p ProviderState, inner int) []string {
	marker := m.pal.accentStyle().Render("▌") + " "
	id := m.pal.Value.Bold(true).Render(p.ID)

	// The credential and the pool size sit on the right of the header, because
	// they are the two facts that decide whether the pool is even consulted.
	right := m.pal.Label.Render("credential ") + m.pal.Value.Render(p.Credential)
	right += m.pal.Dim.Render("  ·  ")
	right += m.pal.Label.Render("keys ") + m.pal.Value.Render(itoa(len(p.Keys)))

	head := marker + id
	pad := inner - lipgloss.Width(head) - lipgloss.Width(right)
	if pad < 2 {
		return []string{head, "  " + right, m.rotationLine(p)}
	}

	out := []string{
		head + repeat(" ", pad) + right,
		m.rotationLine(p),
	}

	if len(p.Keys) == 0 {
		// An anonymous provider has no pool by design, and an account provider
		// without keys could not have started. Both are stated rather than left
		// as an empty gap, because an empty gap reads as a rendering failure.
		if p.Credential == "anonymous" {
			out = append(out, m.pal.Dim.Render("  no keys — an anonymous credential presents none"))
		} else {
			out = append(out, m.pal.WarningText.Render("  no keys in the pool"))
		}
		return out
	}

	// The key rows, pinned to the columns the counters and the bar sit in.
	// The header names each column over the cells that carry it, the bar column
	// included: a title that stopped before the bar would sit on top of the last
	// value in the row instead of over its own.
	okW, errW, barW, shareW := 7, 7, 14, 5
	head0 := "  " + m.pal.Label.Render(padTo("key", 14))
	head0 += m.pal.Label.Render(padTo("OK", okW))
	head0 += m.pal.Label.Render(padTo("REFUSED", errW))
	head0 += m.pal.Label.Render(padTo("", barW))
	head0 += m.pal.Label.Render(padTo("share", shareW))
	out = append(out, head0)

	for _, k := range p.Keys {
		out = append(out, m.keyRow(k, k.Fingerprint == p.Active, okW, errW, barW, shareW))
	}
	return out
}

// rotationLine states whether rotation is on and, when it is, which key is in
// use. Rotation off is said out loud: it is the difference between a pool that
// spreads load and a pool that is decoration.
func (m Model) rotationLine(p ProviderState) string {
	state := m.pal.Dim.Render("rotate " + onOff(p.RotationOn))
	if !p.RotationOn {
		state = m.pal.WarningText.Render("rotate off")
	}
	if p.Credential == "anonymous" {
		state = m.pal.Dim.Render("rotate n/a — anonymous credential")
	}
	return "  " + state
}

// keyRow is one key: the active marker, its fingerprint, its counters and its
// share of successful requests.
//
// The active key is marked twice — the arrow in the margin and the fingerprint in
// the accent — because it is the one value in this section that decides where the
// traffic goes. The arrow says which row, the colour says it again for a reader
// who is looking at the pool rather than reading it.
func (m Model) keyRow(k KeyState, active bool, okW, errW, barW, shareW int) string {
	marker := "  "
	fpStyle := m.pal.Value
	if active {
		marker = m.pal.accentStyle().Render("▸") + " "
		fpStyle = m.pal.accentStyle().Bold(true)
	}

	fp := padTo(fpStyle.Render(clip(k.Fingerprint, 14)), 14)
	ok := padTo(m.pal.SuccessText.Render(itoa(k.OK)), okW)
	err := padTo(m.pal.ErrorText.Render(itoa(k.Refused)), errW)

	total := k.OK + k.Refused
	frac := 0.0
	if total > 0 {
		frac = float64(k.OK) / float64(total)
	}
	bar := m.barCell(frac, barW)

	share := m.pal.Dim.Render(fmt.Sprintf("%*.0f%%", shareW-1, frac*100))
	if total == 0 {
		share = m.pal.Dim.Render(padLeft("—", shareW))
	}

	return marker + fp + ok + err + bar + share
}

// barCell is a share bar: filled cells in a tone that follows the ratio.
//
// The tone is not the signal — the percentage beside it is — but a pool whose
// keys mostly get refused should be visible from across the room, and the bar is
// the only element on the line that can carry that at a glance.
func (m Model) barCell(frac float64, width int) string {
	if width < 2 {
		return ""
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac*float64(width) + 0.5)
	if filled > width {
		filled = width
	}

	c := m.pal.Success
	switch {
	case frac < 0.5:
		c = m.pal.Danger
	case frac < 0.9:
		c = m.pal.Warning
	}

	return lipgloss.NewStyle().Foreground(c).Render(repeat("█", filled)) +
		m.pal.Dim.Render(repeat("░", width-filled))
}

// --- addresses --------------------------------------------------------------

// tableCol is one column of the address table.
type tableCol struct {
	title string
	w     int
	right bool
}

// colGap is the space between two columns of the address table.
const colGap = 2

// addressesSection renders the per-address, per-model matrix.
func (m Model) addressesSection(inner int) sectionView {
	rows := m.sortedAddresses()
	if len(rows) == 0 {
		return sectionView{body: []string{
			m.pal.Dim.Render("no address has served a request yet"),
			m.pal.Hint.Render("the matrix fills in as the daemon forwards traffic"),
		}}
	}

	cols := addressCols(rows, inner)
	head := []string{
		renderCells(colTitles(cols), cols),
		m.pal.Rule.Render(repeat("─", min(inner, colsWidth(cols)))),
	}

	body := make([]string, 0, len(rows)*3)
	for i, r := range rows {
		if i > 0 {
			// A rule between address groups rather than a zebra stripe: the rows
			// nest one level, and a nested zebra reads as a rendering fault.
			body = append(body, m.pal.Rule.Render(repeat("·", min(inner, colsWidth(cols)))))
		}
		body = append(body, m.addressRow(r, cols))
		for _, mr := range r.Models {
			body = append(body, m.modelRow(r, mr, cols))
		}
	}
	return sectionView{head: head, body: body}
}

// sortedAddresses returns the matrix rows in the order the sort mode asks for.
//
// A COPY IS SORTED, never the reading itself: the observation is the daemon's
// answer and the monitor does not edit what it was told. The sort is stable, so
// rows that compare equal stay in the order the daemon reported them — without
// that, a redraw could shuffle two rows with identical counters and the table
// would flicker between two orderings of the same data.
func (m Model) sortedAddresses() []AddressRow {
	rows := make([]AddressRow, len(m.obs.Addresses))
	copy(rows, m.obs.Addresses)

	switch m.sort {
	case sortErrors:
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Error > rows[j].Error })
	case sortRecent:
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].LastSeen.After(rows[j].LastSeen) })
	case sortAddress:
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Address < rows[j].Address })
	default:
		sort.SliceStable(rows, func(i, j int) bool { return requests(rows[i]) > requests(rows[j]) })
	}
	return rows
}

// requests is how many requests an address carried, across all outcomes.
func requests(r AddressRow) int64 { return r.OK + r.Error + r.Aborted }

// addressCols computes the table's columns for the available width.
//
// COLUMNS DROP FROM THE RIGHT as the terminal narrows, and the order is by how
// much they are needed: ABORT (zero for almost every deployment), then LAST,
// then FAMILY. The address and the two counters always survive — they are the
// reason the section exists.
func addressCols(rows []AddressRow, inner int) []tableCol {
	aborts := anyAborted(rows)
	wide := inner >= 56

	cols := []tableCol{{title: "ADDRESS"}}
	if wide {
		cols = append(cols, tableCol{title: "FAM", w: 3})
	}
	cols = append(cols,
		tableCol{title: "OK", w: 5, right: true},
		tableCol{title: "ERR", w: 5, right: true},
	)
	if aborts && wide {
		cols = append(cols, tableCol{title: "ABT", w: 5, right: true})
	}
	if wide {
		cols = append(cols, tableCol{title: "LAST", w: 9})
	}

	// The address takes what is left, but never less than it takes to tell two
	// addresses apart — at that point the optional columns go instead.
	for {
		fixed := colGap * (len(cols) - 1)
		for i := 1; i < len(cols); i++ {
			fixed += cols[i].w
		}
		if rest := inner - fixed; rest >= 14 || len(cols) <= 3 {
			cols[0].w = max(10, rest)
			break
		}
		// Drop the last optional column, which is the rightmost by construction.
		cols = cols[:len(cols)-1]
	}
	return cols
}

// colsWidth is the total width the columns and their gaps occupy.
func colsWidth(cols []tableCol) int {
	total := colGap * (len(cols) - 1)
	for _, c := range cols {
		total += c.w
	}
	return total
}

// colTitles is the header line's cells.
func colTitles(cols []tableCol) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.title
	}
	return out
}

// renderCells lays cells out in the columns.
func renderCells(cells []string, cols []tableCol) string {
	parts := make([]string, 0, len(cols))
	for i, c := range cols {
		cell := ""
		if i < len(cells) {
			cell = cells[i]
		}
		if c.right {
			cell = padLeft(cell, c.w)
		} else {
			cell = padTo(clip(cell, c.w), c.w)
		}
		parts = append(parts, cell)
	}
	return strings.TrimRight(strings.Join(parts, repeat(" ", colGap)), " ")
}

// padLeft right-aligns a rendered cell within width cells.
func padLeft(s string, width int) string {
	gap := width - lipgloss.Width(s)
	if gap <= 0 {
		return s
	}
	return repeat(" ", gap) + s
}

// addressRow is one address: its name, family, counters and age.
func (m Model) addressRow(r AddressRow, cols []tableCol) string {
	cells := make([]string, 0, len(cols))
	for _, c := range cols {
		switch c.title {
		case "ADDRESS":
			cells = append(cells, m.pal.Value.Bold(true).Render(clip(r.Address, c.w)))
		case "FAM":
			cells = append(cells, m.pal.Dim.Render(r.Family))
		case "OK":
			cells = append(cells, m.pal.SuccessText.Render(itoa(r.OK)))
		case "ERR":
			cells = append(cells, m.pal.ErrorText.Render(itoa(r.Error)))
		case "ABT":
			cells = append(cells, m.pal.WarningText.Render(itoa(r.Aborted)))
		case "LAST":
			cells = append(cells, m.pal.Dim.Render(clip(since(r.LastSeen), c.w)))
		}
	}
	return renderCells(cells, cols)
}

// modelRow is one model under its address.
//
// The model is indented into the address column rather than given a column of
// its own: it hangs under its address, and a separate column would put it on the
// same level as one — losing the only hierarchy the matrix has.
func (m Model) modelRow(addr AddressRow, mr ModelRow, cols []tableCol) string {
	cells := make([]string, 0, len(cols))
	for _, c := range cols {
		switch c.title {
		case "ADDRESS":
			cells = append(cells, m.pal.Dim.Render(clip("  "+mr.Model, c.w)))
		case "FAM":
			cells = append(cells, "")
		case "OK":
			cells = append(cells, m.pal.SuccessText.Render(itoa(mr.OK)))
		case "ERR":
			cells = append(cells, m.pal.ErrorText.Render(itoa(mr.Error)))
		case "ABT":
			cells = append(cells, m.pal.WarningText.Render(itoa(mr.Aborted)))
		case "LAST":
			cells = append(cells, "")
		}
	}
	return renderCells(cells, cols)
}

// --- journal ----------------------------------------------------------------

// journalRow is one rendered line of the journal, with the entry it belongs to.
//
// The two indices are kept so that a click and the selection marker can address
// an ENTRY rather than a line: with wrapping on, one entry is several lines, and
// a selection that addressed lines would mark a wrapped continuation as a
// separate event. entry is the position in the filtered journal — what the
// selection and the details key refer to — and index is the position in the
// unfiltered reading, which is what the details modal reads around.
type journalRow struct {
	text  string
	index int
	entry int
}

// journalLayout renders the journal into scrollable lines.
//
// It is a pure function of the model, called by the frame, by the selection's
// reveal and by the click's hit test — so the three cannot disagree about how
// tall an entry is, which is exactly the bug that makes a selection scroll to the
// wrong place or a click open the line above the one under the cursor.
func (m Model) journalLayout(inner int) []journalRow {
	out := make([]journalRow, 0, len(m.filtered))
	needle := strings.ToLower(m.filter)

	for i, entry := range m.filtered {
		selected := i == m.selected

		// The filter is applied to the entry text, so the highlight and the
		// wrapping see the same string.
		body := m.journalLine(entry.text, needle)

		var lines []string
		if m.Wrap {
			lines = strings.Split(wrapLine(body, inner), "\n")
		} else {
			lines = []string{clip(body, inner)}
		}
		for j, line := range lines {
			text := "  " + line
			// Only the first line of a wrapped entry carries the marker: a marker
			// on every continuation would read as several selected entries rather
			// than one.
			if selected && j == 0 {
				text = m.pal.accentStyle().Render("▌") + " " + line
			}
			out = append(out, journalRow{text: text, index: entry.index, entry: i})
		}
	}
	return out
}

// journalSection is the journal, filtered.
//
// No fixed head: a log line carries its own timestamp and level, so a row of
// column titles above it would be a second, weaker copy of what is already in
// every line.
func (m Model) journalSection(inner int) sectionView {
	if len(m.obs.Logs) == 0 {
		return sectionView{body: []string{
			m.pal.Dim.Render("the journal is empty"),
			m.pal.Hint.Render("the daemon writes to " + m.obs.LogPath),
		}}
	}
	if len(m.filtered) == 0 {
		return sectionView{body: []string{
			m.pal.Dim.Render("nothing matches " + m.filter),
			m.pal.Hint.Render("esc clears the filter"),
		}}
	}

	rows := m.journalLayout(inner)
	body := make([]string, 0, len(rows))
	for _, r := range rows {
		body = append(body, r.text)
	}
	return sectionView{body: body}
}

// journalSpan is the range of rendered lines one filtered entry occupies.
//
// With wrapping on an entry is one line or several, and both the reveal and the
// selection need to know which: an entry above the selection that wrapped is what
// makes the selection drift down the page on every move.
func (m Model) journalSpan(entry, inner int) (start, end int, ok bool) {
	start, end = -1, -1
	line := 0
	for _, row := range m.journalLayout(inner) {
		if row.entry == entry {
			if start < 0 {
				start = line
			}
			end = line
		}
		line++
	}
	if start < 0 {
		return 0, 0, false
	}
	return start, end, true
}

// journalEntryAtLine is the filtered entry drawn on a rendered line, and whether
// the line is inside an entry at all.
//
// The last rendered line of an entry and its continuation lines both answer with
// the same entry: the reader clicks the text they can see, and a wrapped tail is
// part of the entry it wrapped from.
func (m Model) journalEntryAtLine(line, inner int) (int, bool) {
	if line < 0 {
		return -1, false
	}
	rows := m.journalLayout(inner)
	if line >= len(rows) {
		return -1, false
	}
	return rows[line].entry, true
}

// anyAborted reports whether any row carries a cancelled request.
//
// The ABORT column is only worth its width when something was actually
// cancelled: it is rare, and a column of zeroes on every screen costs the
// address column the cells it needs to stay legible.
func anyAborted(rows []AddressRow) bool {
	for _, r := range rows {
		if r.Aborted > 0 {
			return true
		}
		for _, mr := range r.Models {
			if mr.Aborted > 0 {
				return true
			}
		}
	}
	return false
}
