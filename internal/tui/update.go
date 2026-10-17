package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// refreshInterval is how often the monitor re-reads the state.
//
// Two seconds: the monitor is meant to be watched while something else is
// happening, and a reader who cannot see the counter move cannot tell a slow
// provider from a stuck daemon. Faster would spend the reader's attention on
// redraws, slower would make a request that just failed look lost.
const refreshInterval = 2 * time.Second

// tickMsg asks for a new reading.
type tickMsg struct{}

// tick returns the command that schedules the next reading.
func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

// Update implements tea.Model.
//
// The keys mean the same thing in every block: switch blocks, move inside the
// focused one, re-read, quit. An unassigned key does NOTHING — no redraw, no
// flicker, because a gratuitous redraw reads as a failure.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// The scroll offsets survive a resize, but they may now be past the end
		// of what is drawn, which is clamped on the way out of the render.
		return m, nil

	case tea.BackgroundColorMsg:
		m.pal = newPalette(msg.IsDark())
		return m, nil

	case readMsg:
		m.err = msg.err
		m.loaded = true
		if msg.err == nil {
			m.obs = msg.obs
			m.filterLogs()
			if m.Follow {
				m.toBottom()
			}
		}
		return m, watch()

	case tickMsg:
		return m, m.read()

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

// watch arranges the next reading.
//
// A reading that failed is followed by another one: a monitor that goes quiet
// exactly when the daemon breaks is a monitor that hides the fault.
func watch() tea.Cmd {
	return tick()
}

// handleKey dispatches a key press.
func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// The filter editor swallows the keys it needs while it is open, and nothing
	// else: the movement keys must keep working while a filter is being typed,
	// because reading a filtered log means scrolling it.
	if m.editing {
		switch msg.String() {
		case "enter":
			m.editing = false
			m.filter = strings.TrimSpace(m.filterDraft)
			m.filterLogs()
			m.Scroll[BlockLogs] = 0
			if m.Follow {
				m.toBottom()
			}
			return m, nil
		case "esc":
			// Cancel means cancel: the filter that was in force before the editor
			// opened is put back, because the editor applied the draft live and the
			// reader would otherwise have no way to undo a typo.
			m.editing = false
			m.filter = m.filterBefore
			m.filterDraft = m.filterBefore
			m.filterLogs()
			m.Scroll[BlockLogs] = 0
			if m.Follow {
				m.toBottom()
			}
			return m, nil
		case "backspace":
			m.filterDraft = trimLast(m.filterDraft)
			m.applyDraft()
			return m, nil
		default:
			if msg.Text != "" {
				m.filterDraft += msg.Text
				m.applyDraft()
				return m, nil
			}
			// Movement keys fall through to the shared handling below, so that a
			// filtered journal can still be scrolled while the filter itself is
			// being typed.
		}
	}

	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit

	case "esc":
		// Escape clears the filter first and quits only when there is none.
		// Leaving the monitor is the one action that costs the reader their
		// place, so it is not bound to the key that means "undo" everywhere else.
		if m.filter != "" {
			m.filter, m.filterDraft, m.filterBefore = "", "", ""
			m.filterLogs()
			m.Scroll[BlockLogs] = 0
			if m.Follow {
				m.toBottom()
			}
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit

	case "tab":
		m.Focus = (m.Focus + 1) % blockCount
		return m, nil

	case "shift+tab":
		m.Focus = (m.Focus + blockCount - 1) % blockCount
		return m, nil

	case "r":
		return m, m.read()

	case "w":
		m.Wrap = !m.Wrap
		return m, nil

	case "f":
		m.Follow = !m.Follow
		if m.Follow {
			m.toBottom()
		}
		return m, nil

	case "/":
		m.editing = true
		m.filterBefore = m.filter
		m.filterDraft = m.filter
		m.Focus = BlockLogs
		return m, nil

	case "up", "k":
		m.scrollBy(-1)
		return m, nil

	case "down", "j":
		m.scrollBy(1)
		return m, nil

	case "pgup", "ctrl+b":
		m.scrollBy(-m.pageSize(m.Focus))
		return m, nil

	case "pgdown", "ctrl+f", "space":
		m.scrollBy(m.pageSize(m.Focus))
		return m, nil

	case "home", "g":
		m.Scroll[m.Focus] = 0
		return m, nil

	case "end", "G":
		m.toBottomOf(m.Focus)
		return m, nil
	}

	return m, nil
}

// scrollBy moves the focused block.
func (m *Model) scrollBy(delta int) {
	m.Scroll[m.Focus] += delta
	m.clamp(m.Focus)

	// Scrolling away from the end releases the follow pin: the reader asked for a
	// different place, and snapping back on the next reading would undo the move.
	if m.Focus == BlockLogs {
		last := m.lineCount(BlockLogs) - m.pageSize(BlockLogs)
		if last < 0 {
			last = 0
		}
		m.Follow = m.Scroll[BlockLogs] >= last
	}
}

// toBottom moves every block to its end.
func (m *Model) toBottom() { m.toBottomOf(BlockLogs) }

// toBottomOf moves one block to its end.
func (m *Model) toBottomOf(b Block) {
	m.Scroll[b] = m.lineCount(b)
	m.clamp(b)
}

// clamp keeps a block's offset within its content.
func (m *Model) clamp(b Block) {
	max := m.lineCount(b) - m.pageSize(b)
	if max < 0 {
		max = 0
	}
	if m.Scroll[b] > max {
		m.Scroll[b] = max
	}
	if m.Scroll[b] < 0 {
		m.Scroll[b] = 0
	}
}

// applyDraft applies the filter being typed.
//
// The draft is applied live rather than on enter: the reader is looking for a
// word they saw, and watching the list narrow is how they find it. The reading
// itself is not repeated — the journal is already in memory, and a disk read per
// keystroke would make the filter feel like a network call.
func (m *Model) applyDraft() {
	m.filter = strings.TrimSpace(m.filterDraft)
	m.filterLogs()
	m.Scroll[BlockLogs] = 0
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
