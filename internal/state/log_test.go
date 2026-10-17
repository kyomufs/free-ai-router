package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoggerWritesAndReadsBack — the log is written and read back.
func TestLoggerWritesAndReadsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	l, err := OpenLogger(path)
	if err != nil {
		t.Fatalf("OpenLogger: %v", err)
	}
	defer l.Close()

	l.Info("daemon started on %s", "127.0.0.1:8787")
	l.Warn("model %q is unavailable", "m")
	l.Error("refusal: %d", 403)

	lines, err := Tail(path, 10)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("Tail returned %d lines, want 3: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "INFO") || !strings.Contains(lines[0], "daemon started") {
		t.Fatalf("the first line lacks the level or the message: %q", lines[0])
	}
	if !strings.Contains(lines[1], "WARN") {
		t.Fatalf("the second line lacks WARN: %q", lines[1])
	}
	if !strings.Contains(lines[2], "ERROR") {
		t.Fatalf("the third line lacks ERROR: %q", lines[2])
	}
}

// TestTailReturnsLastLines — the last lines are needed, not the first.
func TestTailReturnsLastLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	l, err := OpenLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		l.Info("line %d", i)
	}
	l.Close()

	lines, err := Tail(path, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 5 {
		t.Fatalf("Tail returned %d lines, want 5", len(lines))
	}
	if !strings.Contains(lines[4], "line 49") {
		t.Fatalf("the last line is %q, want the newest entry", lines[4])
	}
	if strings.Contains(lines[0], "line 0") {
		t.Fatal("Tail returned the oldest lines instead of the newest")
	}
}

// TestTailOnMissingFile — a missing log is not an error: the daemon may not
// have written anything yet.
func TestTailOnMissingFile(t *testing.T) {
	lines, err := Tail(filepath.Join(t.TempDir(), "missing.log"), 10)
	if err != nil {
		t.Fatalf("Tail on a missing file: %v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("Tail returned %d lines, want none", len(lines))
	}
}

// TestRotateShiftsGenerations — rotation shifts the generations without
// overwriting them.
func TestRotateShiftsGenerations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	// Fill the file above the rotation threshold.
	big := strings.Repeat("x", rotateAt+1)
	if err := os.WriteFile(path, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	// Put an existing generation in place to check the shift.
	if err := os.WriteFile(path+".1", []byte("the previous generation"), 0o644); err != nil {
		t.Fatal(err)
	}

	l, err := OpenLogger(path)
	if err != nil {
		t.Fatalf("OpenLogger: %v", err)
	}
	defer l.Close()

	// The former generation moved to position 2.
	raw, err := os.ReadFile(path + ".2")
	if err != nil {
		t.Fatalf("the previous generation was not shifted: %v", err)
	}
	if string(raw) != "the previous generation" {
		t.Fatalf("generation 2 = %q, want the previous content", raw)
	}
	// The big file became generation 1.
	if _, err := os.ReadFile(path + ".1"); err != nil {
		t.Fatalf("the rotated file did not become generation 1: %v", err)
	}
	// The current file is empty: it has just been created.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("the new log is %d bytes, want empty", info.Size())
	}
}

// TestRotateKeepsBoundedGenerations — the number of generations is bounded,
// otherwise the log would grow without bound.
//
// A note on the shape: rotate() itself only SHIFTS files — the current one
// moves to .1, and a new one is created on open. Therefore what is checked here
// is the number of kept generations, not the total number of files.
func TestRotateKeepsBoundedGenerations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	for i := 0; i < keepGenerations+3; i++ {
		big := strings.Repeat("y", rotateAt+1)
		if err := os.WriteFile(path, []byte(big), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := rotate(path); err != nil {
			t.Fatalf("rotate #%d: %v", i, err)
		}
	}

	// There must be nothing older than keepGenerations generations.
	if _, err := os.Stat(fmt.Sprintf("%s.%d", path, keepGenerations+1)); err == nil {
		t.Fatalf("generation %d survived, so the log grows without bound", keepGenerations+1)
	}
	// And the generations themselves are in place.
	for i := 1; i <= keepGenerations; i++ {
		if _, err := os.Stat(fmt.Sprintf("%s.%d", path, i)); err != nil {
			t.Fatalf("generation %d is missing: %v", i, err)
		}
	}
}

