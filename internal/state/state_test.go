package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// openTemp creates the state in a temporary directory.
func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, path
}

// TestCountersAccumulate — the counters add up by day, model and key.
func TestCountersAccumulate(t *testing.T) {
	s, _ := openTemp(t)

	s.RecordSuccess("model-a", "aaaa1111")
	s.RecordSuccess("model-a", "aaaa1111")
	s.RecordSuccess("model-b", "bbbb2222")
	s.RecordRefusal("model-a", "aaaa1111")

	snap := s.Snapshot()
	if len(snap.Days) != 1 {
		t.Fatalf("Days = %d, want 1", len(snap.Days))
	}
	day := snap.Days[0]
	if day.OK != 3 {
		t.Fatalf("OK = %d, want 3", day.OK)
	}
	if day.Refused != 1 {
		t.Fatalf("Refused = %d, want 1", day.Refused)
	}

	byModel := map[string]ModelSnapshot{}
	for _, m := range day.ByModel {
		byModel[m.Model] = m
	}
	if got := byModel["model-a"]; got.OK != 2 || got.Refused != 1 {
		t.Fatalf("model a = %+v, want ok=2 refused=1", got)
	}
	if got := byModel["model-b"]; got.OK != 1 || got.Refused != 0 {
		t.Fatalf("model b = %+v, want ok=1 refused=0", got)
	}
	if len(day.Keys) != 2 {
		t.Fatalf("Keys = %d, want 2", len(day.Keys))
	}
}

// TestEmptyModelAndKeyDoNotCreateEntries — an empty model or key must not
// create empty entries: they would distort the lists.
func TestEmptyModelAndKeyDoNotCreateEntries(t *testing.T) {
	s, _ := openTemp(t)
	s.RecordSuccess("", "")
	s.RecordRefusal("", "")

	day := s.Snapshot().Days[0]
	if len(day.ByModel) != 0 {
		t.Fatalf("ByModel = %+v, want empty", day.ByModel)
	}
	if len(day.Keys) != 0 {
		t.Fatalf("Keys = %+v, want empty", day.Keys)
	}
	if day.OK != 1 || day.Refused != 1 {
		t.Fatalf("totals = ok %d refused %d, want 1 and 1", day.OK, day.Refused)
	}
}

// TestSaveAndReload — the state survives a restart. That is its only job.
func TestSaveAndReload(t *testing.T) {
	s, path := openTemp(t)
	s.RecordSuccess("model", "key1")
	s.RecordRefusal("model", "key1")
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open after Save: %v", err)
	}
	snap := reopened.Snapshot()
	if len(snap.Days) != 1 {
		t.Fatalf("Days = %d after reload, want 1", len(snap.Days))
	}
	if snap.Days[0].OK != 1 || snap.Days[0].Refused != 1 {
		t.Fatalf("counters lost: %+v", snap.Days[0])
	}
}

// TestCorruptFileDoesNotBlockStart — a corrupt file does not get in the way of
// work, but it does not vanish silently either: the reason is returned.
func TestCorruptFileDoesNotBlockStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err == nil {
		t.Fatal("a corrupt file must be reported")
	}
	if s == nil {
		t.Fatal("a corrupt file must still yield a usable store")
	}
	// The store must be usable.
	s.RecordSuccess("model", "k")
	if got := s.Snapshot().Days[0].OK; got != 1 {
		t.Fatalf("OK = %d, want 1", got)
	}

	// The write REPLACES the corrupt file with a correct one.
	//
	// This is how it should be: the counters are saved to disk on every write,
	// otherwise they would not survive a restart — and that is their only job.
	// The content of the corrupt file is already lost, there is nothing to
	// restore, so a new state takes its place. The former content does not
	// vanish silently: the reason was returned from Open above.
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var shape fileShape
	if uerr := json.Unmarshal(raw, &shape); uerr != nil {
		t.Fatalf("the file was not replaced with valid state: %v (%s)", uerr, raw)
	}
	if shape.Version != schemaVersion {
		t.Fatalf("version = %d, want %d", shape.Version, schemaVersion)
	}
}

