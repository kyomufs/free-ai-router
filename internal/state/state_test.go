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

	s.RecordSuccess(Outcome{Model: "model-a", Key: "aaaa1111"})
	s.RecordSuccess(Outcome{Model: "model-a", Key: "aaaa1111"})
	s.RecordSuccess(Outcome{Model: "model-b", Key: "bbbb2222"})
	s.RecordRefusal(Outcome{Model: "model-a", Key: "aaaa1111"})

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
	if day.Aborted != 0 {
		t.Fatalf("Aborted = %d, want 0", day.Aborted)
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
	s.RecordSuccess(Outcome{})
	s.RecordRefusal(Outcome{})

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

// TestAbortIsNotARefusal — an abort is a client that gave up or an unreachable
// upstream, not the provider refusing anything.
//
// The two are kept apart because the error column of the monitor is read as the
// provider's fault: counting a client's own timeout there would blame the
// provider for it. The live log has lines like
// `connection: Post "...": context canceled` that used to be counted as
// refusals.
func TestAbortIsNotARefusal(t *testing.T) {
	s, _ := openTemp(t)
	s.RecordAbort(Outcome{Model: "m", Address: "213.14.43.234", Key: "k"})

	day := s.Snapshot().Days[0]
	if day.Aborted != 1 {
		t.Fatalf("Aborted = %d, want 1", day.Aborted)
	}
	if day.Refused != 0 || day.OK != 0 {
		t.Fatalf("an abort moved ok/refused: ok %d refused %d", day.OK, day.Refused)
	}
	if len(day.ByAddress) != 1 || day.ByAddress[0].Aborted != 1 {
		t.Fatalf("the address row did not record the abort: %+v", day.ByAddress)
	}
	if len(day.Keys) != 1 {
		t.Fatalf("Keys = %+v, want one entry", day.Keys)
	}
	if day.Keys[0].Aborted != 1 || day.Keys[0].Refused != 0 {
		t.Fatalf("key counters = %+v", day.Keys[0])
	}
	// Only a refusal moves the mark, so an abort must leave it alone.
	if day.Keys[0].LastRefusalAt != 0 {
		t.Fatalf("an abort marked the key as refused: %d", day.Keys[0].LastRefusalAt)
	}
}

// TestAddressBucketsTheMatrix — the terminal monitor draws one row per egress
// address, so an outcome with a known address has to land in three places at
// once: the day totals, the flat per-model list, and the address row.
func TestAddressBucketsTheMatrix(t *testing.T) {
	s, _ := openTemp(t)

	s.RecordSuccess(Outcome{Model: "m", Address: "213.14.43.234", Key: "k"})
	s.RecordSuccess(Outcome{Model: "m", Address: "213.14.43.234", Key: "k"})
	s.RecordRefusal(Outcome{Model: "m", Address: "213.14.43.234", Key: "k"})

	day := s.Snapshot().Days[0]
	if day.OK != 2 || day.Refused != 1 {
		t.Fatalf("day totals = ok %d refused %d, want 2 and 1", day.OK, day.Refused)
	}
	if len(day.ByAddress) != 1 {
		t.Fatalf("ByAddress = %+v, want exactly one row", day.ByAddress)
	}

	row := day.ByAddress[0]
	if row.Address != "213.14.43.234" {
		t.Fatalf("Address = %q", row.Address)
	}
	if row.Family != "v4" {
		t.Fatalf("Family = %q, want v4", row.Family)
	}
	if row.OK != 2 || row.Refused != 1 || row.Aborted != 0 {
		t.Fatalf("address totals = %+v, want ok=2 refused=1 aborted=0", row)
	}
	if len(row.ByModel) != 1 || row.ByModel[0].Model != "m" {
		t.Fatalf("address per-model = %+v", row.ByModel)
	}
	if row.ByModel[0].OK != 2 || row.ByModel[0].Refused != 1 {
		t.Fatalf("address per-model counters = %+v", row.ByModel[0])
	}
}

// TestEmptyAddressCreatesNoEntry — the daemon does not always know its egress
// address.
//
// An empty one must move the totals but must NOT create a row: a row named
// "unknown" would be an invented bucket in the matrix, and the operator would
// read it as a real address that the provider limits.
func TestEmptyAddressCreatesNoEntry(t *testing.T) {
	s, _ := openTemp(t)
	s.RecordSuccess(Outcome{Model: "m", Key: "k"})
	s.RecordAbort(Outcome{Model: "m", Key: "k"})

	day := s.Snapshot().Days[0]
	if len(day.ByAddress) != 0 {
		t.Fatalf("ByAddress = %+v, want no row for an unknown address", day.ByAddress)
	}
	if day.OK != 1 || day.Aborted != 1 {
		t.Fatalf("totals = ok %d aborted %d, want 1 and 1", day.OK, day.Aborted)
	}
}

// TestAddressesAreIndependentBuckets — the provider's anonymous limiter buckets
// quota by the address string, and IPv4 and IPv6 are separate buckets.
//
// Compiling them into one row would hide one of the limits the operator is up
// against, which is the whole reason the matrix exists.
func TestAddressesAreIndependentBuckets(t *testing.T) {
	s, _ := openTemp(t)
	s.RecordSuccess(Outcome{Model: "m", Address: "213.14.43.234"})
	s.RecordRefusal(Outcome{Model: "m", Address: "2a00:1450:4001:800::200e"})
	s.RecordRefusal(Outcome{Model: "m", Address: "2a00:1450:4001:800::200e"})

	day := s.Snapshot().Days[0]
	if len(day.ByAddress) != 2 {
		t.Fatalf("ByAddress = %+v, want two independent rows", day.ByAddress)
	}
	// The order is alphabetical, and digits sort before letters, so the IPv4
	// literal comes first.
	v4, v6 := day.ByAddress[0], day.ByAddress[1]
	if v4.Address != "213.14.43.234" || v4.Family != "v4" {
		t.Fatalf("v4 row = %+v", v4)
	}
	if v4.OK != 1 || v4.Refused != 0 {
		t.Fatalf("v4 counters = %+v", v4)
	}
	if v6.Address != "2a00:1450:4001:800::200e" || v6.Family != "v6" {
		t.Fatalf("v6 row = %+v", v6)
	}
	if v6.Refused != 2 || v6.OK != 0 {
		t.Fatalf("v6 counters = %+v", v6)
	}
}

// TestAddressDimensionSurvivesReload — the matrix is read after a restart, so
// the address dimension has to reach the disk, not only the memory.
func TestAddressDimensionSurvivesReload(t *testing.T) {
	s, path := openTemp(t)
	s.RecordSuccess(Outcome{Model: "m", Address: "213.14.43.234", Key: "k"})
	s.RecordAbort(Outcome{Model: "m", Address: "213.14.43.234", Key: "k"})
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open after Save: %v", err)
	}
	day := reopened.Snapshot().Days[0]
	if len(day.ByAddress) != 1 {
		t.Fatalf("ByAddress = %+v after reload", day.ByAddress)
	}
	row := day.ByAddress[0]
	if row.Address != "213.14.43.234" || row.Family != "v4" {
		t.Fatalf("address row after reload = %+v", row)
	}
	if row.OK != 1 || row.Aborted != 1 || row.Refused != 0 {
		t.Fatalf("address counters after reload = %+v", row)
	}
	if len(row.ByModel) != 1 || row.ByModel[0].Aborted != 1 {
		t.Fatalf("address per-model after reload = %+v", row.ByModel)
	}
}

