package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"free-ai-router/internal/tui/presets"
)

// refreshInterval is how often the monitor re-reads the state.
//
// Two seconds: the monitor is meant to be watched while something else is
// happening, and a reader who cannot see the counter move cannot tell a slow
// provider from a stuck daemon. Faster would spend the reader's attention on
// redraws, slower would make a request that just failed look lost.
const refreshInterval = 2 * time.Second

// wheelStep is how many lines one notch of the wheel moves.
//
// Three rather than one: a notch is a coarse gesture, and a wheel that moved one
// line per notch would make a long journal tedious to walk. It is not a page
// either — a notch should feel like a nudge the reader stays in control of.
const wheelStep = 3

// tickMsg asks for a new reading.
type tickMsg struct{}

// tick returns the command that schedules the next reading.
func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

// ---------------------------------------------------------------------------
// keys
// ---------------------------------------------------------------------------

// keyMap declares every binding the monitor answers to.
//
// ONE DECLARATION, USED THREE TIMES: by the key handler, by the help bar and by
// the help modal. The earlier version declared the bindings in the view and acted
// on them with a bare string switch in the update loop, which is two lists that
// agree until someone edits one of them — and the place the disagreement would
// show is the help bar, because that is the list the reader trusts.
type keyMap struct {
	NextSection key.Binding
	PrevSection key.Binding
	SectionDash key.Binding
	SectionLog  key.Binding

	Up       key.Binding
	Down     key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	Top      key.Binding
	Bottom   key.Binding

	Filter  key.Binding
	Wrap    key.Binding
	Follow  key.Binding
	Details key.Binding

	Sort    key.Binding
	Axes    key.Binding
	Refresh key.Binding

	Help key.Binding
	Back key.Binding
	Quit key.Binding
}

// newKeyMap returns the monitor's bindings.
//
// The arrows and the vim letters are both bound everywhere: a terminal reader may
// arrive from either habit, and neither set costs the other anything. `g` and `G`
// are top and bottom rather than being left out: the journal is long, and the
// alternative to them is holding a page key down.
func newKeyMap() keyMap {
	return keyMap{
		NextSection: key.NewBinding(
			key.WithKeys("tab", "l", "right"),
			key.WithHelp("tab l →", "next section"),
		),
		PrevSection: key.NewBinding(
			key.WithKeys("shift+tab", "h", "left"),
			key.WithHelp("⇧tab h ←", "previous section"),
		),
		SectionDash: key.NewBinding(
			key.WithKeys("1", "d"),
			key.WithHelp("1 d", "the dashboard"),
		),
		SectionLog: key.NewBinding(
			key.WithKeys("2"),
			key.WithHelp("2", "the journal"),
		),

		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑ k", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓ j", "down"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("pgup", "ctrl+b"),
			key.WithHelp("pgup", "a page up"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("pgdown", "ctrl+f", "space"),
			key.WithHelp("pgdn", "a page down"),
		),
		Top: key.NewBinding(
			key.WithKeys("home", "g"),
			key.WithHelp("g", "the top"),
		),
		Bottom: key.NewBinding(
			key.WithKeys("end", "G"),
			key.WithHelp("G", "the end"),
		),

		Filter: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "filter the journal"),
		),
		Wrap: key.NewBinding(
			key.WithKeys("w"),
			key.WithHelp("w", "wrap long lines"),
		),
		Follow: key.NewBinding(
			key.WithKeys("f"),
			key.WithHelp("f", "follow the end"),
		),
		Details: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "the line in full"),
		),

		Sort: key.NewBinding(
			key.WithKeys("s"),
			key.WithHelp("s", "sort the matrix"),
		),
		Axes: key.NewBinding(
			key.WithKeys("i"),
			key.WithHelp("i", "every setting and its origin"),
		),
		Refresh: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "read again"),
		),

		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "these keys"),
		),
		Back: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "clear, then leave"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
	}
}

// keyGroup is a titled run of bindings, as the help modal lists them.
//
// Grouped by what the reader is trying to do rather than by which section they
// appear in: `w`, `f` and `/` all act on the journal, and a reader looking for
// them looks under "the journal", not under "keys" because that happens to be
// where the handler lives.
type keyGroup struct {
	name     string
	bindings []key.Binding
}