// TestRotateIsNoopBelowThreshold — a small log is left alone.
func TestRotateIsNoopBelowThreshold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("small"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rotate(path); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Fatal("a small log was rotated")
	}
}

// TestTruncateKeepsTheFile — truncation keeps the file in place rather than
// deleting it: the daemon keeps the descriptor open, and removal would leave it
// writing into nowhere.
func TestTruncateKeepsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte("data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Truncate(path); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the file disappeared after Truncate: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("the file is %d bytes, want empty", info.Size())
	}
}

// TestTruncateOnMissingFile — a missing file is not an error.
func TestTruncateOnMissingFile(t *testing.T) {
	if err := Truncate(filepath.Join(t.TempDir(), "missing.log")); err != nil {
		t.Fatalf("Truncate on a missing file: %v", err)
	}
}

// TestLogFileSizeCountsGenerations — the `data` command must see the full size,
// not just the current file.
func TestLogFileSizeCountsGenerations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".1", []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A foreign file must not make it into the count.
	if err := os.WriteFile(filepath.Join(dir, "other.json"), []byte("miss"), 0o644); err != nil {
		t.Fatal(err)
	}

	bytes, files, err := LogFileSize(path)
	if err != nil {
		t.Fatalf("LogFileSize: %v", err)
	}
	if files != 2 {
		t.Fatalf("files = %d, want 2", files)
	}
	if want := int64(len("current") + len("previous")); bytes != want {
		t.Fatalf("bytes = %d, want %d", bytes, want)
	}
}

// TestLogFilesOrderedNewestFirst — the order is given by hand: sorting the
// names would put the current file before the generations, while it is the
// newest in meaning.
func TestLogFilesOrderedNewestFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	for _, name := range []string{"app.log", "app.log.1", "app.log.2"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := LogFiles(path)
	want := []string{path, path + ".1", path + ".2"}
	if len(got) != len(want) {
		t.Fatalf("LogFiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("LogFiles = %v, want %v", got, want)
		}
	}
}

// TestConcurrentWritesDoNotInterleave — lines do not get mixed up: the log is
// read with the eyes, and a glued-together line is useless.
func TestConcurrentWritesDoNotInterleave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	l, err := OpenLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	const writers, each = 8, 40
	done := make(chan struct{})
	for w := 0; w < writers; w++ {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < each; i++ {
				l.Info("writer %d line %d", id, i)
			}
		}(w)
	}
	for w := 0; w < writers; w++ {
		<-done
	}

	lines, err := Tail(path, writers*each+10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != writers*each {
		t.Fatalf("lines = %d, want %d (writes were lost)", len(lines), writers*each)
	}
	for _, line := range lines {
		// Every line must start with a timestamp and contain exactly one
		// message.
		if !strings.Contains(line, "INFO") {
			t.Fatalf("a line lacks its level: %q", line)
		}
		if strings.Count(line, "writer") != 1 {
			t.Fatalf("lines interleaved: %q", line)
		}
	}
}

// TestNilLoggerIsSafe — calls on a nil log do not panic: that way code which
// was not given a log keeps working.
func TestNilLoggerIsSafe(t *testing.T) {
	var l *Logger
	l.Info("nothing")
	l.Warn("nothing")
	l.Error("nothing")
	if err := l.Close(); err != nil {
		t.Fatalf("Close on nil: %v", err)
	}
	if l.Path() != "" {
		t.Fatalf("Path on nil = %q, want empty", l.Path())
	}
}
