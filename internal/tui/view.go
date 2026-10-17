package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// layout constants.
//
// The blocks have a stable size for a given terminal, and the size does not
// depend on how much data arrived: a layout that grows and shrinks on every
// reading moves the lines the reader is looking at. What does not fit is
// scrolled to, not squeezed in.
const (
	// keysMin is the height of the key block, borders included.
	keysMin = 5
	// keysMax is the height of the key block when there are many providers.
	keysMax = 9
	// matrixMin is the height of the matrix, borders included.
	matrixMin = 6
	// matrixMax is the height of the matrix when there are many addresses.
	matrixMax = 15
	// logsMin is the height of the log block, borders included.
	logsMin = 5
	// gutter is the indent one level of the matrix takes.
	gutter = 2
	// chromeBase is the number of lines the frame spends outside the three
	// blocks before the footer grows.
	//
	// The frame is assembled as: header (TWO lines — the title and the axes in
	// force), the key block, the matrix, the log block, then the footer. The
	// newlines between them merely end a line a block already claimed, so the
	// only lines nobody's block accounts for are the two of the header and the
	// one the collapsed help bar takes.
	//
	// THE FOOTER IS NOT CONSTANT, and that is why this is a base rather than the
	// whole story: `?` expands the bar, and a frame whose chrome was computed for
	// the collapsed bar then overruns the screen by exactly the lines it gained.
	// The remainder is taken from the log block instead — see layout.
	chromeBase = 3
)

// palette holds the styles in use.
//
// Colours are chosen for both backgrounds and stay calm: this is a tool, not a
// shop window. Nothing is signalled by colour alone — every state that colour
// distinguishes also carries a word, because a monitor read over a screenshot or
// a monochrome terminal must not lose its meaning.
type palette struct {
	accent lipgloss.Style
	dim    lipgloss.Style
	ok     lipgloss.Style
	err    lipgloss.Style
	warn   lipgloss.Style
	plain  lipgloss.Style
}

// newPalette builds the styles for a background.
func newPalette(dark bool) palette {
	pick := lipgloss.LightDark(dark)
	accent := pick(lipgloss.Color("#0B5FFF"), lipgloss.Color("#7AA2F7"))
	ok := pick(lipgloss.Color("#1A7F37"), lipgloss.Color("#9ECE6A"))
	err := pick(lipgloss.Color("#B42318"), lipgloss.Color("#F7768E"))
	warn := pick(lipgloss.Color("#9A6700"), lipgloss.Color("#E0AF68"))
	dim := pick(lipgloss.Color("#6E7781"), lipgloss.Color("#565F89"))

	return palette{
		accent: lipgloss.NewStyle().Bold(true).Foreground(accent),
		dim:    lipgloss.NewStyle().Foreground(dim),
		ok:     lipgloss.NewStyle().Foreground(ok),
		err:    lipgloss.NewStyle().Foreground(err),
		warn:   lipgloss.NewStyle().Foreground(warn),
		plain:  lipgloss.NewStyle(),
	}
}

// borderColor is the frame colour of an inactive block.
func (p palette) borderColor() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(p.dim.GetForeground())
}

// View implements tea.Model.
//
// It returns a tea.View rather than a string: in v2 the terminal's own state —
// the alternate screen, the title, the mouse mode — is part of the view, and the
// monitor now sets two of them.
//
// THE ALTERNATE SCREEN IS ENTERED UNLESS Scrollback IS SET. Full screen is what
// makes the monitor an application rather than a printout: the blocks keep their
// shape as readings arrive, nothing scrolls the frame out from under the cursor,
// and the terminal is restored on exit. --scrollback is the other half of the
// choice, for a run whose output is meant to be read afterwards.
//
// The title is set because a full-screen application that does not name itself
// leaves the operator guessing which window this is. It costs nothing and it is
// restored on exit along with the screen.
func (m Model) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}

	width := m.width
	if width <= 0 {
		width = 80
	}

	var b strings.Builder
	b.WriteString(m.header(width))
	b.WriteString("\n")

	for _, blk := range []struct {
		block  Block
		height int
	}{
		{BlockKeys, m.blockHeight(BlockKeys)},
		{BlockMatrix, m.blockHeight(BlockMatrix)},
		{BlockLogs, m.blockHeight(BlockLogs)},
	} {
		b.WriteString(m.renderBlock(blk.block, width, blk.height))
		b.WriteString("\n")
	}

	b.WriteString(m.footer(width))

	view := tea.NewView(b.String())
	view.AltScreen = !m.Scrollback
	view.WindowTitle = "free-ai-router · monitor"
	return view
}