// groups returns every binding, titled.
func (k keyMap) groups() []keyGroup {
	return []keyGroup{
		{name: "moving", bindings: []key.Binding{
			k.NextSection, k.PrevSection, k.SectionDash, k.SectionLog,
		}},
		{name: "scrolling", bindings: []key.Binding{
			k.Up, k.Down, k.PageUp, k.PageDown, k.Top, k.Bottom,
		}},
		{name: "the journal", bindings: []key.Binding{
			k.Filter, k.Wrap, k.Follow, k.Details,
		}},
		{name: "the daemon", bindings: []key.Binding{
			k.Sort, k.Axes, k.Refresh,
		}},
		{name: "the monitor", bindings: []key.Binding{
			k.Help, k.Back, k.Quit,
		}},
	}
}

// shortHints is the subset the one-line bar shows.
//
// NOT THE FIRST N OF THE FULL LIST. The bar is a reminder, so what it has to
// carry is how to move, how to look closer, and how to leave. On a narrow
// terminal the look-closer key is the one dropped: it is the only one of the
// three the reader can also guess at, and the modal is one `?` away — which is a
// hint that has to survive in both widths, or the full list becomes unreachable.
func (k keyMap) shortHints(narrow bool) []presets.Hint {
	if narrow {
		return []presets.Hint{
			{Key: "tab", Desc: "sections"},
			{Key: "↑↓", Desc: "scroll"},
			{Key: "?", Desc: "keys"},
			{Key: "q", Desc: "quit"},
		}
	}
	return []presets.Hint{
		{Key: "tab", Desc: "sections"},
		{Key: "↑↓", Desc: "scroll"},
		{Key: "enter", Desc: "details"},
		{Key: "?", Desc: "keys"},
		{Key: "q", Desc: "quit"},
	}
}

// ---------------------------------------------------------------------------
// update
// ---------------------------------------------------------------------------

// Update implements tea.Model.
//
// The keys mean the same thing everywhere: move between sections, move inside
// the focused one, re-read, leave. An unassigned key does NOTHING — no redraw, no
// flicker, because a gratuitous redraw reads as a failure.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// The geometry is rebuilt rather than the offsets reset: the reader's
		// place in the journal survives a resize, and whatever is now past the
		// end is clamped on the way out.
		m.resize()
		m.clamp(m.Focus)
		return m, nil

	case tea.BackgroundColorMsg:
		m.pal = newPalette(msg.IsDark())
		// The spinner is rebuilt with the palette: it is drawn in the accent, and
		// a spinner left at the old colour is the one element that would not
		// follow the terminal's background.
		m.spinner = m.pal.NewSpinner()
		return m, nil

	case spinner.TickMsg:
		// The spinner's own command is RETURNED, not dropped: without it the
		// animation stops after a single frame and the loading state looks frozen
		// — which is the one thing a spinner exists to disprove.
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case readMsg:
		return m.applyRead(msg)

	case tickMsg:
		return m, m.read()

	case toastMsg:
		// The number is compared before the toast is cleared: a second toast
		// raised while the first was on screen owns its own timer, and clearing on
		// any tick would cut it off.
		if m.toast != nil && m.toast.seq == msg.seq {
			m.toast = nil
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseClickMsg:
		return m.handleClick(msg)

	case tea.MouseWheelMsg:
		return m.handleWheel(msg)
	}

	return m, nil
}

