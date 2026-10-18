package tui

import (
	"errors"
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
			{Name: "listen", Value: "127.0.0.1:8795", Source: "flag"},
			{Name: "credential", Value: "anonymous", Source: "file"},
			{Name: "key_rotation", Value: "on", Source: "file"},
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

// monitor builds a monitor holding a reading, sized and laid out.
//
// The geometry is built here rather than left to the first message: every test
// that touches a hit test or a scroll limit reads it, and a zero geometry would
// make those tests pass for the wrong reason.
func monitor(obs Observation) Model {
	m := New(&fakeSource{obs: obs})
	m.loaded = true
	m.obs = obs
	m.width, m.height = 120, 40
	m.filterLogs()
	m.resize()
	return m
}

// resize delivers a terminal size to the monitor.
func resize(t *testing.T, m Model, width, height int) Model {
	t.Helper()
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	return got
}

// draw is what the monitor draws, without styling.
func draw(m Model) string {
	return ansi.Strip(m.View().Content)
}

// frameLines is the drawn frame, one entry per terminal row.
func frameLines(m Model) []string {
	return strings.Split(strings.TrimRight(draw(m), "\n"), "\n")
}

// rowOf is the index of the first line containing a needle.
func rowOf(m Model, needle string) int {
	for i, line := range frameLines(m) {
		if strings.Contains(line, needle) {
			return i
		}
	}
	return -1
}

// press delivers a key to the monitor.
func press(t *testing.T, m Model, k string) Model {
	t.Helper()
	msg, ok := keyMsg(k)
	if !ok {
		t.Fatalf("no key message for %q", k)
	}
	next, _ := m.Update(msg)
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	return got
}

// pressAll delivers several keys in order.
func pressAll(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		m = press(t, m, k)
	}
	return m
}

// wheel delivers a wheel notch.
//
// The message carries real coordinates rather than zeroes: the handler reads the
// button, and a message built with an empty mouse would pass a check that the
// running program would fail.
func wheel(t *testing.T, m Model, button tea.MouseButton) Model {
	t.Helper()
	next, _ := m.Update(tea.MouseWheelMsg{X: 10, Y: 10, Button: button})
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	return got
}

// click delivers a left click at a screen cell.
func click(t *testing.T, m Model, x, y int) Model {
	t.Helper()
	next, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	return got
}

// keyMsg builds a key press from its name, so that the tests exercise the same
// strings the monitor binds.
func keyMsg(k string) (tea.KeyMsg, bool) {
	switch k {
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
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}, true
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}, true
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
	if k == "G" {
		return tea.KeyPressMsg{Code: 'g', ShiftedCode: 'G', Text: "G"}, true
	}
	if r := []rune(k); len(r) == 1 {
		return tea.KeyPressMsg{Code: r[0], Text: k}, true
	}
	return nil, false
}

// journalObs is an observation with a long journal, for the scrolling tests.
func journalObs(n int) Observation {
	obs := observation()
	obs.Logs = obs.Logs[:0]
	for i := range n {
		obs.Logs = append(obs.Logs, "INFO line "+itoa(i))
	}
	return obs
}

// ---------------------------------------------------------------------------
// the frame
// ---------------------------------------------------------------------------

// TestTheFrameIsExactlyTheTerminal is the layout contract.
//
// Not "fits within": EQUALS. On the alternate screen the frame is the whole
// screen, and a line one column short leaves the previous frame's cell showing at
// the right edge while a line one row short leaves the shell's prompt underneath.
// A block that is too wide is worse still — it wraps, and a wrapped frame stops
// lining up.
func TestTheFrameIsExactlyTheTerminal(t *testing.T) {
	obs := observation()
	// Long values, so that clipping rather than luck is what keeps lines short.
	obs.Daemon = append(obs.Daemon, Row{
		Name:   "upstream",
		Value:  strings.Repeat("https://example.invalid/very/long", 4),
		Source: "file",
	})
	obs.Logs = append(obs.Logs, strings.Repeat("x", 300))
	obs.Notes = []string{"the journal file is missing", "counters are from memory"}

	for _, size := range [][2]int{
		{40, 12}, {60, 20}, {80, 24}, {100, 30}, {120, 40}, {200, 70}, {79, 19}, {81, 21},
	} {
		width, height := size[0], size[1]
		m := resize(t, monitor(obs), width, height)

		for _, focus := range []Block{BlockKeys, BlockMatrix, BlockLogs} {
			m.Focus = focus
			lines := frameLines(m)

			if len(lines) != height {
				t.Errorf("%dx%d %s: the frame is %d lines tall, want %d",
					width, height, focus, len(lines), height)
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got != width {
					t.Errorf("%dx%d %s: line %d is %d columns wide, want %d: %q",
						width, height, focus, i, got, width, line)
				}
			}
		}
	}
}

// TestTheGeometryMatchesTheDrawnFrame is the invariant that makes the mouse work.
//
// The bands are placed once and both the drawing and the hit tests read that
// placement. This checks the two against each other on the drawn output, because
// the failure mode is silent: a click that lands one row off moves the focus to a
// section nobody asked for, and nothing crashes.
func TestTheGeometryMatchesTheDrawnFrame(t *testing.T) {
	m := resize(t, monitor(observation()), 120, 40)
	g := m.geom

	if got := rowOf(m, "free-ai-router"); got != g.Banner.Y {
		t.Errorf("the banner is drawn on row %d, the geometry says %d", got, g.Banner.Y)
	}
	if got := rowOf(m, "keys"); got != g.Tabs.Y {
		t.Errorf("the tab strip is drawn on row %d, the geometry says %d", got, g.Tabs.Y)
	}
	if got := rowOf(m, "╭"); got != g.Body.Y {
		t.Errorf("the body frame opens on row %d, the geometry says %d", got, g.Body.Y)
	}
	if got := rowOf(m, "live"); got != g.Status.Y {
		t.Errorf("the metrics line is drawn on row %d, the geometry says %d", got, g.Status.Y)
	}

	// Every band is non-empty and together they tile the terminal exactly: no
	// row is claimed twice and no row is left over.
	bands := []Rect{g.Banner, g.Notes, g.Tabs, g.Body, g.Status, g.Keys}
	next := 0
	for _, r := range bands {
		if r.Empty() {
			continue
		}
		if r.Y != next {
			t.Errorf("a band starts at row %d, the previous one ended at %d", r.Y, next)
		}
		if r.W != g.Width {
			t.Errorf("a band is %d columns wide, the terminal is %d", r.W, g.Width)
		}
		next = r.Y + r.H
	}
	if next != g.Height {
		t.Errorf("the bands cover %d rows, the terminal has %d", next, g.Height)
	}
}