// header draws the title and the axes in effect.
func (m Model) header(width int) string {
	var b strings.Builder
	b.WriteString(m.pal.accent.Render("free-ai-router"))
	b.WriteString("  ")
	b.WriteString(m.pal.dim.Render("monitor · read-only"))

	switch {
	case m.err != nil:
		b.WriteString("\n")
		b.WriteString(m.pal.err.Render(clip("read failed: "+m.err.Error(), width)))
	case len(m.obs.Daemon) > 0:
		b.WriteString("\n")
		b.WriteString(m.axesLine(width))
	case m.loaded:
		b.WriteString("\n")
		b.WriteString(m.pal.dim.Render("read " + m.obs.At.Format("15:04:05")))
	}
	return b.String()
}

// axesLine renders the daemon axes as one clipped line.
func (m Model) axesLine(width int) string {
	parts := make([]string, 0, len(m.obs.Daemon))
	for _, row := range m.obs.Daemon {
		part := row.Name + " " + m.pal.plain.Render(row.Value)
		// The origin of every value is shown: on a managed host the file wins and
		// a flag lasts one run, and the difference is exactly what a reader
		// wondering "why is this not what I set" needs.
		if row.Source != "" {
			part += m.pal.dim.Render("(" + row.Source + ")")
		}
		parts = append(parts, part)
	}
	return clip(strings.Join(parts, m.pal.dim.Render(" · ")), width)
}

// footer draws the key hints and the notes.
//
// THE HINTS ARE A HELP BAR, NOT A SENTENCE. The previous version printed one long
// line of "key · key · key", which reads as a wall and loses its pairings the
// moment the terminal narrows. bubbles/help renders the bindings as `key desc`
// cells with a separator and drops what does not fit instead of truncating
// mid-word, which is why it is used here rather than a longer literal.
func (m Model) footer(width int) string {
	var b strings.Builder
	// The help bar is clipped like every other line of the frame.
	//
	// bubbles/help drops cells that do not fit, but it curries the last one it
	// printed and a narrow terminal still ends up with a line over the edge —
	// which on the alternate screen wraps and pushes a block out of the frame.
	// Clipping here is what keeps the promise the rest of the frame already
	// makes: no line is wider than the terminal.
	b.WriteString(clip(m.help.View(m.keys), width))

	for _, note := range m.obs.Notes {
		b.WriteString("\n")
		b.WriteString(m.pal.warn.Render(clip("note: "+note, width)))
	}
	return b.String()
}

// keyMap declares the monitor's bindings for the help bar.
//
// The keys are NAMED here and ACTED ON in update.go, and the help bar is what
// keeps the two honest: a binding added to one and forgotten in the other is
// visible the moment the bar is read, instead of being discoverable only by
// pressing keys that do nothing.
type keyMap struct {
	Block   key.Binding
	Scroll  key.Binding
	Page    key.Binding
	Ends    key.Binding
	Filter  key.Binding
	Wrap    key.Binding
	Follow  key.Binding
	Refresh key.Binding
	Full    key.Binding
	Quit    key.Binding
}

// ShortHelp implements help.KeyMap: the line that always fits.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Block, k.Scroll, k.Filter, k.Wrap, k.Follow, k.Refresh, k.Quit}
}

// FullHelp implements help.KeyMap: what `?` reveals.
//
// Grouped by what the keys touch rather than listed flat — movement, the log
// block's own switches, then the ways out — because that is the order a reader
// needs them in when the bar has grown past one line.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Block, k.Scroll, k.Page, k.Ends},
		{k.Filter, k.Wrap, k.Follow, k.Refresh},
		{k.Full, k.Quit},
	}
}