// applyRead folds one reading into the model.
//
// It is separate from the message switch because a reading is the moment the
// whole frame can change shape: the notes band appears or goes with it, the
// journal grows, and the geometry that every hit test is measured against has to
// be rebuilt before the next frame is drawn.
func (m Model) applyRead(msg readMsg) (tea.Model, tea.Cmd) {
	m.now = time.Now()
	m.err = msg.err
	m.loaded = true

	if msg.err == nil {
		m.obs = msg.obs
		m.filterLogs()
	}

	// THE GEOMETRY IS REBUILT HERE, not only on a resize. The notes band exists
	// only when the reading carries notes: a warning strip that appeared without
	// the geometry knowing would move every row of the frame down by one, and the
	// tab strip and the journal would both be hit-tested one row off.
	m.resize()

	if m.Follow {
		m.toBottom()
	}
	m.clampSelection()
	m.clamp(BlockLogs)

	cmds := []tea.Cmd{tick()}

	// A reading the reader asked for is confirmed; an automatic one is not. A
	// toast every two seconds is not a confirmation, it is a decoration, and the
	// reader would stop seeing it within a minute.
	if m.manual {
		m.manual = false
		var cmd tea.Cmd
		m, cmd = m.raiseToast("read "+m.now.Format("15:04:05"), msg.err != nil)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// resize recomputes the frame's geometry for the current terminal and reading.
func (m *Model) resize() {
	w, h := m.width, m.height
	if w < 1 {
		w = defaultWidth
	}
	if h < 1 {
		h = defaultHeight
	}
	m.geom = geometry(w, h, len(m.obs.Notes) > 0)
}

// ---------------------------------------------------------------------------
// keys
// ---------------------------------------------------------------------------

// handleKey routes a key press.
//
// THE MODAL TAKES EVERYTHING while it is open. A panel covering the frame that
// let the keys through would have the reader scrolling a journal they cannot see
// behind it, and the one key it must still honour is the one that force-quits.
func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.overlay != nil {
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		// The key that OPENED a modal closes it again: a reader who pressed `?`
		// by accident should not have to learn a second key to undo it.
		toggled := (m.overlay.kind == overlayHelp && key.Matches(msg, m.keys.Help)) ||
			(m.overlay.kind == overlayAxes && key.Matches(msg, m.keys.Axes))
		// The quit key closes the modal rather than the program: the modal owns
		// it while it is up, and leaving without choosing to would make `q` a
		// small trap.
		switch {
		case toggled, key.Matches(msg, m.keys.Back, m.keys.Quit, m.keys.Details):
			m.overlay = nil
		}
		return m, nil
	}

	if m.editing {
		if next, cmd, handled := m.handleEditorKey(msg); handled {
			return next, cmd
		}
		// Movement keys fall through to the shared handling below, so that a
		// filtered journal can still be scrolled while the filter itself is being
		// typed — which is how the reader checks what they typed landed where
		// they meant.
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		m.quitting = true
		return m, tea.Quit

	case key.Matches(msg, m.keys.Help):
		m.overlay = &overlay{kind: overlayHelp}
		return m, nil

	case key.Matches(msg, m.keys.Axes):
		m.overlay = &overlay{kind: overlayAxes}
		return m, nil

	case key.Matches(msg, m.keys.Back):
		// Escape clears the filter first and leaves only when there is none.
		// Leaving is the one action that costs the reader their place, so it is
		// not bound to the key that means "undo" everywhere else.
		if m.filter != "" {
			m.filter, m.filterDraft, m.filterBefore = "", "", ""
			m.refilterJournal()
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit

	case key.Matches(msg, m.keys.NextSection):
		m.Focus = (m.Focus + 1) % blockCount
		return m, nil

	case key.Matches(msg, m.keys.PrevSection):
		m.Focus = (m.Focus + blockCount - 1) % blockCount
		return m, nil

	case key.Matches(msg, m.keys.SectionDash):
		m.Focus = BlockDashboard
		return m, nil

	case key.Matches(msg, m.keys.SectionLog):
		m.Focus = BlockLogs
		return m, nil

	case key.Matches(msg, m.keys.Refresh):
		// The reading is marked as the reader's so that it is confirmed when it
		// lands: whether it succeeded is not known yet, and a confirmation
		// printed on the way out would be a guess.
		m.manual = true
		return m, m.read()

	case key.Matches(msg, m.keys.Sort):
		// Sorting only applies to the matrix tile, but the key is not gated on
		// the dashboard being on screen: the order is a property of the reading,
		// not of what is being looked at, and a reader who sorts and then returns
		// to the dashboard should find it sorted.
		m.sort = m.sort.next()
		m.Scroll[BlockDashboard] = 0
		return m, nil

	case key.Matches(msg, m.keys.Wrap):
		m.Wrap = !m.Wrap
		// Wrapping changes how many lines the journal has, so the offset may now
		// be past the end of it.
		m.clamp(BlockLogs)
		return m, nil

	case key.Matches(msg, m.keys.Follow):
		m.Follow = !m.Follow
		if m.Follow {
			m.toBottom()
		}
		return m, nil

	case key.Matches(msg, m.keys.Filter):
		m.editing = true
		m.filterBefore = m.filter
		m.filterDraft = m.filter
		m.Focus = BlockLogs
		return m, nil

	case key.Matches(msg, m.keys.Details):
		// The modal is opened from the journal and only from the journal: it
		// shows a log line, and the other sections have no line to show.
		if m.Focus == BlockLogs {
			m.openDetails()
		}
		return m, nil

	case key.Matches(msg, m.keys.Up):
		m.vertical(-1)
		return m, nil

	case key.Matches(msg, m.keys.Down):
		m.vertical(1)
		return m, nil

	case key.Matches(msg, m.keys.PageUp):
		m.scrollBy(-m.pageSize())
		return m, nil

	case key.Matches(msg, m.keys.PageDown):
		m.scrollBy(m.pageSize())
		return m, nil

	case key.Matches(msg, m.keys.Top):
		m.scrollTo(0)
		m.selectEntry(0)
		return m, nil

	case key.Matches(msg, m.keys.Bottom):
		m.scrollTo(m.scrollLimit(m.Focus))
		m.selectEntry(len(m.filtered) - 1)
		return m, nil
	}

	return m, nil
}

// handleEditorKey handles a key while the filter is being typed.
//
// The third return says whether the key was consumed. Text goes into the draft
// and is applied live rather than on enter: the reader is looking for a word they
// saw, and watching the list narrow is how they find it. The reading is not
// repeated for it — the journal is already in memory, and a disk read per
// keystroke would make the filter feel like a network call.
func (m Model) handleEditorKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keys.Details):
		m.editing = false
		m.filter = strings.TrimSpace(m.filterDraft)
		m.refilterJournal()
		return m, nil, true

	case key.Matches(msg, m.keys.Back):
		// Cancel means cancel: the filter that was in force before the editor
		// opened is put back. The editor applied the draft live, so without this
		// the reader would have no way to undo a typo but to retype the old one.
		m.editing = false
		m.filter = m.filterBefore
		m.filterDraft = m.filterBefore
		m.refilterJournal()
		return m, nil, true
	}

	if msg.String() == "backspace" {
		m.filterDraft = trimLast(m.filterDraft)
		m.applyDraft()
		return m, nil, true
	}

	// Anything that produced text is text. Everything else — the arrows, the page
	// keys, tab — is left for the handler above, so the journal stays scrollable
	// mid-filter.
	if msg.Text != "" {
		m.filterDraft += msg.Text
		m.applyDraft()
		return m, nil, true
	}

	return m, nil, false
}

