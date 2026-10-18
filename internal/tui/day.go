package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// The day selector: which day of counters the frame is showing.
//
// WHY IT IS A READING AND NOT A VIEW. The counters are kept per day on disk, and
// a day other than the current one is a different file's worth of answers — or,
// on the same file, a different bucket of it. Switching days therefore re-reads
// rather than re-sorts: the frame never holds two days and never has to decide
// which of its numbers belongs to which.
//
// The journal follows the selection. It is one tail of one file rather than a
// per-day index, so the filter is applied to the lines that are in the tail and
// says so when the day is not one the tail reaches.

// dayName is the day as the frame names it: "today" for the day the daemon is
// writing into, the key itself for every other one.
func (m Model) dayName() string {
	if m.obs.Day == "" || m.obs.Day == m.obs.Today {
		return "today"
	}
	return m.obs.Day
}

// dayIndex is where the shown day sits in the recorded ones, newest first, or -1
// when the reading names no day the list holds — which only happens on a reading
// that failed.
func (m Model) dayIndex() int {
	for i, day := range m.obs.Days {
		if day == m.obs.Day {
			return i
		}
	}
	return -1
}

// dayPlace is where the shown day sits as the frame names it: the position
// counted from the newest, and how many days there are. ok is false when the
// state holds no days at all.
func (m Model) dayPlace() (pos, count int, ok bool) {
	if len(m.obs.Days) == 0 {
		return 0, 0, false
	}
	// A reading of a day the list does not name is shown as the newest entry:
	// the source only produces it by falling back, and the frame must not claim
	// a position it cannot point at.
	pos = max(1, m.dayIndex()+1)
	return pos, len(m.obs.Days), true
}

// logDay is the day the journal is filtered to, empty when it is not filtered.
//
// ON TODAY THE JOURNAL IS NOT FILTERED. It is a tail of one file whose lines are
// stamped in local time while the counters key their days in UTC, so a day filter
// over it is an approximation — and on today's view the approximation can only
// hide lines, never find any: everything recent is in the tail already. It is the
// past days that the tail cannot answer, and those are the ones it is filtered
// for.
func (m Model) logDay() string {
	if m.obs.Day == "" || m.obs.Day == m.obs.Today {
		return ""
	}
	return m.obs.Day
}

// dayKeyOf turns a journal line's stamp into the day key the counters use.
//
// The stamp is parsed rather than compared as a string prefix, because the line
// carries a LOCAL time and the day key is UTC: for a reader east of Greenwich the
// last hours of a day are filed under the next one, and a prefix comparison would
// put them in the wrong list every night.
func dayKeyOf(line string) string {
	const stamp = len("2006-01-02 15:04:05.000")
	if len(line) < stamp {
		return ""
	}
	at, err := time.ParseInLocation("2006-01-02 15:04:05.000", line[:stamp], time.Local)
	if err != nil {
		return ""
	}
	return at.UTC().Format("2006-01-02")
}

// lineOnDay reports whether a journal line belongs to a day.
//
// A line whose stamp cannot be read is KEPT. The other choice — dropping it —
// turns a journal whose format changed into an empty day, and an empty day is
// indistinguishable from a daemon that served nothing, which is the one answer a
// filter must never invent.
func lineOnDay(line, day string) bool {
	if day == "" {
		return true
	}
	key := dayKeyOf(line)
	return key == "" || key == day
}

// stepDay moves the shown day through the recorded ones, positive for a newer day
// and negative for an older one.
//
// It returns a toast command when the move was refused, and none when it was
// made: the frame names the day it is showing, so a successful move is visible
// without a message covering the metrics — while a bound key that did nothing at
// all would leave the reader pressing it again.
func (m Model) stepDay(steps int) (Model, tea.Cmd) {
	if len(m.obs.Days) == 0 {
		return m, nil
	}

	// A reading of a day the list does not name — the source falling back, or a
	// reading that failed — is treated as the newest position, which is where the
	// frame draws it too.
	idx := m.dayIndex()
	if idx < 0 {
		idx = 0
	}

	switch next := idx - steps; {
	case next < 0:
		// Newer than the newest day: there is nothing later to show, unless the
		// reader was pinned to a day and is now asking for the daemon's own.
		if m.day == "" {
			return m.raiseToast("newest day recorded", false)
		}
		m.day = ""
		return m, m.read()
	case next >= len(m.obs.Days):
		return m.raiseToast("oldest day recorded", false)
	case m.obs.Days[next] == m.obs.Today:
		// Landing on the daemon's own day hands the choice back to it rather than
		// pinning its key: the two look the same now and differ at midnight.
		m.day = ""
		return m, m.read()
	default:
		m.day = m.obs.Days[next]
		return m, m.read()
	}
}

// backToToday returns to the day the daemon is writing into, and reports whether
// that changed anything.
func (m Model) backToToday() (Model, tea.Cmd, bool) {
	if m.day == "" {
		return m, nil, false
	}
	m.day = ""
	return m, m.read(), true
}