// TestVersionOneFileStillLoads — a version 1 file predates both the address
// dimension and the third outcome.
//
// Its counters are the operator's data and must survive the upgrade. The two
// things that did not exist must read as "nothing recorded" — not as an error,
// and not as an invented empty row.
func TestVersionOneFileStillLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// The shape a version 1 daemon wrote: no "aborted", no "by_address".
	// The day must be today's, otherwise the retention prune removes it before
	// the assertions and the test would pass for the wrong reason.
	today := dayKeyForTest()
	fixture := `{
  "version": 1,
  "updated_at": 1792253829189,
  "days": {
    "` + today + `": {
      "ok": 321,
      "refused": 28,
      "by_model": {
        "deepseek-v4.1-flash-free": { "ok": 226, "refused": 1, "last_seen": 1792252727001 },
        "mimo-v2.6-flash-free": { "ok": 2, "refused": 10, "last_seen": 1792252727001 }
      }
    }
  }
}`
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open of a version 1 file: %v", err)
	}

	snap := s.Snapshot()
	if len(snap.Days) != 1 {
		t.Fatalf("Days = %d, want 1", len(snap.Days))
	}
	day := snap.Days[0]
	if day.OK != 321 || day.Refused != 28 {
		t.Fatalf("version 1 counters lost: ok %d refused %d", day.OK, day.Refused)
	}
	if day.Aborted != 0 {
		t.Fatalf("Aborted = %d, want 0: the field did not exist in version 1", day.Aborted)
	}
	if len(day.ByAddress) != 0 {
		t.Fatalf("ByAddress = %+v, want empty: the field did not exist in version 1", day.ByAddress)
	}

	byModel := map[string]ModelSnapshot{}
	for _, m := range day.ByModel {
		byModel[m.Model] = m
	}
	if got := byModel["mimo-v2.6-flash-free"]; got.OK != 2 || got.Refused != 10 {
		t.Fatalf("per-model counters lost: %+v", got)
	}

	// The next write stamps the file as version 2 and keeps the old counters.
	s.RecordSuccess(Outcome{Model: "deepseek-v4.1-flash-free"})

	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var shape fileShape
	if uerr := json.Unmarshal(raw, &shape); uerr != nil {
		t.Fatalf("the upgraded file is not valid JSON: %v", uerr)
	}
	if shape.Version != schemaVersion {
		t.Fatalf("version = %d after a write, want %d", shape.Version, schemaVersion)
	}
	if d := shape.Days[today]; d == nil || d.OK != 322 || d.Refused != 28 {
		t.Fatalf("counters after the upgrade write: %+v", d)
	}
}

// TestAddressFamily — the family is read off the literal, because the limiter
// buckets by the string.
//
// A v4-mapped IPv6 literal is therefore an IPv6 string and must not be folded
// in with plain IPv4 addresses: that would merge two buckets into one row.
func TestAddressFamily(t *testing.T) {
	for _, tc := range []struct {
		address string
		want    string
	}{
		{"213.14.43.234", "v4"},
		{"2a00:1450:4001:800::200e", "v6"},
		{"::ffff:213.14.43.234", "v6"},
		{"", ""},
		{"not-an-address", ""},
	} {
		if got := addressFamily(tc.address); got != tc.want {
			t.Fatalf("addressFamily(%q) = %q, want %q", tc.address, got, tc.want)
		}
	}
}

// TestSaveAndReload — the state survives a restart. That is its only job.
func TestSaveAndReload(t *testing.T) {
	s, path := openTemp(t)
	s.RecordSuccess(Outcome{Model: "model", Key: "key1"})
	s.RecordRefusal(Outcome{Model: "model", Key: "key1"})
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
	s.RecordSuccess(Outcome{Model: "model", Key: "k"})
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
	s.RecordSuccess(Outcome{Model: "model", Key: "k"})
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
	s.RecordSuccess(Outcome{Model: "model", Key: "k"})
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
		s.RecordSuccess(Outcome{Model: m, Key: "k"})
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