// ---------------------------------------------------------------------------
// mouse
// ---------------------------------------------------------------------------

// handleClick routes a left click.
//
// THE CLICK SETS THE FOCUS, and nothing else. A click that acted — quitting,
// toggling, opening a modal — would make the mouse a second way to reach a
// destructive action, and a terminal click is the easiest thing in the interface
// to make by accident while selecting text.
func (m Model) handleClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Mouse().Button != tea.MouseLeft {
		return m, nil
	}

	// A CLICK WHILE A MODAL IS UP CLOSES IT, wherever it landed, and is not
	// passed through. Passing it on would let a click on the panel also move the
	// focus underneath; and making the reader find the panel again to dismiss it
	// would be a small cruelty, since the click is already the sign that their
	// attention has moved.
	if m.overlay != nil {
		m.overlay = nil
		return m, nil
	}

	x, y := msg.Mouse().X, msg.Mouse().Y

	// The tab strip first: now that a section fills the body, the strip is the
	// only part of the frame that moves the focus between sections.
	if b, ok := m.tabAt(x, y); ok {
		m.Focus = b
		return m, nil
	}

	// A click in the journal selects the entry under the pointer. Restricted to
	// the journal: the other sections scroll, and a click that moved their offset
	// as well would make the mouse a worse arrow key.
	if m.Focus == BlockLogs && m.geom.Body.Contains(x, y) {
		if entry, ok := m.entryAt(y); ok {
			m.selected = entry
			m.revealSelection()
		}
	}

	return m, nil
}

// handleWheel routes a wheel notch.
//
// It moves the focused section, wherever the pointer happens to be. Following the
// pointer instead would mean that a wheel over the tab strip scrolls a section
// the reader did not choose, and the reader cannot see which one it moved.
func (m Model) handleWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if m.overlay != nil {
		return m, nil
	}

	switch msg.Mouse().Button {
	case tea.MouseWheelUp:
		m.scrollBy(-wheelStep)
	case tea.MouseWheelDown:
		m.scrollBy(wheelStep)
	}

	return m, nil
}

