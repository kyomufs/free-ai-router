package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// fakeSource is a reading that does not touch the disk.
type fakeSource struct {
	obs   Observation
	err   error
	calls int
}

func (f *fakeSource) Observe() (Observation, error) {
	f.calls++
	return f.obs, f.err
}

// observation is a deployment with two addresses, two models and a journal.
func observation() Observation {
	at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	return Observation{
		At: at,
		Daemon: []Row{
			{Name: "mode", Value: "local", Source: "file"},
			{Name: "family", Value: "auto", Source: "default"},
		},
		Providers: []ProviderState{{
			ID:         "opencode",
			Type:       "opencode",
			Credential: "anonymous",
			RotationOn: true,
			Keys: []KeyState{
				{Fingerprint: "1111aaaa", OK: 12, Refused: 1, LastSeen: at.Add(-2 * time.Second)},
				{Fingerprint: "2222bbbb", OK: 41, Refused: 3, LastSeen: at.Add(-2 * time.Minute)},
			},
			Active: "1111aaaa",
		}},
		Addresses: []AddressRow{
			{
				Address:  "213.14.43.234",
				Family:   "v4",
				OK:       41,
				Error:    3,
				LastSeen: at.Add(-2 * time.Second),
				Models: []ModelRow{
					{Model: "mimo-v2.6-flash", OK: 20, Error: 1},
					{Model: "deepseek-v4.1-flash-free", OK: 21, Error: 2},
				},
			},
			{
				Address: "2a00:1450:4001:80e::200e",
				Family:  "v6",
				OK:      4,
				Error:   0,
				Models:  []ModelRow{{Model: "mimo-v2.6-flash", OK: 4}},
			},
		},
		Totals:  Totals{Day: "2026-10-17", OK: 45, Refused: 3},
		Logs:    []string{"INFO started", "WARN provider refused", "INFO served"},
		LogPath: "/tmp/app.log",
	}
}

// monitor builds a monitor holding a reading and a terminal size.
func monitor(obs Observation) Model {
	m := New(&fakeSource{obs: obs})
	m.loaded = true
	m.obs = obs
	m.width, m.height = 120, 40
	m.filterLogs()
	return m
}

// text is what the monitor draws, without styling.
func text(m Model) string {
	return ansi.Strip(m.View().Content)
}

// press delivers a key to the monitor.
func press(t *testing.T, m Model, key string) Model {
	t.Helper()
	msg, ok := keyMsg(key)
	if !ok {
		t.Fatalf("no key message for %q", key)
	}
	next, _ := m.Update(msg)
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	return got
}

// keyMsg builds a key press from its name, so that the tests exercise the same
// strings the monitor switches on.
func keyMsg(key string) (tea.KeyMsg, bool) {
	switch key {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}, true
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, true
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}, true
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}, true
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}, true
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}, true
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}, true
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}, true
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}, true
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}, true
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}, true
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}, true
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, true
	case "ctrl+b":
		return tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}, true
	case "ctrl+f":
		return tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl}, true
	}
	if key == "G" {
		return tea.KeyPressMsg{Code: 'g', ShiftedCode: 'G', Text: "G"}, true
	}
	if r := []rune(key); len(r) == 1 {
		return tea.KeyPressMsg{Code: r[0], Text: key}, true
	}
	return nil, false
}

// TestEveryLineFitsTheTerminal is the layout contract.
//
// A block that is one column too wide wraps, and a wrapped frame is unreadable:
// the borders stop lining up and the columns of the matrix land under the wrong
// headings. The width is therefore checked, not assumed, for the sizes people
// actually use.
func TestEveryLineFitsTheTerminal(t *testing.T) {
	obs := observation()
	// Long values, so that clipping rather than luck is what keeps the line short.
	obs.Daemon = append(obs.Daemon, Row{
		Name:   "upstream",
		Value:  strings.Repeat("https://example.invalid/very/long", 4),
		Source: "file",
	})
	obs.Logs = append(obs.Logs, strings.Repeat("x", 300))

	for _, width := range []int{60, 80, 100, 120, 200} {
		for _, height := range []int{24, 40, 70} {
			m := monitor(obs)
			m.width, m.height = width, height

			lines := strings.Split(strings.TrimRight(ansi.Strip(m.View().Content), "\n"), "\n")
			if len(lines) > height {
				t.Errorf("%dx%d: the drawing is %d lines tall, the terminal is %d tall",
					width, height, len(lines), height)
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got > width {
					t.Errorf("%dx%d: line %d is %d columns wide: %q", width, height, i, got, line)
				}
			}
		}
	}
}