// TestANoteTakesARowFromTheBodyOnlyWhileItIsThere checks the one band that comes
// and goes, because its arrival is what moves every row below it.
func TestANoteTakesARowFromTheBodyOnlyWhileItIsThere(t *testing.T) {
	obs := observation()
	quiet := resize(t, monitor(obs), 100, 30)

	obs.Notes = []string{"the daemon is in server mode: the address matrix is empty"}
	loud := monitor(obs)
	loud.width, loud.height = 100, 30
	loud.resize()

	if loud.geom.Notes.Empty() {
		t.Fatal("the notes band is missing while the reading carries a note")
	}
	if got, want := loud.geom.Body.H, quiet.geom.Body.H-notesLines; got != want {
		t.Errorf("the body is %d rows with a note, %d without one", got, want)
	}
	if !strings.Contains(draw(loud), "server mode") {
		t.Errorf("the note is not drawn:\n%s", draw(loud))
	}
	if strings.Contains(draw(quiet), "server mode") {
		t.Errorf("the note is drawn in a reading that has none:\n%s", draw(quiet))
	}
}

// TestOnlyTheActiveSectionIsFramed checks the tab design: one section fills the
// body, and it is the only thing on screen with a border.
func TestOnlyTheActiveSectionIsFramed(t *testing.T) {
	m := resize(t, monitor(observation()), 120, 40)

	for _, tc := range []struct {
		key    string
		title  string
		hidden string
	}{
		{"1", "keys", "213.14.43.234"},
		{"2", "addresses", "2222bbbb"},
		{"3", "log", "213.14.43.234"},
	} {
		got := draw(press(t, m, tc.key))

		if !strings.Contains(got, tc.title) {
			t.Errorf("%q: the section is not named:\n%s", tc.key, got)
		}
		if n := strings.Count(got, "╭"); n != 1 {
			t.Errorf("%q: %d frames are drawn, want 1:\n%s", tc.key, n, got)
		}
		if n := strings.Count(got, "╰"); n != 1 {
			t.Errorf("%q: %d frames are closed at the bottom, want 1:\n%s", tc.key, n, got)
		}
		if strings.Contains(got, tc.hidden) {
			t.Errorf("%q: another section's content is on screen (%q):\n%s", tc.key, tc.hidden, got)
		}
		// Every top frame ends in a corner, which is what a wrapped line loses.
		for i, line := range strings.Split(got, "\n") {
			if strings.Contains(line, "╭") && !strings.HasSuffix(line, "╮") {
				t.Errorf("%q: the top frame on line %d does not close: %q", tc.key, i, line)
			}
		}
	}
}

// TestTheBannerNamesTheFrameAndItsClock checks the top strip: what this is, that
// it does not write, and that the clock is moving.
func TestTheBannerNamesTheFrameAndItsClock(t *testing.T) {
	m := monitor(observation())
	m.now = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	m.resize()

	got := draw(m)
	for _, want := range []string{"free-ai-router", "monitor", "read-only", "12:00:00"} {
		if !strings.Contains(got, want) {
			t.Errorf("the banner does not say %q:\n%s", want, got)
		}
	}
}

// TestTheShortTerminalCollapsesTheBanner checks the one band allowed to shrink.
//
// Under twenty rows the banner gives its rule back to the body: on a short
// terminal a decorative separator is a journal line.
func TestTheShortTerminalCollapsesTheBanner(t *testing.T) {
	m := resize(t, monitor(observation()), 100, shortTerminal-1)

	if got, want := m.geom.Banner.H, 1; got != want {
		t.Errorf("the banner takes %d rows on a short terminal, want %d", got, want)
	}
	if got := strings.Count(draw(m), "free-ai-router"); got != 1 {
		t.Errorf("the banner is drawn %d times:\n%s", got, draw(m))
	}
	// The bands under it still start where the geometry says.
	if got, want := rowOf(m, "╭"), m.geom.Body.Y; got != want {
		t.Errorf("the body opens on row %d, the geometry says %d", got, want)
	}
}

// TestTheLoadingStateSaysWhatItWaitsFor is the first frame, before any reading.
func TestTheLoadingStateSaysWhatItWaitsFor(t *testing.T) {
	m := New(&fakeSource{})
	got := draw(m)

	if !strings.Contains(got, "reading") {
		t.Errorf("the loading state does not say it is reading:\n%s", got)
	}
	// The chrome is drawn in the state as well: the frame must not reflow when
	// the first reading lands.
	if !strings.Contains(got, "free-ai-router") || !strings.Contains(got, "addresses") {
		t.Errorf("the chrome is missing from the loading state:\n%s", got)
	}
}

// TestTheErrorStateShowsTheReason checks that a failure is shown as a failure and
// not as the last good reading.
func TestTheErrorStateShowsTheReason(t *testing.T) {
	m := monitor(observation())
	m.err = errors.New("connection refused")
	m.resize()

	got := draw(m)
	if !strings.Contains(got, "could not be read") || !strings.Contains(got, "connection refused") {
		t.Errorf("the error state does not explain itself:\n%s", got)
	}
	if strings.Contains(got, "213.14.43.234") {
		t.Errorf("the last good reading is drawn as if it were current:\n%s", got)
	}
	if !strings.Contains(got, "stale") {
		t.Errorf("the freshness cell does not say the reading is stale:\n%s", got)
	}
}