// newKeyMap builds the bindings.
func newKeyMap() keyMap {
	return keyMap{
		Block:   key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "block")),
		Scroll:  key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "scroll")),
		Page:    key.NewBinding(key.WithKeys("pgup", "pgdown"), key.WithHelp("pgup/pgdn", "page")),
		Ends:    key.NewBinding(key.WithKeys("home", "end"), key.WithHelp("home/end", "ends")),
		Filter:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Wrap:    key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "wrap")),
		Follow:  key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "follow")),
		Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Full:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "more keys")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// chrome returns the number of frame lines the three blocks do not account for.
//
// It is measured rather than assumed, because the footer is the one part of the
// frame whose height changes while the monitor runs: `?` expands the help bar
// from one line to four, and every other part of the frame has to give those
// lines back. The measurement uses the real width, since the width decides how
// many help cells fit and therefore how many lines the bar takes.
//
// Measuring is cheap here — the footer is a handful of styled strings — and it
// is the only way to be right for both states without a second constant to keep
// in step.
func (m Model) chrome() int {
	width := m.width
	if width <= 0 {
		width = 80
	}
	return (chromeBase - 1) + lipgloss.Height(m.footer(width))
}

// blockHeight returns the height of a block, borders included.
func (m Model) blockHeight(b Block) int {
	keysH, matrixH, logsH := m.layout()
	switch b {
	case BlockKeys:
		return keysH
	case BlockMatrix:
		return matrixH
	default:
		return logsH
	}
}

// layout returns the height of each block, borders included.
//
// The log block gets what is left, because it is the one block whose useful size
// cannot be known in advance: the other two are as tall as their content, and a
// fixed share of the screen would be wrong for almost every deployment.
func (m Model) layout() (keysH, matrixH, logsH int) {
	available := m.height - m.chrome()
	if available < keysMin+matrixMin+logsMin {
		// Too short for three blocks. The minimums are kept and the terminal
		// scrolls: a monitor that dropped blocks to fit would hide data without
		// saying so.
		return keysMin, matrixMin, logsMin
	}

	keysH = keysMin + 2*len(m.obs.Providers)
	if keysH > keysMax {
		keysH = keysMax
	}

	matrixH = matrixMin + m.matrixContentLines()
	if matrixH > matrixMax {
		matrixH = matrixMax
	}

	logsH = available - keysH - matrixH
	if logsH < logsMin {
		// The difference comes out of the matrix first: it scrolls, and it is the
		// block that is least useful when it is half drawn.
		take := min(logsMin-logsH, matrixH-matrixMin)
		matrixH -= take
		logsH += take
	}
	if logsH < logsMin {
		take := min(logsMin-logsH, keysH-keysMin)
		keysH -= take
		logsH += take
	}

	// THE LOG BLOCK TAKES THE REST, and this line is what makes the frame exactly
	// as tall as the terminal.
	//
	// The block-height arithmetic is mostly subtraction, but the two capped
	// blocks round their own sizes down to a maximum, and those reductions are
	// invisible here: without this, the leftover lines simply vanish and a
	// full-screen monitor ends with a band of empty terminal under the footer.
	//
	// In the scrollback mode this monitor used to run in, that band was harmless
	// — the next prompt was one line further down. On the alternate screen it is
	// the whole difference between an application and a printout, so the
	// remainder is claimed by the block that can always use it.
	logsH += available - (keysH + matrixH + logsH)

	return keysH, matrixH, logsH
}

// matrixContentLines is how many lines the matrix wants beyond its border.
func (m Model) matrixContentLines() int {
	n := 0
	for _, row := range m.obs.Addresses {
		n++ // the address line itself
		n += len(row.Models)
	}
	if n == 0 {
		return 2 // the empty-state text and its explanation
	}
	return n + 2 // the column header and the total
}

// contentWidth is the width available to text inside a block, borders and
// horizontal padding excluded.
func (m Model) contentWidth(width int) int {
	inner := width - 4
	if inner < 12 {
		inner = 12
	}
	return inner
}