// TestTheBlocksAreFramed checks that the three blocks are drawn, that each has a
// complete frame, and that a frame is closed on the right — the visual assertion
// that the width arithmetic came out even.
func TestTheBlocksAreFramed(t *testing.T) {
	got := text(monitor(observation()))

	for _, name := range []string{"keys", "addresses", "log"} {
		if !strings.Contains(got, name) {
			t.Errorf("the %s block has no title:\n%s", name, got)
		}
	}
	if n := strings.Count(got, "╭"); n != blockCount {
		t.Errorf("%d blocks are framed, want %d:\n%s", n, blockCount, got)
	}
	if n := strings.Count(got, "╰"); n != blockCount {
		t.Errorf("%d frames are closed at the bottom, want %d:\n%s", n, blockCount, got)
	}

	// Every top frame has to end in a corner, which is what a line that wrapped
	// would lose.
	for i, line := range strings.Split(got, "\n") {
		if !strings.Contains(line, "╭") {
			continue
		}
		if !strings.HasSuffix(line, "╮") {
			t.Errorf("the top frame on line %d does not close: %q", i, line)
		}
	}
}

// TestAnAddressRowCarriesItsOwnCounters is the point of the exercise: the
// provider's anonymous tier is limited per address, so the address — not the
// key, not the total — has to be a row.
func TestAnAddressRowCarriesItsOwnCounters(t *testing.T) {
	got := text(monitor(observation()))

	if !strings.Contains(got, "213.14.43.234") {
		t.Fatalf("the address row is missing:\n%s", got)
	}
	if !strings.Contains(got, "OK") || !strings.Contains(got, "ERROR") {
		t.Fatalf("the OK/ERROR columns are missing:\n%s", got)
	}
	// The columns have to be named, and the address row has to be under them.
	header := strings.Index(got, "ERROR")
	row := strings.Index(got, "213.14.43.234")
	if header < 0 || row < 0 || row < header {
		t.Fatalf("the address row is not under the column header:\n%s", got)
	}
}

// TestAModelRowSitsUnderItsAddress checks the nesting, which is what makes the
// matrix readable: the model rows belong to the address above them.
func TestAModelRowSitsUnderItsAddress(t *testing.T) {
	got := text(monitor(observation()))

	first := strings.Index(got, "213.14.43.234")
	model := strings.Index(got, "mimo-v2.6-flash")
	second := strings.Index(got, "2a00:1450:4001:80e::200e")

	if first < 0 || model < 0 || second < 0 {
		t.Fatalf("the matrix is incomplete:\n%s", got)
	}
	if !(first < model && model < second) {
		t.Fatalf("the model row is not under its address (address %d, model %d, next address %d):\n%s",
			first, model, second, got)
	}
}

// TestTheKeyBlockNamesTheActiveKey checks the block's whole job: which key is in
// use, and whether rotation is on.
func TestTheKeyBlockNamesTheActiveKey(t *testing.T) {
	got := text(monitor(observation()))

	if !strings.Contains(got, "active") {
		t.Fatalf("the active key is not marked:\n%s", got)
	}
	if !strings.Contains(got, "rotate on") {
		t.Fatalf("the rotation state is not shown:\n%s", got)
	}
	if !strings.Contains(got, "credential anonymous") {
		t.Fatalf("the credential mode is not shown:\n%s", got)
	}
	if !strings.Contains(got, "1111aaaa") || !strings.Contains(got, "2222bbbb") {
		t.Fatalf("the pool is not listed:\n%s", got)
	}
}

// TestRotationOffIsSaidOutLoud: the axis is shown as a word, so that a terminal
// without colour loses nothing.
func TestRotationOffIsSaidOutLoud(t *testing.T) {
	obs := observation()
	obs.Providers[0].RotationOn = false

	if got := text(monitor(obs)); !strings.Contains(got, "rotate off") {
		t.Fatalf("rotation off is not reported:\n%s", got)
	}
}

// TestNoKeysIsExplained: a setting that toggles nothing reads as broken, so the
// absence is spelled out rather than left as an unlit indicator.
func TestNoKeysIsExplained(t *testing.T) {
	obs := observation()
	obs.Providers[0].Keys = nil
	obs.Providers[0].Active = ""

	got := text(monitor(obs))
	if !strings.Contains(got, "no keys") {
		t.Fatalf("the empty pool is not explained:\n%s", got)
	}
	if !strings.Contains(got, "rotate on") {
		t.Fatalf("the axis is hidden when the pool is empty:\n%s", got)
	}
}