// TestTheEmptyStateNamesTheWayOut checks the barren reading: a daemon that
// answered with nothing is the deployment's state, not a failure.
func TestTheEmptyStateNamesTheWayOut(t *testing.T) {
	m := monitor(Observation{At: time.Now()})

	got := draw(m)
	if !strings.Contains(got, "nothing to show yet") {
		t.Errorf("the empty state is missing:\n%s", got)
	}
	if !strings.Contains(got, "start the daemon") {
		t.Errorf("the empty state does not name the way out:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// sections and their content
// ---------------------------------------------------------------------------

// TestTheAddressRowCarriesItsOwnCounters is the point of the exercise: the
// provider's anonymous tier is limited per address, so the address — not the key,
// not the total — has to be a row.
func TestTheAddressRowCarriesItsOwnCounters(t *testing.T) {
	m := press(t, monitor(observation()), "2")
	got := draw(m)

	if !strings.Contains(got, "213.14.43.234") {
		t.Fatalf("the address row is missing:\n%s", got)
	}
	if !strings.Contains(got, "OK") || !strings.Contains(got, "ERR") {
		t.Fatalf("the OK/ERR columns are missing:\n%s", got)
	}
	// The columns are named, and the address row is under them.
	header := rowOf(m, "ADDRESS")
	row := rowOf(m, "213.14.43.234")
	if header < 0 || row < 0 || row <= header {
		t.Fatalf("the address row is not under the column header:\n%s", got)
	}
	// Every counter is over its own heading, which is what makes one row
	// comparable with the next.
	if col := columnOf(frameLines(m)[row], "41"); col != columnOf(frameLines(m)[header], "OK") {
		t.Errorf("the OK counter is at column %d, its heading at %d",
			col, columnOf(frameLines(m)[header], "OK"))
	}
}

// TestAModelRowSitsUnderItsAddress checks the nesting, which is what makes the
// matrix readable: the model rows belong to the address above them.
func TestAModelRowSitsUnderItsAddress(t *testing.T) {
	got := frameLines(press(t, monitor(observation()), "2"))

	first := indexOfLine(got, "213.14.43.234")
	model := indexOfLine(got, "mimo-v2.6-flash")
	second := indexOfLine(got, "2a00:1450:4001:80e::200e")
	lastModel := indexOfLine(got, "deepseek-v4.1-flash-free")

	if first < 0 || model < 0 || second < 0 || lastModel < 0 {
		t.Fatalf("the matrix is incomplete:\n%s", strings.Join(got, "\n"))
	}
	if model <= first {
		t.Errorf("the model row is on line %d, its address on %d", model, first)
	}
	if lastModel >= second {
		t.Errorf("the model row is on line %d, the NEXT address on %d", lastModel, second)
	}
	// Model rows are indented under their address.
	if col, want := columnOf(got[model], "mimo"), columnOf(got[first], "213"); col <= want {
		t.Errorf("the model row starts at column %d, its address at %d:\n%s",
			col, want, strings.Join(got, "\n"))
	}
}

// TestTheSortKeyReordersTheMatrix checks that the sort key is real: the counters
// are unchanged, only their order is.
func TestTheSortKeyReordersTheMatrix(t *testing.T) {
	m := press(t, monitor(observation()), "2")
	before := strings.Join(frameLines(m), "\n")
	if indexOfLine(frameLines(m), "213.14.43.234") > indexOfLine(frameLines(m), "2a00:1450:4001:80e::200e") {
		t.Fatalf("the default sort does not put the busiest address first:\n%s", before)
	}

	got := press(t, m, "s")
	if m.sort == got.sort {
		t.Fatal("the sort key did not change the order")
	}
	if !strings.Contains(draw(got), "sort errors") {
		t.Errorf("the frame does not name the sort it is in:\n%s", draw(got))
	}

	// The address with the errors is now above the one without them.
	lines := frameLines(got)
	if indexOfLine(lines, "213.14.43.234") > indexOfLine(lines, "2a00:1450:4001:80e::200e") {
		t.Errorf("sorting by errors did not move the failing address up:\n%s", strings.Join(lines, "\n"))
	}

	// Cycling round returns to where it started.
	for range sortModeCount - 1 {
		got = press(t, got, "s")
	}
	if got.sort != m.sort {
		t.Errorf("the sort cycled to %v, want %v", got.sort, m.sort)
	}
}

// TestTheKeyBlockNamesTheActiveKey checks the key section: the pool, the active
// key, and the counters that explain why the active one is the active one.
func TestTheKeyBlockNamesTheActiveKey(t *testing.T) {
	m := press(t, monitor(observation()), "1")
	got := draw(m)

	if !strings.Contains(got, "opencode") {
		t.Fatalf("the provider is not named:\n%s", got)
	}
	if !strings.Contains(got, "1111aaaa") || !strings.Contains(got, "2222bbbb") {
		t.Fatalf("the key pool is not drawn:\n%s", got)
	}
	// The active key is the one value here that decides where traffic goes, so
	// it is marked rather than left to be inferred from the counters.
	active := rowOf(m, "1111aaaa")
	if !strings.Contains(frameLines(m)[active], "▸") {
		t.Errorf("the active key is not marked:\n%s", got)
	}
	if idle := rowOf(m, "2222bbbb"); strings.Contains(frameLines(m)[idle], "▸") {
		t.Errorf("a key that is not in use is marked as active:\n%s", got)
	}

	// The counters of the pool are shown, not just the fingerprints: a key with
	// refusals is the reason a reader opens this section. Every value sits under
	// its own heading — a table whose columns drift by a cell is a table nobody
	// can compare across rows.
	head := frameLines(m)[rowOf(m, "REFUSED")]
	second2 := rowOf(m, "2222bbbb")
	for _, c := range []struct {
		title  string
		line   int
		cell   string
		rights bool
	}{
		{title: "key", line: active, cell: "1111aaaa"},
		{title: "OK", line: active, cell: "12"},
		{title: "REFUSED", line: second2, cell: "3"},
		{title: "OK", line: second2, cell: "41"},
		// The share is right-aligned in its cells, so its heading lines up with
		// the last digit and not with the first.
		{title: "share", line: active, cell: "92%", rights: true},
	} {
		at := columnOf(frameLines(m)[c.line], c.cell)
		heading := columnOf(head, c.title)
		if c.rights {
			at += lipgloss.Width(c.cell)
			heading += lipgloss.Width(c.title)
		}
		if at != heading {
			t.Errorf("the %s column is at %d in the row and %d in the heading:\n%s",
				c.title, at, heading, head)
		}
	}
	if !strings.Contains(got, "2 keys") {
		t.Errorf("the frame's summary does not count the pool:\n%s", got)
	}
}

// TestRotationOffIsSaidOutLoud checks that a switch which changes behaviour is
// named even when it is on: a reader must be able to see the state, not infer it.
func TestRotationOffIsSaidOutLoud(t *testing.T) {
	obs := observation()
	obs.Providers[0].Credential = "account"

	on := draw(press(t, monitor(obs), "1"))
	if !strings.Contains(on, "rotate on") {
		t.Errorf("rotation on is not said:\n%s", on)
	}

	obs.Providers[0].RotationOn = false
	off := draw(press(t, monitor(obs), "1"))
	if !strings.Contains(off, "rotate off") {
		t.Errorf("rotation off is not said:\n%s", off)
	}
	// With an anonymous credential rotation has nothing to rotate, and saying
	// "off" there would invent a fault the deployment does not have.
	obs.Providers[0].Credential = "anonymous"
	if anon := draw(press(t, monitor(obs), "1")); !strings.Contains(anon, "n/a") {
		t.Errorf("an anonymous credential does not say rotation is not applicable:\n%s", anon)
	}
}

// TestNoKeysIsExplained checks the empty pool: the reader needs to know that the
// daemon is fine, not that the monitor failed to read it.
func TestNoKeysIsExplained(t *testing.T) {
	obs := observation()
	obs.Providers[0].Keys = nil
	obs.Providers[0].Active = ""

	got := draw(press(t, monitor(obs), "1"))
	if !strings.Contains(got, "no keys") {
		t.Errorf("an empty pool is not explained:\n%s", got)
	}
}

// TestAnEmptyMatrixSaysWhy checks the section that is empty by design in local
// mode: the matrix only fills in server mode, and that is a fact about the
// deployment rather than a fault.
func TestAnEmptyMatrixSaysWhy(t *testing.T) {
	obs := observation()
	obs.Addresses = nil

	got := draw(press(t, monitor(obs), "2"))
	if !strings.Contains(got, "no address has served") {
		t.Errorf("an empty matrix is not explained:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// the journal
// ---------------------------------------------------------------------------

// TestTheFilterNarrowsTheJournal checks that the filter is applied, that what it
// matched is highlighted, and that the frame says how much it hid.
func TestTheFilterNarrowsTheJournal(t *testing.T) {
	m := press(t, monitor(journalObs(50)), "3")
	m = pressAll(t, m, "/", "4", "2", "enter")

	if len(m.filtered) != 1 {
		t.Fatalf("the filter kept %d lines, want 1", len(m.filtered))
	}
	if got := draw(m); !strings.Contains(got, "line 42") {
		t.Errorf("the filtered line is not drawn:\n%s", got)
	}
	if !strings.Contains(draw(m), "1 of 50 lines") {
		t.Errorf("the frame does not say how much the filter hid:\n%s", draw(m))
	}
}

// TestTheFilterIsTypedAndCancelled checks that escape restores the filter that
// was in force, because the editor applies the draft live and a reader would
// otherwise have no way back from a typo.
func TestTheFilterIsTypedAndCancelled(t *testing.T) {
	m := pressAll(t, monitor(journalObs(50)), "3", "/", "4", "2")
	if len(m.filtered) != 1 {
		t.Fatalf("the draft was not applied live: %d lines", len(m.filtered))
	}

	back := press(t, m, "esc")
	if back.editing {
		t.Error("escape did not close the editor")
	}
	if len(back.filtered) != len(journalObs(50).Logs) {
		t.Errorf("escape left %d lines, want all %d", len(back.filtered), len(journalObs(50).Logs))
	}
	if back.quitting {
		t.Error("escape left the program while it had a filter to cancel")
	}
}

// TestTheFilterEditorKeepsTheMovementKeys checks that a filtered journal can
// still be moved through while the filter is being typed: that is how a reader
// checks what they typed landed where they meant.
func TestTheFilterEditorKeepsTheMovementKeys(t *testing.T) {
	m := pressAll(t, monitor(journalObs(200)), "3", "/", "1", "0")

	if !m.editing {
		t.Fatal("the editor closed on its own")
	}
	if !strings.Contains(m.filterDraft, "10") {
		t.Fatalf("the digits did not reach the draft: %q", m.filterDraft)
	}

	moved := press(t, m, "down")
	if !moved.editing {
		t.Error("the movement key closed the editor")
	}
	if moved.selected == m.selected {
		t.Error("the movement key did not move through the filtered journal")
	}
	if moved.filterDraft != m.filterDraft {
		t.Errorf("the movement key edited the draft: %q, want %q", moved.filterDraft, m.filterDraft)
	}
}

// TestEscapeClearsTheFilterBeforeItQuits checks the two-stage escape: leaving is
// the one action that costs the reader their place, so it is not bound to the key
// that means "undo" everywhere else.
func TestEscapeClearsTheFilterBeforeItQuits(t *testing.T) {
	m := pressAll(t, monitor(observation()), "/", "s", "e", "r", "esc")

	if m.quitting {
		t.Fatal("escape quit while it had a filter to clear")
	}
	if m.filter != "" {
		t.Errorf("the filter survived: %q", m.filter)
	}
	if len(m.filtered) != len(observation().Logs) {
		t.Errorf("the journal was not restored: %d lines", len(m.filtered))
	}

	// With nothing left to clear, escape means leave.
	if !press(t, m, "esc").quitting {
		t.Error("escape did not quit with no filter left to clear")
	}
}

// TestWrapChangesHowManyLinesTheJournalTakes checks the wrap switch against the
// drawing rather than the flag: the switch is worth nothing if the frame ignores
// it.
func TestWrapChangesHowManyLinesTheJournalTakes(t *testing.T) {
	obs := journalObs(1)
	obs.Logs = []string{strings.Repeat("long ", 60)}

	m := press(t, monitor(obs), "3")
	if !m.Wrap {
		t.Fatal("wrapping is off by default, which is not what the frame claims")
	}
	wrapped := len(m.journalLayout(contentWidth(m.geom.Body.W)))

	flat := press(t, m, "w")
	if flat.Wrap {
		t.Fatal("the wrap key did not turn wrapping off")
	}
	plain := len(flat.journalLayout(contentWidth(flat.geom.Body.W)))

	if plain != 1 {
		t.Errorf("an unwrapped entry takes %d lines, want 1", plain)
	}
	if wrapped <= plain {
		t.Errorf("a wrapped entry takes %d lines, an unwrapped one %d: wrapping did nothing", wrapped, plain)
	}
	if !strings.Contains(draw(flat), "wrap off") {
		t.Errorf("the frame does not name the wrap state:\n%s", draw(flat))
	}
}

// TestTheScrollStopsAtTheEnd checks the clamp: scrolling past the end leaves the
// reader at a blank section with no way to tell why.
func TestTheScrollStopsAtTheEnd(t *testing.T) {
	m := resize(t, monitor(journalObs(200)), 100, 20)
	m = pressAll(t, m, "3", "G")

	limit := m.scrollLimit(BlockLogs)
	if limit == 0 {
		t.Fatal("the journal is short enough not to scroll, so the clamp is not tested")
	}
	if m.Scroll[BlockLogs] != limit {
		t.Errorf("the end key left the journal at %d, its limit is %d", m.Scroll[BlockLogs], limit)
	}

	// The page key past the end stops at the end rather than running away, and
	// the section still has lines to draw.
	for range 20 {
		m = press(t, m, "pgdown")
	}
	if m.Scroll[BlockLogs] != limit {
		t.Errorf("scrolling past the end moved the offset to %d, want %d", m.Scroll[BlockLogs], limit)
	}
	if got := len(m.sectionOf(contentWidth(m.geom.Body.W)).body); got == 0 {
		t.Error("the journal is empty after scrolling, so the frame would show nothing")
	}

	// And the same at the top.
	for range 20 {
		m = press(t, m, "pgup")
	}
	if m.Scroll[BlockLogs] != 0 {
		t.Errorf("scrolling past the top left the offset at %d, want 0", m.Scroll[BlockLogs])
	}
}

// TestScrollingReleasesTheFollowPin checks that a reader who scrolls away keeps
// their place: snapping back on the next reading would undo the move.
func TestScrollingReleasesTheFollowPin(t *testing.T) {
	m := pressAll(t, monitor(journalObs(60)), "3", "g")
	if m.Follow {
		t.Fatal("scrolling to the top did not release the follow pin")
	}

	pinned := pressAll(t, m, "G")
	if !pinned.Follow {
		t.Error("scrolling back to the end did not re-pin the journal")
	}

	// The switch exists as well, and it means the same thing.
	off := press(t, pinned, "f")
	if off.Follow {
		t.Error("the follow key did not release the pin")
	}
	if !strings.Contains(draw(off), "follow off") {
		t.Errorf("the frame does not name the follow state:\n%s", draw(off))
	}
}

// TestTheSelectionStaysVisible checks that moving the selection down keeps it on
// screen: a selection the reader has scrolled past would make the details key
// open a line that is not drawn.
func TestTheSelectionStaysVisible(t *testing.T) {
	m := pressAll(t, monitor(journalObs(200)), "3", "g")
	page := m.pageSize(BlockLogs)
	if page < 3 {
		t.Fatalf("the page is %d lines, too short to test the reveal", page)
	}

	for i := 0; i < page+5; i++ {
		m = press(t, m, "down")
	}
	if m.selected < 0 {
		t.Fatal("the movement keys did not select an entry")
	}
	// The cursor is what these keys move, so the reader walks one entry per press
	// rather than one drawn line.
	if m.selected < page {
		t.Errorf("the selection is on entry %d after %d presses down", m.selected, page+5)
	}

	start, end, ok := m.journalSpan(m.selected, contentWidth(m.geom.Body.W))
	if !ok {
		t.Fatal("the selected entry is not in the layout the frame draws")
	}
	off := m.Scroll[BlockLogs]
	if start < off || end > off+page-1 {
		t.Errorf("the selection spans lines %d..%d, the page is %d..%d", start, end, off, off+page-1)
	}

	// The pin follows the position, not the key that moved there: walking away
	// from the end releases it, and the end key takes it up again.
	if m.Follow {
		t.Error("moving the selection away from the end left the journal following it")
	}
	if back := press(t, m, "G"); !back.Follow {
		t.Error("the end key did not take the follow pin up again")
	}
}

// TestAClickSelectsTheJournalEntryUnderThePointer checks the hit test against the
// geometry: the row is measured from the body's rectangle, so a click lands on
// the line that is drawn there.
func TestAClickSelectsTheJournalEntryUnderThePointer(t *testing.T) {
	m := pressAll(t, monitor(journalObs(40)), "3", "g")

	// The third drawn row of the journal, addressed through the frame's own
	// geometry rather than by guessing where the body starts.
	y := m.geom.Body.Y + bodyInset + 3
	got := click(t, m, m.geom.Body.X+4, y)

	entry, ok := m.journalEntryAtLine(m.Scroll[BlockLogs]+3, contentWidth(m.geom.Body.W))
	if !ok {
		t.Fatal("the third journal row is not an entry")
	}
	if got.selected != entry {
		t.Errorf("the click selected entry %d, the row drawn there is %d", got.selected, entry)
	}
	if !strings.Contains(draw(got), "line "+itoa(entry)) {
		t.Errorf("the selected entry is not the one drawn there:\n%s", draw(got))
	}

	// A click above the body — on the tab strip — moves the section instead, and
	// must not select anything.
	onTab := click(t, m, 2, m.geom.Tabs.Y)
	if onTab.selected != m.selected {
		t.Errorf("a click on the tab strip moved the selection to %d", onTab.selected)
	}
}

// TestTheClickChoosesTheTabItLandsOn checks the tab hit test: the strip is the
// mouse's way between sections, and each label owns its own columns.
func TestTheClickChoosesTheTabItLandsOn(t *testing.T) {
	m := resize(t, monitor(observation()), 120, 40)

	labels := m.tabLabels(m.geom.Tabs.W)
	cursor := 0
	for i, label := range labels {
		w := lipgloss.Width(m.pal.TabIdlePill.Render(label))

		got := click(t, m, cursor+w/2, m.geom.Tabs.Y)
		if int(got.Focus) != i {
			t.Errorf("a click on tab %d (%q) focused %v", i, label, got.Focus)
		}
		cursor += w + tabGap
	}

	// The gap between two labels belongs to no section: a click there is a
	// mis-aimed click, and moving the focus would make it look deliberate.
	if got := click(t, m, cursor+tabGap-1, m.geom.Tabs.Y); got.Focus != m.Focus {
		t.Errorf("a click past the last tab moved the focus to %v", got.Focus)
	}
}

// TestTheTabKeysMoveBetweenSections checks every way the sections are reached:
// the cycle, the letters, the digits and the arrows all land on the same three.
func TestTheTabKeysMoveBetweenSections(t *testing.T) {
	m := resize(t, monitor(observation()), 120, 40)

	for _, tc := range []struct {
		keys []string
		want Block
	}{
		{[]string{"tab"}, BlockMatrix},
		{[]string{"l"}, BlockMatrix},
		{[]string{"right"}, BlockMatrix},
		{[]string{"shift+tab"}, BlockLogs},
		{[]string{"h"}, BlockLogs},
		{[]string{"left"}, BlockLogs},
		{[]string{"tab", "tab", "tab"}, BlockKeys},
		{[]string{"1"}, BlockKeys},
		{[]string{"2"}, BlockMatrix},
		{[]string{"3"}, BlockLogs},
	} {
		if got := pressAll(t, m, tc.keys...).Focus; got != tc.want {
			t.Errorf("%v focused %v, want %v", tc.keys, got, tc.want)
		}
	}
}

// TestTheWheelScrollsTheFocusedSection checks that the wheel follows the focus
// rather than the pointer: a wheel that scrolled whatever it happened to be over
// would move a section the reader did not choose.
func TestTheWheelScrollsTheFocusedSection(t *testing.T) {
	m := pressAll(t, monitor(journalObs(200)), "3", "g")

	got := wheel(t, m, tea.MouseWheelDown)
	if got.Scroll[BlockLogs] != wheelStep {
		t.Errorf("the wheel moved the journal to %d, want %d", got.Scroll[BlockLogs], wheelStep)
	}
	if got.Scroll[BlockMatrix] != 0 {
		t.Errorf("the wheel moved a section that does not have the focus")
	}

	back := wheel(t, got, tea.MouseWheelUp)
	if back.Scroll[BlockLogs] != 0 {
		t.Errorf("the wheel up left the journal at %d, want 0", back.Scroll[BlockLogs])
	}
}

// ---------------------------------------------------------------------------
// the modals
// ---------------------------------------------------------------------------

// TestTheDetailsModalOpensOnTheSelectedLine checks the modal against the journal:
// it shows the line the reader pointed at, in full, with its neighbours.
func TestTheDetailsModalOpensOnTheSelectedLine(t *testing.T) {
	m := pressAll(t, monitor(journalObs(40)), "3", "g", "down", "down", "enter")

	if m.overlay == nil || m.overlay.kind != overlayDetails {
		t.Fatalf("the details modal did not open: %+v", m.overlay)
	}
	want := "line " + itoa(m.overlay.index)
	if got := draw(m); !strings.Contains(got, want) {
		t.Errorf("the modal does not show %q:\n%s", want, got)
	}
	// The row it was opened on is still visible behind it: the modal is
	// composited over the frame, not drawn into it.
	if !strings.Contains(draw(m), "free-ai-router") {
		t.Errorf("the frame is gone from behind the modal:\n%s", draw(m))
	}
}

// TestTheDetailsModalShowsContext checks that the modal answers "what happened
// around this line", which is what the reader stopped for.
func TestTheDetailsModalShowsContext(t *testing.T) {
	m := pressAll(t, monitor(journalObs(40)), "3", "g", "down", "down", "down", "enter")

	got := draw(m)
	idx := m.overlay.index
	for _, i := range []int{idx - 1, idx + 1} {
		if !strings.Contains(got, "line "+itoa(i)) {
			t.Errorf("the line after/before the opened one (%d) is not shown:\n%s", i, got)
		}
	}
	if !strings.Contains(got, "▌") {
		t.Errorf("the opened line is not marked:\n%s", got)
	}
}

// TestTheModalIsDismissedWithoutLeaving checks that escape closes the modal and
// does not quit, which is the difference between closing a panel and losing the
// monitor.
func TestTheModalIsDismissedWithoutLeaving(t *testing.T) {
	opened := pressAll(t, monitor(observation()), "?")
	if opened.overlay == nil {
		t.Fatal("the help modal did not open")
	}

	closed := press(t, opened, "esc")
	if closed.overlay != nil {
		t.Error("escape did not close the modal")
	}
	if closed.quitting {
		t.Error("escape quit the program instead of closing the modal")
	}
}

// TestTheModalOwnsTheQuitKey checks that `q` closes the modal rather than the
// program: the modal owns the keys while it is up, and leaving without choosing
// to would make `q` a small trap.
func TestTheModalOwnsTheQuitKey(t *testing.T) {
	opened := pressAll(t, monitor(observation()), "?")
	closed := press(t, opened, "q")

	if closed.overlay != nil {
		t.Error("the quit key did not close the modal")
	}
	if closed.quitting {
		t.Error("the quit key quit while a modal was up")
	}
	// The force-quit still leaves, modal or not: a reader must never be stuck.
	if !press(t, opened, "ctrl+c").quitting {
		t.Error("ctrl+c did not quit with a modal up")
	}
}

// TestTheOpenKeyTogglesTheModalItself checks that the key which opened a panel
// closes it: a reader who pressed `?` by accident should not have to learn a
// second key to undo it.
func TestTheOpenKeyTogglesTheModalItself(t *testing.T) {
	for _, tc := range []struct {
		key  string
		kind overlayKind
	}{
		{"?", overlayHelp},
		{"i", overlayAxes},
	} {
		opened := press(t, monitor(observation()), tc.key)
		if opened.overlay == nil || opened.overlay.kind != tc.kind {
			t.Fatalf("%q did not open its modal: %+v", tc.key, opened.overlay)
		}
		if got := press(t, opened, tc.key); got.overlay != nil {
			t.Errorf("%q did not close its own modal", tc.key)
		}
	}
}

// TestTheAxesModalListsEveryAxisWithItsOrigin checks the panel that replaced the
// axes strip: the value and which of the three sources won.
func TestTheAxesModalListsEveryAxisWithItsOrigin(t *testing.T) {
	obs := observation()
	m := press(t, monitor(obs), "i")

	got := draw(m)
	for _, row := range obs.Daemon {
		if !strings.Contains(got, row.Name) {
			t.Errorf("the axis %q is missing:\n%s", row.Name, got)
		}
		if !strings.Contains(got, row.Value) {
			t.Errorf("the value of %q is missing:\n%s", row.Name, got)
		}
		if !strings.Contains(got, row.Source) {
			t.Errorf("the origin of %q is missing:\n%s", row.Name, got)
		}
	}
	// The three axes that decide where traffic goes are on the status line, where
	// they are actually watched, rather than costing the body two rows.
	status := frameLines(m)[m.geom.Status.Y]
	for _, name := range []string{"mode", "credential", "family"} {
		if !strings.Contains(status, name) {
			t.Errorf("the status line does not carry %q: %q", name, status)
		}
	}
}

// TestTheHelpModalListsEveryBinding checks the modal against the key map: the
// help must be generated from the bindings the handlers use, or it is a second
// list that agrees until someone edits one.
func TestTheHelpModalListsEveryBinding(t *testing.T) {
	got := draw(press(t, monitor(observation()), "?"))

	total := 0
	for _, g := range monitor(observation()).keys.groups() {
		if !strings.Contains(got, g.name) {
			t.Errorf("the group %q is missing:\n%s", g.name, got)
		}
		for _, b := range g.bindings {
			total++
			help := b.Help()
			if help.Key == "" || help.Desc == "" {
				t.Errorf("a binding in %q has no help text", g.name)
				continue
			}
			if !strings.Contains(got, help.Desc) {
				t.Errorf("the binding %q (%s) is missing from the help:\n%s",
					help.Key, help.Desc, got)
			}
		}
	}
	// The modal lists them all: it is the answer to the bar's one line.
	if total < 15 {
		t.Errorf("only %d bindings are declared, the monitor should have more", total)
	}
	// The read-only promise belongs here, where a reader goes to ask what this
	// program can do — and it belongs in the heading, which is the part of a
	// modal that never gets clipped away.
	if !strings.Contains(got, "read-only") {
		t.Errorf("the help does not say the monitor does not write:\n%s", got)
	}
}

// TestTheHelpModalFitsTheTerminal checks that a modal is clipped rather than
// wrapped: a modal wider than the terminal would push the frame under it sideways.
func TestTheHelpModalFitsTheTerminal(t *testing.T) {
	for _, size := range [][2]int{{40, 12}, {60, 20}, {80, 24}, {120, 40}} {
		m := resize(t, monitor(observation()), size[0], size[1])
		m = press(t, m, "?")

		lines := frameLines(m)
		if len(lines) != size[1] {
			t.Errorf("%v: the frame with a modal is %d lines, want %d", size, len(lines), size[1])
		}
		for i, line := range lines {
			if got := lipgloss.Width(line); got != size[0] {
				t.Errorf("%v: line %d is %d columns, want %d: %q", size, i, got, size[0], line)
			}
		}
	}
}

// TestTheModalIsDismissedByAClick checks the mouse side of the modal: a click
// anywhere closes it, which is what a reader's attention moving elsewhere looks
// like.
func TestTheModalIsDismissedByAClick(t *testing.T) {
	opened := pressAll(t, monitor(observation()), "i")
	if opened.overlay == nil {
		t.Fatal("the axes modal did not open")
	}

	closed := click(t, opened, 5, 5)
	if closed.overlay != nil {
		t.Error("a click did not close the modal")
	}
	if closed.Focus != opened.Focus {
		t.Errorf("the click that closed the modal also moved the focus to %v", closed.Focus)
	}
}

// ---------------------------------------------------------------------------
// the readings
// ---------------------------------------------------------------------------

// TestTheRefreshReadsAgainAndSaysSo checks both halves of the refresh key: it
// re-reads, and it confirms — a re-read of an unchanged daemon draws an identical
// frame, and without the confirmation the reader cannot tell it from a key that
// did nothing.
func TestTheRefreshReadsAgainAndSaysSo(t *testing.T) {
	src := &fakeSource{obs: observation()}
	m := New(src)
	m.loaded = true
	m.obs = observation()
	m.width, m.height = 120, 40
	m.resize()

	next, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	pressed, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	if !pressed.manual {
		t.Fatal("the refresh did not mark the reading as the reader's")
	}
	if cmd == nil {
		t.Fatal("the refresh returned no command")
	}

	// Deliver the reading the command produces, then the toast it raises.
	read, _ := pressed.Update(cmd())
	after, ok := read.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", read)
	}
	if src.calls == 0 {
		t.Error("the refresh did not read the source")
	}
	if after.manual {
		t.Error("the manual mark survived the reading")
	}
	if after.toast == nil {
		t.Fatal("the refresh was not confirmed")
	}
	if !strings.Contains(draw(after), "read ") {
		t.Errorf("the confirmation is not drawn:\n%s", draw(after))
	}
}

// TestTheAutomaticReadingIsNotConfirmed checks the other half: a toast every two
// seconds is not a confirmation, it is a decoration, and the reader would stop
// seeing it within a minute.
func TestTheAutomaticReadingIsNotConfirmed(t *testing.T) {
	m := monitor(observation())
	next, _ := m.Update(tickMsg{})
	ticked, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	if ticked.manual {
		t.Error("a scheduled tick marked the reading as the reader's")
	}

	read, _ := ticked.Update(readMsg{obs: observation()})
	after, ok := read.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", read)
	}
	if after.toast != nil {
		t.Error("an automatic reading raised a confirmation")
	}
	if !after.loaded || after.err != nil {
		t.Errorf("the reading was not applied: loaded=%v err=%v", after.loaded, after.err)
	}
}

// TestTheToastClearsItself checks that the confirmation goes away without the
// reader having to dismiss it, and that a second toast is not cut short by the
// first one's timer.
func TestTheToastClearsItself(t *testing.T) {
	m := monitor(observation())

	raised, _ := m.raiseToast("read 12:00:00", false)
	if raised.toast == nil {
		t.Fatal("raiseToast did not raise a toast")
	}
	cleared, _ := raised.Update(toastMsg{seq: raised.toast.seq})
	after, ok := cleared.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", cleared)
	}
	if after.toast != nil {
		t.Error("the toast survived its own timer")
	}

	// A stale timer must not clear a toast raised after it.
	second, _ := raised.raiseToast("read 12:00:02", true)
	third, _ := second.Update(toastMsg{seq: raised.toast.seq})
	kept, ok := third.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", third)
	}
	if kept.toast == nil {
		t.Error("an earlier timer cut a later toast short")
	}
}

// TestAReadingFailureIsShownAndTheFrameSurvives checks the failure path: the
// reading is reported, the chrome stays, and the next good reading brings the
// sections back.
func TestAReadingFailureIsShownAndTheFrameSurvives(t *testing.T) {
	m := monitor(observation())

	next, _ := m.Update(readMsg{err: errors.New("no route to host")})
	failed, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	if failed.err == nil {
		t.Fatal("the failure was swallowed")
	}
	if !strings.Contains(draw(failed), "no route to host") {
		t.Errorf("the reason is not drawn:\n%s", draw(failed))
	}
	if !strings.Contains(draw(failed), "free-ai-router") {
		t.Errorf("the chrome was dropped along with the reading:\n%s", draw(failed))
	}

	back, _ := failed.Update(readMsg{obs: observation()})
	recovered, ok := back.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", back)
	}
	if recovered.err != nil {
		t.Fatalf("the failure survived a good reading: %v", recovered.err)
	}
	if !strings.Contains(draw(recovered), "opencode") {
		t.Errorf("the sections did not come back:\n%s", draw(recovered))
	}
}

// TestTheReadingsKeepTheReadersPlace checks that an arriving reading does not
// move anything the reader set: a monitor that reset the filter or the sort every
// two seconds would be unusable.
func TestTheReadingsKeepTheReadersPlace(t *testing.T) {
	m := pressAll(t, monitor(journalObs(200)), "3", "/", "1", "0", "enter", "s")

	next, _ := m.Update(readMsg{obs: journalObs(200)})
	after, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}

	if after.filter != m.filter {
		t.Errorf("the reading reset the filter: %q, want %q", after.filter, m.filter)
	}
	if after.sort != m.sort {
		t.Errorf("the reading reset the sort: %v, want %v", after.sort, m.sort)
	}
	if after.Wrap != m.Wrap || after.Follow != m.Follow {
		t.Errorf("the reading reset the wrap/follow switches: %v/%v", after.Wrap, after.Follow)
	}
}

