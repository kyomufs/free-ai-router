package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"free-ai-router/internal/state"
	"free-ai-router/internal/tui"
)

// TestDaemonUptimeComesFromThePIDFile checks the counter in the banner's corner.
//
// THE AGE IS ONLY REPORTED FOR A LIVE PROCESS. An unclean shutdown leaves the
// file behind, and its age is then a number about a period during which nothing
// was running — the monitor would claim a daemon that has not been up since. The
// answer to "how long has it been up" is zero, which the frame draws as a dashed
// placeholder rather than as a daemon that started this instant.
func TestDaemonUptimeComesFromThePIDFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.pid")

	// The running process is this test, whose identifier is certain to be alive.
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatalf("writing the pid file: %v", err)
	}

	started := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, started, started); err != nil {
		t.Fatalf("dating the pid file: %v", err)
	}

	got := daemonUptime(path)
	if got < 2*time.Hour || got > 2*time.Hour+time.Minute {
		t.Errorf("a daemon started two hours ago reports %v", got)
	}

	// The same file naming a process that is not there: the leftover of a run
	// that ended badly, and not a running daemon.
	if err := os.WriteFile(path, []byte("999999\n"), 0o644); err != nil {
		t.Fatalf("writing the stale pid file: %v", err)
	}
	if got := daemonUptime(path); got != 0 {
		t.Errorf("a stale pid file reports %v, want 0", got)
	}

	// A file that is not there, and no file named at all.
	if got := daemonUptime(filepath.Join(dir, "missing.pid")); got != 0 {
		t.Errorf("a missing pid file reports %v, want 0", got)
	}
	if got := daemonUptime(""); got != 0 {
		t.Errorf("no pid file reports %v, want 0", got)
	}

	// A clock that ran backwards must not produce a negative age: the counter is
	// eight cells of digits and has no room for a sign.
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatalf("rewriting the pid file: %v", err)
	}
	ahead := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, ahead, ahead); err != nil {
		t.Fatalf("dating the pid file into the future: %v", err)
	}
	if got := daemonUptime(path); got != 0 {
		t.Errorf("a pid file from the future reports %v, want 0", got)
	}
}

// daySnap is a state file's worth of days for the tests below: three days, the
// newest first, with an address that first carried a request on each of the two
// newer ones.
func daySnap() *state.Snapshot {
	return &state.Snapshot{
		Version: 2,
		Today:   "2026-10-17",
		Days: []state.DaySnapshot{
			{
				Day: "2026-10-17",
				OK:  5,
				ByAddress: []state.AddressSnapshot{
					{Address: "198.51.100.4", Family: "v4", OK: 3, FirstSeen: 1_000, LastSeen: 2_000},
					{Address: "203.0.113.9", Family: "v4", OK: 2, FirstSeen: 900, LastSeen: 1_900},
				},
			},
			{Day: "2026-10-16", OK: 7, Refused: 1},
			{Day: "2026-10-15", OK: 0},
		},
	}
}

// TestTheDayIsResolvedByTheSource checks the half of the selector the model
// cannot do itself: which day the state holds, and what is shown when the reader
// asks for one it does not.
func TestTheDayIsResolvedByTheSource(t *testing.T) {
	snap := daySnap()

	// No day asked for means the daemon's own, which is what lets the frame
	// follow it across midnight.
	if shown := daySnapshot(snap, ""); shown == nil || shown.Day != "2026-10-17" {
		t.Errorf("an empty day resolved to %v, want today", shown)
	}
	if shown := daySnapshot(snap, "2026-10-16"); shown == nil || shown.OK != 7 {
		t.Errorf("the day asked for resolved to %v", shown)
	}
	// A day inside the retention window that the file does not have: nil, so the
	// caller can note it rather than answer with today's numbers.
	if shown := daySnapshot(snap, "2026-09-30"); shown != nil {
		t.Errorf("a day the state does not hold resolved to %v", shown)
	}
	if shown := daySnapshot(nil, "2026-10-16"); shown != nil {
		t.Errorf("a day of a state that does not exist resolved to %v", shown)
	}
}

// TestTheDaysAreListedNewestFirst checks the order the selector walks: it is the
// state's own, and the frame's position is counted from the front of it.
func TestTheDaysAreListedNewestFirst(t *testing.T) {
	got := dayKeys(daySnap())
	want := []string{"2026-10-17", "2026-10-16", "2026-10-15"}
	if len(got) != len(want) {
		t.Fatalf("the days are %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("the days are %v, want %v", got, want)
		}
	}
	if dayKeys(nil) != nil {
		t.Errorf("a state that does not exist lists no days")
	}
}

// TestTheMatrixCarriesTheAddressOrderAndItsMarks checks the reading of a past
// day: the rows are that day's, in the state's own order, and they carry the
// marks the monitor sorts by — the order of use has to survive a restart, and it
// only does if it is read back out of the file.
func TestTheMatrixCarriesTheAddressOrderAndItsMarks(t *testing.T) {
	rows, tot := matrix(daySnap(), "2026-10-17")
	if tot.OK != 5 || tot.Day != "2026-10-17" {
		t.Errorf("the totals are %+v", tot)
	}
	if len(rows) != 2 {
		t.Fatalf("the matrix has %d rows, want 2", len(rows))
	}
	if rows[0].Address != "198.51.100.4" {
		t.Errorf("the first row is %q, want the state's own first", rows[0].Address)
	}
	if rows[0].FirstSeen.IsZero() || rows[0].LastSeen.IsZero() {
		t.Errorf("the marks are missing from the row: %+v", rows[0])
	}

	// Yesterday's totals, not today's: the day is an argument, not a default.
	_, yesterday := matrix(daySnap(), "2026-10-16")
	if yesterday.OK != 7 || yesterday.Refused != 1 {
		t.Errorf("yesterday reads %+v", yesterday)
	}

	// A day the state does not hold has no matrix and no totals — which the frame
	// draws as "no counters", not as zeroes.
	if rows, tot := matrix(daySnap(), "2026-09-30"); rows != nil || tot != (tui.Totals{}) {
		t.Errorf("a day the state does not hold reads %v and %+v", rows, tot)
	}
}