// TestAnEmptyMatrixSaysWhy: a blank block would look like a failure of the
// monitor rather than an absence of data.
func TestAnEmptyMatrixSaysWhy(t *testing.T) {
	obs := observation()
	obs.Addresses = nil

	if got := text(monitor(obs)); !strings.Contains(got, "no addresses observed yet") {
		t.Fatalf("the empty matrix is not explained:\n%s", got)
	}
}

// TestTheFilterNarrowsTheJournal checks the filter and the count that makes it
// trustworthy: a filter has to say how much it is hiding.
func TestTheFilterNarrowsTheJournal(t *testing.T) {
	m := monitor(observation())
	m = press(t, m, "/")
	for _, r := range "warn" {
		m = press(t, m, string(r))
	}
	m = press(t, m, "enter")

	got := text(m)
	if !strings.Contains(got, "WARN provider refused") {
		t.Fatalf("the matching line is gone:\n%s", got)
	}
	if strings.Contains(got, "INFO started") {
		t.Fatalf("a non-matching line survived the filter:\n%s", got)
	}
	if !strings.Contains(got, "showing 1/3") {
		t.Fatalf("the filter does not report how much it hides:\n%s", got)
	}
}

// TestTheFilterIsTypedAndCancelled: escape means "undo" while the editor is open,
// and only means "quit" when there is nothing to undo.
func TestTheFilterIsTypedAndCancelled(t *testing.T) {
	m := monitor(observation())
	m = press(t, m, "/")
	m = press(t, m, "w")

	if !m.editing {
		t.Fatal("the filter editor did not open")
	}
	m = press(t, m, "esc")
	if m.editing {
		t.Fatal("escape did not close the filter editor")
	}
	if m.quitting {
		t.Fatal("escape quit while the filter editor was open")
	}
	if m.filter != "" {
		t.Fatalf("escape kept the cancelled filter %q", m.filter)
	}
}

// TestEscapeClearsABeforeItQuits: leaving is the one action that costs the
// reader their place, so it is not bound to the key that means "undo".
func TestEscapeClearsABeforeItQuits(t *testing.T) {
	m := monitor(observation())
	m.filter = "warn"
	m.filterLogs()

	m = press(t, m, "esc")
	if m.filter != "" || m.quitting {
		t.Fatalf("the first escape should clear the filter only: filter %q, quitting %v", m.filter, m.quitting)
	}
	m = press(t, m, "esc")
	if !m.quitting {
		t.Fatal("the second escape did not quit")
	}
}

// TestWrapChangesHowManyLinesTheJournalTakes: wrapping is a view of the same
// data, so it must add lines rather than lose text.
func TestWrapChangesHowManyLinesTheJournalTakes(t *testing.T) {
	obs := observation()
	obs.Logs = []string{strings.Repeat("x", 400)}

	m := monitor(obs)
	m.Wrap = false
	unwrapped := m.lineCount(BlockLogs)

	m.Wrap = true
	wrapped := m.lineCount(BlockLogs)

	if wrapped <= unwrapped {
		t.Fatalf("wrapping did not split the long line: unwrapped %d, wrapped %d", unwrapped, wrapped)
	}

	// Nothing may be lost: the runes of the line have to appear in the drawing.
	joined := strings.Join(m.logLines(m.contentWidth(m.width)), "")
	if n := strings.Count(joined, "x"); n != 400 {
		t.Fatalf("wrapping lost text: %d of 400 characters survive", n)
	}
}

// TestTheMonitorDoesNotTakeTheScreen: the alternate screen is erased on exit, and
// a diagnostic that erases itself cannot be quoted in a bug report.
func TestTheMonitorDoesNotTakeTheScreen(t *testing.T) {
	v := monitor(observation()).View()
	if v.AltScreen {
		t.Fatal("the monitor entered the alternate screen")
	}
	if v.WindowTitle != "" {
		t.Fatalf("the monitor set the window title to %q", v.WindowTitle)
	}
}