// TestMissingFileIsNotAnError — a missing file is a normal start.
func TestMissingFileIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if snap := s.Snapshot(); len(snap.Days) != 0 {
		t.Fatalf("a fresh store has %d days, want 0", len(snap.Days))
	}
}

// TestEmptyFileIsNotAnError — an empty file is equivalent to a missing one.
func TestEmptyFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err != nil {
		t.Fatalf("an empty file must be accepted: %v", err)
	}
}

// TestSaveIsAtomic — the write goes through a temporary file and a rename.
//
// What is checked is the observable consequence: after the write no litter is
// left alongside, and the file reads as valid JSON.
func TestSaveIsAtomic(t *testing.T) {
	s, path := openTemp(t)
	s.RecordSuccess("model", "k")
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var shape fileShape
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("the saved file is not valid JSON: %v", err)
	}
	if shape.Version != schemaVersion {
		t.Fatalf("version = %d, want %d", shape.Version, schemaVersion)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			t.Fatalf("Save left a temporary file behind: %s", e.Name())
		}
	}
}

// TestPruneDropsOldDays — the retention window is bounded, otherwise the file
// would grow forever.
func TestPruneDropsOldDays(t *testing.T) {
	s, _ := openTemp(t)

	// Put in a deliberately old day and a fresh one.
	s.mu.Lock()
	s.days["2000-01-01"] = &DayCounters{OK: 999}
	s.days[dayKeyForTest()] = &DayCounters{OK: 1}
	s.mu.Unlock()

	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	snap := s.Snapshot()
	for _, d := range snap.Days {
		if d.Day == "2000-01-01" {
			t.Fatal("a day outside the retention window survived the prune")
		}
	}
	if len(snap.Days) != 1 {
		t.Fatalf("Days = %d, want only today", len(snap.Days))
	}
}

// TestClearEmptiesEverything — clearing zeroes the counters and saves an empty
// state.
func TestClearEmptiesEverything(t *testing.T) {
	s, path := openTemp(t)
	s.RecordSuccess("model", "k")
	if err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if snap := s.Snapshot(); len(snap.Days) != 0 {
		t.Fatalf("Clear left %d days", len(snap.Days))
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if snap := reopened.Snapshot(); len(snap.Days) != 0 {
		t.Fatalf("Clear did not persist: %d days on reload", len(snap.Days))
	}
}

// TestSnapshotIsDeterministicallyOrdered — without an explicit order comparing
// two snapshots would be impossible.
func TestSnapshotIsDeterministicallyOrdered(t *testing.T) {
	s, _ := openTemp(t)
	for _, m := range []string{"z", "b", "a"} {
		s.RecordSuccess(m, "k")
	}

	first := s.Snapshot()
	second := s.Snapshot()
	if len(first.Days) != len(second.Days) {
		t.Fatal("snapshot length varies between calls")
	}
	for i := range first.Days {
		a, b := first.Days[i].ByModel, second.Days[i].ByModel
		if len(a) != len(b) {
			t.Fatal("model list length varies between calls")
		}
		for j := range a {
			if a[j].Model != b[j].Model {
				t.Fatalf("model order varies: %q then %q", a[j].Model, b[j].Model)
			}
		}
		// And the order must be alphabetical.
		for j := 1; j < len(a); j++ {
			if a[j-1].Model > a[j].Model {
				t.Fatalf("models are not sorted: %q before %q", a[j-1].Model, a[j].Model)
			}
		}
	}
}

// TestDayKeyIsUTC — the day boundary is taken by UTC, because providers reset
// their daily limits by UTC.
func TestDayKeyIsUTC(t *testing.T) {
	if got := dayKey(parseTime(t, "2026-10-17T23:30:00Z")); got != "2026-10-17" {
		t.Fatalf("dayKey(23:30Z) = %q", got)
	}
	if got := dayKey(parseTime(t, "2026-10-18T00:30:00Z")); got != "2026-10-18" {
		t.Fatalf("dayKey(00:30Z) = %q", got)
	}
}

// --- helpers ---

// dayKeyForTest returns today's day key.
func dayKeyForTest() string { return dayKey(time.Now()) }

// parseTime parses a moment in time in RFC3339 format.
func parseTime(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parseTime(%q): %v", s, err)
	}
	return at
}