// pageSize is how many lines a block shows, borders excluded.
func (m Model) pageSize(b Block) int {
	return max(1, m.blockHeight(b)-2)
}

// lineCount is how many lines a block has.
func (m Model) lineCount(b Block) int {
	return len(m.content(b, m.contentWidth(m.widthOrDefault())))
}

// widthOrDefault is the terminal width, with a sane fallback for the moment
// before the first size message.
func (m Model) widthOrDefault() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}

// content returns the lines of a block, already fitted to the given width.
//
// One function for both drawing and measuring: two would be two places to keep
// in step, and a mismatch shows up as a scroll that stops before the end.
func (m Model) content(b Block, width int) []string {
	switch b {
	case BlockKeys:
		return m.keyLines()
	case BlockMatrix:
		return m.matrixLines(width)
	default:
		return m.logLines(width)
	}
}

// renderBlock draws one block with its frame and title.
func (m Model) renderBlock(b Block, width, height int) string {
	bodyH := max(1, height-2)
	lines := m.content(b, m.contentWidth(width))

	from := min(m.Scroll[b], len(lines))
	to := min(from+bodyH, len(lines))

	frame := m.pal.borderColor()
	if b == m.Focus {
		frame = lipgloss.NewStyle().Foreground(m.pal.accent.GetForeground())
	}

	title := m.pal.dim.Render(b.String())
	if b == m.Focus {
		title = m.pal.accent.Render(b.String())
	}

	brd := lipgloss.RoundedBorder()
	var out strings.Builder
	out.WriteString(m.topBorder(brd, title, width, frame))
	out.WriteString("\n")

	body := make([]string, 0, bodyH)
	for _, line := range lines[from:to] {
		body = append(body, line)
	}
	for len(body) < bodyH {
		body = append(body, "")
	}

	inner := m.contentWidth(width)
	bar := frame.Render(brd.Left)
	for i, line := range body {
		if i > 0 {
			out.WriteString("\n")
		}
		out.WriteString(bar)
		out.WriteString(" ")
		out.WriteString(pad(clip(line, inner), inner))
		out.WriteString(" ")
		out.WriteString(bar)
	}
	out.WriteString("\n")
	out.WriteString(frame.Render(brd.BottomLeft + strings.Repeat(brd.Bottom, width-2) + brd.BottomRight))
	return out.String()
}

// topBorder draws the top frame line with the title in it.
//
// The line is composed rather than styled: the title has to sit IN the frame,
// and the account is exact — two glyphs for the corners, one for the dash that
// opens the title, one space on each side of it, and dashes for the rest.
func (m Model) topBorder(brd lipgloss.Border, title string, width int, frame lipgloss.Style) string {
	fill := width - 5 - lipgloss.Width(title)
	if fill < 0 {
		fill = 0
	}
	return frame.Render(brd.TopLeft+brd.Top) +
		" " + title + " " +
		frame.Render(strings.Repeat(brd.Top, fill)+brd.TopRight)
}