// TestTabMovesBetweenBlocks.
func TestTabMovesBetweenBlocks(t *testing.T) {
	m := monitor(observation())
	if m.Focus != BlockKeys {
		t.Fatalf("the monitor does not start on the key block: %v", m.Focus)
	}

	m = press(t, m, "tab")
	if m.Focus != BlockMatrix {
		t.Fatalf("tab from the key block landed on %v", m.Focus)
	}
	m = press(t, m, "tab")
	if m.Focus != BlockLogs {
		t.Fatalf("tab from the matrix landed on %v", m.Focus)
	}
	m = press(t, m, "tab")
	if m.Focus != BlockKeys {
		t.Fatalf("tab from the journal did not return to the keys: %v", m.Focus)
	}
	m = press(t, m, "shift+tab")
	if m.Focus != BlockLogs {
		t.Fatalf("shift+tab from the keys landed on %v", m.Focus)
	}
}

// TestQuittingIsRequested.
func TestQuittingIsRequested(t *testing.T) {
	for _, key := range []string{"q", "ctrl+c"} {
		m := monitor(observation())
		if m = press(t, m, key); !m.quitting {
			t.Fatalf("%q did not request an exit", key)
		}
		if m.View().Content != "" {
			t.Fatalf("%q left content on the screen after quitting", key)
		}
	}
}

// TestTheScrollStopsAtTheEnd: an offset past the content would draw an empty
// block and look like missing data.
func TestTheScrollStopsAtTheEnd(t *testing.T) {
	obs := observation()
	for i := 0; i < 60; i++ {
		obs.Logs = append(obs.Logs, fmt.Sprintf("INFO line %d", i))
	}

	m := monitor(obs)
	m.Focus = BlockLogs
	m.Follow = false

	for i := 0; i < 500; i++ {
		m = press(t, m, "down")
	}
	last := m.lineCount(BlockLogs) - m.pageSize(BlockLogs)
	if m.Scroll[BlockLogs] != last {
		t.Fatalf("the scroll went past the end: %d, want %d", m.Scroll[BlockLogs], last)
	}

	for i := 0; i < 500; i++ {
		m = press(t, m, "up")
	}
	if m.Scroll[BlockLogs] != 0 {
		t.Fatalf("the scroll went past the start: %d", m.Scroll[BlockLogs])
	}
}

// TestScrollingReleasesTheFollowPin: the reader asked for a different place, and
// snapping back on the next reading would undo the move.
func TestScrollingReleasesTheFollowPin(t *testing.T) {
	obs := observation()
	for i := 0; i < 60; i++ {
		obs.Logs = append(obs.Logs, fmt.Sprintf("INFO line %d", i))
	}

	m := monitor(obs)
	m.Focus = BlockLogs
	if !m.Follow {
		t.Fatal("the journal does not follow by default")
	}

	m = press(t, m, "up")
	if m.Follow {
		t.Fatal("scrolling up kept the journal pinned to the end")
	}

	m = press(t, m, "f")
	if !m.Follow {
		t.Fatal("the follow key did not pin the journal to the end again")
	}
}

// TestAReadingFailureIsShown: a monitor that goes quiet exactly when the daemon
// breaks is a monitor that hides the fault.
func TestAReadingFailureIsShown(t *testing.T) {
	m := New(&fakeSource{err: errors.New("state file is unreadable")})
	next, _ := m.Update(readMsg{err: errors.New("state file is unreadable")})
	m = next.(Model)

	if !strings.Contains(text(m), "state file is unreadable") {
		t.Fatalf("the reading failure is not shown:\n%s", text(m))
	}
}

// TestARefreshReadsAgain: the monitor is a monitor, so a reading has to happen
// more than once, and every reading has to come from the source.
func TestARefreshReadsAgain(t *testing.T) {
	source := &fakeSource{obs: observation()}
	m := New(source)

	cmd := m.read()
	if cmd == nil {
		t.Fatal("the reading command is nil")
	}
	msg := cmd()
	if source.calls != 1 {
		t.Fatalf("the reading command did not call the source: %d calls", source.calls)
	}
	if _, ok := msg.(readMsg); !ok {
		t.Fatalf("the reading command returned %T, want readMsg", msg)
	}

	// A tick asks for another reading rather than for a redraw.
	next, cmd := m.Update(tickMsg{})
	if cmd == nil {
		t.Fatal("a tick scheduled no reading")
	}
	if _, ok := next.(Model); !ok {
		t.Fatalf("a tick returned %T, want Model", next)
	}
}

// TestANoticeIsShownInTheFooter: facts the reader should not have to infer get
// their own line.
func TestANoticeIsShownInTheFooter(t *testing.T) {
	obs := observation()
	obs.Notes = []string{"the journal is empty"}

	if got := text(monitor(obs)); !strings.Contains(got, "note: the journal is empty") {
		t.Fatalf("the notice is not shown:\n%s", got)
	}
}