// TestTheMonitorTakesTheScreenAndGivesItBackOnDemand checks the two display
// modes: the alternate screen for watching, and the plain one for capturing.
func TestTheMonitorTakesTheScreenAndGivesItBackOnDemand(t *testing.T) {
	m := monitor(observation())
	if m.View().AltScreen != true {
		t.Error("the monitor does not take the alternate screen by default")
	}
	if m.View().WindowTitle == "" {
		t.Error("the monitor does not name its window")
	}

	m.Scrollback = true
	if m.View().AltScreen {
		t.Error("--scrollback still takes the alternate screen")
	}
}

// TestQuittingIsRequested checks that both quit keys ask the program to stop and
// that the frame is emptied on the way out: the alternate screen is restored by
// the program, and drawing one more frame over the restored screen would leave
// half a monitor in the shell.
func TestQuittingIsRequested(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		m := press(t, monitor(observation()), k)
		if !m.quitting {
			t.Errorf("%q did not request a quit", k)
		}
		if m.View().Content != "" {
			t.Errorf("%q: the frame is still drawn after the quit", k)
		}
	}
}

// TestAnUnknownKeyChangesNothing checks that an unbound key is inert: a
// gratuitous redraw reads as a failure, because the reader cannot tell it from
// the state having changed.
func TestAnUnknownKeyChangesNothing(t *testing.T) {
	m := monitor(observation())
	before := draw(m)
	beforeScroll := m.Scroll

	got := press(t, m, "z")
	if draw(got) != before {
		t.Error("an unbound key changed the frame")
	}
	if got.Scroll != beforeScroll {
		t.Error("an unbound key moved a scroll offset")
	}
}