// pad extends a line to a width.
func pad(s string, width int) string {
	if n := width - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// gutterStr is the indent prefix of a model row.
const gutterStr = "  "

// keyLines renders the key block.
func (m Model) keyLines() []string {
	if len(m.obs.Providers) == 0 {
		return []string{m.pal.dim.Render("no providers configured")}
	}

	var out []string
	for _, p := range m.obs.Providers {
		out = append(out, m.providerHeaderLine(p))

		if len(p.Keys) == 0 {
			// "off" and "nothing to rotate" are different facts, and only one of
			// them is a decision, so the second is spelled out rather than shown
			// as an unlit indicator.
			why := "the provider's own anonymous key is used"
			if p.Credential == "account" {
				why = "no key in the pool"
			}
			out = append(out, m.pal.dim.Render("  no keys — "+why))
			continue
		}

		// A pool under the anonymous credential is configured and deliberately
		// not sent. Listing the keys without that word would let the block read
		// as "these are in use", which is the one thing the mode rules out.
		if p.Credential == "anonymous" {
			out = append(out, m.pal.dim.Render(
				"  keys present but unused — credential is anonymous, so none is sent"))
		}
		for _, k := range p.Keys {
			out = append(out, m.keyRow(k, k.Fingerprint == p.Active))
		}
	}
	out = append(out, m.pal.dim.Render("  active is inferred from the counters: the daemon holds the rotating pointer"))
	return out
}

// providerHeaderLine is the "type / credential / pool / rotation" line.
func (m Model) providerHeaderLine(p ProviderState) string {
	var b strings.Builder
	b.WriteString(m.pal.plain.Bold(true).Render(p.ID))
	b.WriteString("  ")
	b.WriteString(m.pal.dim.Render("type " + p.Type))
	b.WriteString("  ")
	b.WriteString(m.pal.dim.Render("credential "))
	b.WriteString(m.pal.plain.Render(p.Credential))
	b.WriteString("  ")
	b.WriteString(m.pal.dim.Render("pool "))
	b.WriteString(m.pal.plain.Render(fmt.Sprintf("%d", len(p.Keys))))
	b.WriteString("  ")

	// The rotation axis is named even when the pool is empty: the setting and the
	// absence of anything to apply it to are two facts.
	label := "rotate off"
	style := m.pal.dim
	if p.RotationOn {
		label = "rotate on"
		style = m.pal.ok
	}
	b.WriteString(style.Render(label))
	return b.String()
}

// keyRow renders one key of the pool.
func (m Model) keyRow(k KeyState, active bool) string {
	marker := "  "
	if active {
		marker = m.pal.accent.Render("▸ ")
	}
	var b strings.Builder
	b.WriteString(marker)
	b.WriteString(m.pal.plain.Render(k.Fingerprint))
	b.WriteString("  ")
	b.WriteString(m.pal.ok.Render(fmt.Sprintf("OK %d", k.OK)))
	b.WriteString("  ")
	b.WriteString(m.pal.err.Render(fmt.Sprintf("ERROR %d", k.Refused)))
	if k.Aborted > 0 {
		b.WriteString("  ")
		b.WriteString(m.pal.warn.Render(fmt.Sprintf("ABORT %d", k.Aborted)))
	}
	b.WriteString("  ")
	b.WriteString(m.pal.dim.Render("last " + since(k.LastSeen)))
	if active {
		b.WriteString("  ")
		b.WriteString(m.pal.accent.Render("active"))
	}
	return b.String()
}

// matrixLines renders the per-address matrix.
//
// This is the block that exists. The provider's anonymous tier is limited per
// address rather than per key, so a single total cannot tell a spent address
// from a spent key — the address is the only row key that can. The model rows
// under an address say which model drew the refusal: one address can be out of
// quota on one model and fine on another.
func (m Model) matrixLines(width int) []string {
	if len(m.obs.Addresses) == 0 {
		return []string{
			m.pal.dim.Render("no addresses observed yet"),
			m.pal.dim.Render("a row appears with the first request, once the egress address is known"),
		}
	}

	// The address column is as wide as the widest entry, so that the counters
	// line up and the eye can compare rows — but never more than half the block,
	// because an address that long would push the counters off the screen and a
	// matrix without its columns is not a matrix.
	nameW := len("address")
	for _, row := range m.obs.Addresses {
		nameW = max(nameW, lipgloss.Width(row.Address))
		for _, mr := range row.Models {
			nameW = max(nameW, lipgloss.Width(mr.Model)+gutter)
		}
	}
	if cap := max(12, width/2); nameW > cap {
		nameW = cap
	}

	aborts := anyAborted(m.obs.Addresses)

	out := []string{m.columnHeader(nameW, aborts)}
	for _, row := range m.obs.Addresses {
		out = append(out, m.addressRow(row, nameW, aborts))
		for _, mr := range row.Models {
			out = append(out, m.modelRow(mr, nameW, aborts))
		}
	}
	// The day's own total closes the block. The rows above are a breakdown, and
	// without the total a reader would add them up to answer "how much traffic
	// was there today" — which is the question the matrix is here to make
	// unnecessary.
	out = append(out, m.totalRow(nameW, aborts))
	return out
}

// totalRow is the line under the matrix.
func (m Model) totalRow(nameW int, aborts bool) string {
	label := "all addresses"
	if m.obs.Totals.Day != "" {
		label = "total " + m.obs.Totals.Day
	}
	line := m.pal.dim.Render(pad(label, nameW)) + "  "
	line += m.pal.dim.Render("   ") + "  "
	line += m.pal.ok.Render(fmt.Sprintf("%8d", m.obs.Totals.OK))
	line += "  "
	line += m.pal.err.Render(fmt.Sprintf("%8d", m.obs.Totals.Refused))
	if aborts {
		line += "  "
		line += m.pal.warn.Render(fmt.Sprintf("%8d", m.obs.Totals.Aborted))
	}
	return line
}

// columnHeader is the matrix header line.
func (m Model) columnHeader(nameW int, aborts bool) string {
	head := fmt.Sprintf("%-*s  %-3s  %8s  %8s", nameW, "address", "fam", "OK", "ERROR")
	if aborts {
		head += fmt.Sprintf("  %8s", "ABORT")
	}
	return m.pal.dim.Render(head)
}

// addressRow is one address with its own counters.
func (m Model) addressRow(row AddressRow, nameW int, aborts bool) string {
	line := pad(clip(row.Address, nameW), nameW) + "  "
	line += m.pal.dim.Render(pad(row.Family, 3)) + "  "
	line += m.pal.ok.Render(fmt.Sprintf("%8d", row.OK))
	line += "  "
	line += m.pal.err.Render(fmt.Sprintf("%8d", row.Error))
	if aborts {
		line += "  "
		line += m.pal.warn.Render(fmt.Sprintf("%8d", row.Aborted))
	}
	if row.LastSeen.IsZero() {
		return line
	}
	return line + "  " + m.pal.dim.Render("last "+since(row.LastSeen))
}

// modelRow is one model under an address.
//
// The family column is blank here and the counters sit in the same columns as
// the address above: the rows are a breakdown OF that address, and shifting the
// numbers would break the column-reading the matrix exists for.
func (m Model) modelRow(mr ModelRow, nameW int, aborts bool) string {
	label := pad(clip(gutterStr+mr.Model, nameW), nameW)
	line := m.pal.dim.Render(label) + "  "
	line += m.pal.dim.Render("   ") + "  "
	line += m.pal.ok.Render(fmt.Sprintf("%8d", mr.OK))
	line += "  "
	line += m.pal.err.Render(fmt.Sprintf("%8d", mr.Error))
	if aborts {
		line += "  "
		line += m.pal.warn.Render(fmt.Sprintf("%8d", mr.Aborted))
	}
	return line
}

// anyAborted reports whether any address has aborts, which decides whether the
// third column is named.
func anyAborted(rows []AddressRow) bool {
	for _, row := range rows {
		if row.Aborted > 0 {
			return true
		}
		for _, mr := range row.Models {
			if mr.Aborted > 0 {
				return true
			}
		}
	}
	return false
}

// logLines renders the journal block.
func (m Model) logLines(width int) []string {
	total := len(m.obs.Logs)
	shown := len(m.filtered)

	state := fmt.Sprintf("tail %d  showing %d/%d", tailLimit, shown, total)
	if m.filter != "" {
		state += fmt.Sprintf("  filter %q", m.filter)
	}
	if m.editing {
		state += "  (enter applies, esc cancels)"
	}
	state += "  follow " + onOff(m.Follow) + "  wrap " + onOff(m.Wrap)

	out := []string{m.pal.dim.Render(state)}
	if m.obs.LogPath != "" {
		out = append(out, m.pal.dim.Render(clip(m.obs.LogPath, width)))
	}

	if shown == 0 {
		if total == 0 {
			out = append(out, m.pal.dim.Render("the journal is empty"))
		} else {
			out = append(out, m.pal.dim.Render("nothing matches the filter"))
		}
		return out
	}

	for _, line := range m.filtered {
		if m.Wrap {
			out = append(out, strings.Split(wrapLine(line.text, width), "\n")...)
			continue
		}
		out = append(out, clip(line.text, width))
	}
	return out
}

// onOff renders a boolean as a word. Never a symbol: the state has to survive
// being read on a terminal without colour.
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