// entryAt maps a screen row inside the journal to the entry drawn there.
//
// The row is measured from the geometry rather than counted from the top of the
// screen, so the hit test and the frame are derived from one description of where
// the body is: the earlier version recomputed the offsets by hand in the handler,
// and every element added above the body silently moved the clicks off by a row.
func (m Model) entryAt(y int) (int, bool) {
	row := y - m.geom.Body.Y - bodyInset
	if row < 0 || row >= m.geom.Body.H-2 {
		return -1, false
	}
	return m.journalEntryAtLine(m.Scroll[BlockLogs]+row, contentWidth(m.geom.Body.W))
}

// ---------------------------------------------------------------------------
// scrolling
// ---------------------------------------------------------------------------

// bodyInset is how many rows of a block's rectangle the border takes.
const bodyInset = 1

// pageSize is how many content lines the body can show: its height with its
// border excluded.
//
// IT DOES NOT DEPEND ON THE BLOCK. The address table's column titles used to be a
// fixed head that took rows off the page, and the one table on the screen is now
// a tile inside the dashboard, scrolling with everything else.
//
// A page is still asked for on behalf of a block — a reading moves the journal to
// its end even while the reader is looking at the dashboard — but that is a
// question about which section's content is being moved, not about how tall a
// page is.
func (m Model) pageSize() int {
	return max(1, m.geom.Body.H-2)
}

// lineCount is how many content lines a block has.
//
// The section is rebuilt to count them. That is affordable because this runs
// once per key press and once per reading, not once per drawn line, and the
// alternative — a cache — has to be invalidated on every resize, reading, filter
// and wrap change, which is four places to get wrong. If it ever shows up in a
// profile, the cache goes here.
func (m Model) lineCount(b Block) int {
	inner := contentWidth(m.geom.Body.W)
	if b == BlockLogs {
		return len(m.journalSection(inner))
	}
	return len(m.dashboardSection(inner))
}

// scrollLimit is the furthest a block can scroll.
func (m Model) scrollLimit(b Block) int {
	return max(0, m.lineCount(b)-m.pageSize())
}

// scrollBy moves the focused block.
// vertical moves the focused section one step up or down.
//
// WHICH OF THE TWO THINGS "ONE STEP" MEANS DEPENDS ON THE SECTION, and that is
// the point: the journal has a cursor, and the interesting unit there is an entry
// — which may be several drawn lines when it wraps — while the two tables have no
// cursor and are read a line at a time. Moving the selection and scrolling the
// lines by one in the same press, as this did, made the journal advance by two
// steps per press and left the marker and the offset fighting each other.
func (m *Model) vertical(delta int) {
	if m.Focus == BlockLogs {
		m.moveSelection(delta)
		return
	}
	m.scrollBy(delta)
}

// scrollTo moves the focused section to an offset.
func (m *Model) scrollTo(off int) {
	m.Scroll[m.Focus] = off
	m.clamp(m.Focus)
	m.pinFollow()
}

// scrollBy moves the focused section by a number of lines.
func (m *Model) scrollBy(delta int) { m.scrollTo(m.Scroll[m.Focus] + delta) }

// pinFollow records whether the journal is at its end.
//
// The pin is derived from the position rather than set by the key that moved
// there: a reader who reaches the end with the page-down key wants the same
// thing as one who reaches it with the end key, and the offset is the only place
// that knows.
func (m *Model) pinFollow() {
	if m.Focus != BlockLogs {
		return
	}
	m.Follow = m.Scroll[BlockLogs] >= m.scrollLimit(BlockLogs)
}

// moveSelection moves the journal selection by one entry.
//
// THE SELECTION IS KEPT VISIBLE, and that is the whole reason this is not a bare
// increment: a selection the reader has scrolled past would make the details key
// open a line that is not on the screen, and the modal would then show something
// the reader never pointed at.
//
// The first move from nothing selects the entry at the top of the page rather
// than skipping one: pressing down once should land on what the reader is looking
// at, not on the line after it.
func (m *Model) moveSelection(delta int) {
	if m.Focus != BlockLogs || len(m.filtered) == 0 {
		return
	}

	if m.selected < 0 {
		entry, ok := m.journalEntryAtLine(m.Scroll[BlockLogs], contentWidth(m.geom.Body.W))
		if !ok {
			entry = 0
		}
		m.selected = entry
	} else {
		m.selected = min(max(0, m.selected+delta), len(m.filtered)-1)
	}

	m.revealSelection()
}