// TestTheNarrowTerminalKeepsTheSectionsReachable checks the adaptive part: the
// strip shortens rather than losing its tail, because the section that falls off
// the end is always the journal.
func TestTheNarrowTerminalKeepsTheSectionsReachable(t *testing.T) {
	for _, width := range []int{16, 24, 44, 80} {
		m := resize(t, monitor(observation()), width, 16)

		labels := m.tabLabels(m.geom.Tabs.W)
		if len(labels) != blockCount {
			t.Fatalf("%d columns: %d tabs are drawn, want %d", width, len(labels), blockCount)
		}
		if got := lipgloss.Width(m.tabsStrip(m.geom.Tabs.W)); got > m.geom.Tabs.W {
			t.Errorf("%d columns: the strip is %d wide, the band is %d", width, got, m.geom.Tabs.W)
		}
		if width < 24 {
			for i, label := range labels {
				if lipgloss.Width(label) != 1 {
					t.Errorf("%d columns: tab %d is %q, want a single letter", width, i, label)
				}
			}
		}

		// Every section is still reachable, whatever the strip had to give up.
		for i, b := range []Block{BlockKeys, BlockMatrix, BlockLogs} {
			got := press(t, m, itoa(i+1))
			if got.Focus != b {
				t.Errorf("%d columns: the section key focused %v, want %v", width, got.Focus, b)
			}
			if title := got.sectionTitle(); title != b.String() {
				t.Errorf("%d columns: the %v section is titled %q", width, b, title)
			}
		}
	}
}

// indexOfLine is the first frame line containing a needle.
func indexOfLine(lines []string, needle string) int {
	for i, line := range lines {
		if strings.Contains(line, needle) {
			return i
		}
	}
	return -1
}

// columnOf is the column a needle starts at within a frame line.
func columnOf(line, needle string) int {
	i := strings.Index(line, needle)
	if i < 0 {
		return -1
	}
	return lipgloss.Width(line[:i])
}