// selectEntry selects an entry by its position in the filtered journal.
func (m *Model) selectEntry(entry int) {
	if m.Focus != BlockLogs || len(m.filtered) == 0 {
		return
	}
	m.selected = min(max(0, entry), len(m.filtered)-1)
	m.revealSelection()
}

// revealSelection scrolls the journal so that the selected entry is on screen.
func (m *Model) revealSelection() {
	if m.selected < 0 || m.selected >= len(m.filtered) {
		return
	}

	start, end, ok := m.journalSpan(m.selected, contentWidth(m.geom.Body.W))
	if !ok {
		return
	}

	page := m.pageSize()

	if start < m.Scroll[BlockLogs] {
		m.Scroll[BlockLogs] = start
	}
	// The entry's LAST line is brought onto the page, not its first: a wrapped
	// entry scrolled to by its head would have its tail below the fold, and the
	// reader moving down would see the marker sit still for several presses.
	if last := m.Scroll[BlockLogs] + page - 1; end > last {
		m.Scroll[BlockLogs] = end - page + 1
	}
	m.clamp(BlockLogs)

	// Moving the selection is reading, not leaving the end: the follow pin is
	// released only when the reader has actually gone somewhere else.
	m.pinFollow()
}

// toBottom moves the journal to its end.
func (m *Model) toBottom() { m.scrollTo(m.scrollLimit(BlockLogs)) }

// clamp keeps a block's offset within its content.
func (m *Model) clamp(b Block) {
	m.Scroll[b] = min(max(0, m.Scroll[b]), m.scrollLimit(b))
}

// clampSelection keeps the selection inside the filtered journal.
func (m *Model) clampSelection() {
	if m.selected >= len(m.filtered) {
		m.selected = len(m.filtered) - 1
	}
	if m.selected < -1 {
		m.selected = -1
	}
}

// ---------------------------------------------------------------------------
// the journal
// ---------------------------------------------------------------------------

// applyDraft applies the filter being typed.
//
// The draft is applied live rather than on enter: the reader is looking for a
// word they saw, and watching the list narrow is how they find it. The reading
// itself is not repeated — the journal is already in memory, and a disk read per
// keystroke would make the filter feel like a network call.
func (m *Model) applyDraft() {
	m.filter = strings.TrimSpace(m.filterDraft)
	m.refilterJournal()
}

// refilterJournal rebuilds the filtered journal and puts the reader back at the
// top of it.
//
// Every change to the filter goes through here: narrowing the list changes what
// line 0 is, so an offset kept from the wider list points at nothing the reader
// asked for.
func (m *Model) refilterJournal() {
	m.filterLogs()
	m.Scroll[BlockLogs] = 0
	m.clampSelection()
	if m.Follow {
		m.toBottom()
	}
}

// trimLast removes the last character of a string.
//
// []rune rather than slicing the bytes: a filter typed in a language the reader
// thinks in must not lose half a character on backspace.
func trimLast(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return string(r[:len(r)-1])
}

// filterLogs applies the current filter to the journal.
//
// Matching is case-insensitive and on a substring, not a pattern: the reader is
// looking for a word they saw, and a pattern language would be one more thing to
// get wrong while reading a log.
func (m *Model) filterLogs() {
	m.filtered = m.filtered[:0]
	needle := strings.ToLower(m.filter)

	for i, line := range m.obs.Logs {
		if needle == "" || strings.Contains(strings.ToLower(line), needle) {
			m.filtered = append(m.filtered, logLine{text: line, index: i})
		}
	}
}

// openDetails opens the modal on the selected line.
//
// The entry's position in the UNFILTERED reading is what the modal is given,
// because it shows the lines around it: a reader who filtered for an error wants
// to see what came just before it, and those lines are filtered out of the
// journal they are looking at.
func (m *Model) openDetails() {
	if m.selected < 0 || m.selected >= len(m.filtered) {
		return
	}
	m.overlay = &overlay{kind: overlayDetails, index: m.filtered[m.selected].index}
}
